package tailscale

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrChangedHostKey = errors.New("SSH host key changed; explicit reconfirmation is required")

type State struct {
	Pins    map[string]string `json:"hostKeyPins"`
	Managed map[string]string `json:"managedPeers"`
}

type StateStore struct {
	mu   sync.Mutex
	path string
}

func NewStateStore(dataDir string) *StateStore {
	return &StateStore{path: filepath.Join(dataDir, "tailscale-deployments.json")}
}

func (s *StateStore) Read() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *StateStore) readLocked() (State, error) {
	state := State{Pins: map[string]string{}, Managed: map[string]string{}}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("inspect Tailscale deployment state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return State{}, errors.New("Tailscale deployment state must be a private regular file")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return State{}, fmt.Errorf("read Tailscale deployment state: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("parse Tailscale deployment state: %w", err)
	}
	if state.Pins == nil {
		state.Pins = map[string]string{}
	}
	if state.Managed == nil {
		state.Managed = map[string]string{}
	}
	return state, nil
}

func (s *StateStore) Save(state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(state)
}

func (s *StateStore) Update(update func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readLocked()
	if err != nil {
		return err
	}
	if update != nil {
		if err := update(&state); err != nil {
			return err
		}
	}
	return s.saveLocked(state)
}

func (s *StateStore) saveLocked(state State) error {
	if state.Pins == nil {
		state.Pins = map[string]string{}
	}
	if state.Managed == nil {
		state.Managed = map[string]string{}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create private Tailscale state directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tailscale-deployments-*")
	if err != nil {
		return fmt.Errorf("create temporary Tailscale state: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("commit Tailscale deployment state: %w", err)
	}
	return nil
}

func (s *StateStore) CheckPin(identity, presented string, confirmedChanged bool) error {
	state, err := s.Read()
	if err != nil {
		return err
	}
	if identity == "" || presented == "" {
		return errors.New("stable peer identity and SSH host-key fingerprint are required")
	}
	if prior := state.Pins[identity]; prior != "" && prior != presented && !confirmedChanged {
		return ErrChangedHostKey
	}
	return nil
}

func (s *StateStore) Pin(identity, fingerprint string) error {
	if identity == "" || fingerprint == "" {
		return errors.New("stable peer identity and fingerprint are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readLocked()
	if err != nil {
		return err
	}
	state.Pins[identity] = fingerprint
	return s.saveLocked(state)
}

func (s *StateStore) Associate(identity, nodeID string) error {
	if identity == "" || nodeID == "" {
		return errors.New("stable peer identity and NodeDance node ID are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readLocked()
	if err != nil {
		return err
	}
	state.Managed[identity] = nodeID
	return s.saveLocked(state)
}

// Disassociate removes a peer mapping only when it still points at nodeID.
// This keeps resolution of an older deployment task from erasing a newer
// association for the same peer.
func (s *StateStore) Disassociate(identity, nodeID string) error {
	if identity == "" || nodeID == "" {
		return errors.New("stable peer identity and NodeDance node ID are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readLocked()
	if err != nil {
		return err
	}
	if state.Managed[identity] != nodeID {
		return nil
	}
	delete(state.Managed, identity)
	return s.saveLocked(state)
}

// Package containerprefs defines the replaceable identity-migration seam used
// by rebuild task reporting. S06 owns the production persistent preference
// store; this package includes an in-memory adapter for integration tests.
package containerprefs

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var ErrIdentityConflict = errors.New("container preference identity already has different values")

type Preferences struct {
	Alias      string
	Icon       string
	Note       string
	ServiceURL string
	Pinned     bool
	Visible    bool
	Order      int
}

type Identity struct {
	NodeID      string
	ContainerID string
}

// Migrator implementations must make the same identity move idempotent:
// Agent task reports are durably retried until Core acknowledges them.
type Migrator interface {
	MigrateContainer(context.Context, string, string, string) error
}

// NopMigrator is the production fallback until the S06 persistent preference
// repository is installed.
type NopMigrator struct{}

func (NopMigrator) MigrateContainer(context.Context, string, string, string) error { return nil }

// MemoryStore is a replaceable test adapter. It moves S06 display preferences
// by identity and never copies them to the stopped rollback container.
type MemoryStore struct {
	mu     sync.RWMutex
	values map[Identity]Preferences
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{values: make(map[Identity]Preferences)}
}

func (s *MemoryStore) Set(ctx context.Context, identity Identity, value Preferences) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || !validIdentity(identity) {
		return errors.New("container preference identity is invalid")
	}
	s.mu.Lock()
	s.values[identity] = value
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, identity Identity) (Preferences, bool, error) {
	if err := ctx.Err(); err != nil {
		return Preferences{}, false, err
	}
	if s == nil || !validIdentity(identity) {
		return Preferences{}, false, errors.New("container preference identity is invalid")
	}
	s.mu.RLock()
	value, ok := s.values[identity]
	s.mu.RUnlock()
	return value, ok, nil
}

func (s *MemoryStore) MigrateContainer(ctx context.Context, nodeID, fromID, toID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	from := Identity{NodeID: nodeID, ContainerID: fromID}
	to := Identity{NodeID: nodeID, ContainerID: toID}
	if s == nil || !validIdentity(from) || !validIdentity(to) || from == to {
		return errors.New("container preference migration identity is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldValue, hasOld := s.values[from]
	newValue, hasNew := s.values[to]
	if !hasOld {
		return nil // no preference to migrate, or a prior idempotent move
	}
	if hasNew && newValue != oldValue {
		return ErrIdentityConflict
	}
	s.values[to] = oldValue
	delete(s.values, from)
	return nil
}

func validIdentity(value Identity) bool {
	return strings.TrimSpace(value.NodeID) != "" && len(value.NodeID) <= 128 &&
		protocol.IsFullContainerID(value.ContainerID)
}

package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	agentupdate "github.com/CST-Cat/NodeDance/internal/agent/update"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	coreupdates "github.com/CST-Cat/NodeDance/internal/core/updates"
	"github.com/google/uuid"
)

const maxReleaseManifestBytes = 64 << 10

func (s *Server) handleAgentUpdateAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if r.URL.Path != "/api/v1/updates" && !strings.HasPrefix(r.URL.Path, "/api/v1/updates/") {
		return false
	}
	if len(s.agentUpdatePublicKey) == 0 {
		http.Error(w, "Agent update trust key is not configured", http.StatusServiceUnavailable)
		return true
	}
	switch r.URL.Path {
	case "/api/v1/updates", "/api/v1/updates/":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return true
		}
		releases, err := s.updates.ListReleases(r.Context())
		if err != nil {
			http.Error(w, "release list unavailable", 500)
			return true
		}
		tasks, err := s.updates.ListTasks(r.Context(), 200)
		if err != nil {
			http.Error(w, "update task list unavailable", 500)
			return true
		}
		settings, err := s.updates.Settings(r.Context())
		if err != nil {
			http.Error(w, "update schedule unavailable", 500)
			return true
		}
		nodes, err := s.agents.ListNodes(r.Context())
		if err != nil {
			http.Error(w, "Agent list unavailable", 500)
			return true
		}
		writeJSON(w, 200, map[string]any{"releases": releases, "tasks": tasks, "settings": settings, "nodes": nodes})
		return true
	case "/api/v1/updates/releases":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return true
		}
		s.uploadAgentRelease(w, r, current)
		return true
	case "/api/v1/updates/tasks":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return true
		}
		items, err := s.updates.ListTasks(r.Context(), 200)
		if err != nil {
			http.Error(w, "update task list unavailable", 500)
			return true
		}
		writeJSON(w, 200, map[string]any{"tasks": items})
		return true
	case "/api/v1/updates/settings":
		if r.Method == http.MethodGet {
			settings, err := s.updates.Settings(r.Context())
			if err != nil {
				http.Error(w, "update schedule unavailable", 500)
				return true
			}
			writeJSON(w, 200, map[string]any{"settings": settings})
			return true
		}
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", 405)
			return true
		}
		var settings coreupdates.Settings
		if !decodeJSON(w, r, &settings) {
			return true
		}
		if err := s.updates.SaveSettings(r.Context(), settings); err != nil {
			http.Error(w, "invalid Agent update schedule", 400)
			return true
		}
		s.auditUpdate(r, current, "agent_update_settings", "succeeded", "")
		writeJSON(w, 200, map[string]any{"settings": settings})
		return true
	case "/api/v1/updates/campaign/resume":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return true
		}
		if err := s.updates.ResumeCampaign(r.Context()); err != nil {
			http.Error(w, "campaign could not be resumed", 500)
			return true
		}
		s.auditUpdate(r, current, "agent_update_campaign_resume", "succeeded", "")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/updates/"), "/")
	if len(parts) == 3 && parts[0] == "nodes" && parts[2] == "update" && validUUID(parts[1]) && r.Method == http.MethodPost {
		s.requestAgentUpdate(w, r, current, parts[1])
		return true
	}
	http.NotFound(w, r)
	return true
}

func (s *Server) uploadAgentRelease(w http.ResponseWriter, r *http.Request, current *session) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(agentupdate.MaxArtifactBytes)+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "invalid or oversized release upload", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	manifestFile, _, err := r.FormFile("manifest")
	if err != nil {
		http.Error(w, "signed manifest is required", 400)
		return
	}
	defer manifestFile.Close()
	raw, err := io.ReadAll(io.LimitReader(manifestFile, maxReleaseManifestBytes+1))
	if err != nil || len(raw) > maxReleaseManifestBytes {
		http.Error(w, "release manifest is invalid", 400)
		return
	}
	var manifest agentupdate.Manifest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		http.Error(w, "release manifest is invalid", 400)
		return
	}
	if err := manifest.Verify(s.agentUpdatePublicKey); err != nil {
		http.Error(w, "release signature or manifest is invalid", 400)
		return
	}
	artifact, header, err := r.FormFile("artifact")
	if err != nil {
		http.Error(w, "Agent artifact is required", 400)
		return
	}
	defer artifact.Close()
	if header.Size != manifest.Size {
		http.Error(w, "artifact size does not match signed manifest", 400)
		return
	}
	id := uuid.NewString()
	directory := filepath.Join(s.dataDir, "agent-updates", "releases", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		http.Error(w, "release storage unavailable", 500)
		return
	}
	_ = os.Chmod(directory, 0700)
	tmp, err := os.CreateTemp(directory, ".upload-")
	if err != nil {
		http.Error(w, "release storage unavailable", 500)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		http.Error(w, "release storage unavailable", 500)
		return
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(artifact, manifest.Size+1))
	if copyErr != nil || written != manifest.Size || hex.EncodeToString(hasher.Sum(nil)) != manifest.SHA256 {
		_ = tmp.Close()
		_ = os.RemoveAll(directory)
		http.Error(w, "artifact digest or size does not match signed manifest", 400)
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.RemoveAll(directory)
		http.Error(w, "release storage unavailable", 500)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.RemoveAll(directory)
		http.Error(w, "release storage unavailable", 500)
		return
	}
	path := filepath.Join(directory, "nodedance-agent")
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.RemoveAll(directory)
		http.Error(w, "release storage unavailable", 500)
		return
	}
	if _, err := s.updates.CreateRelease(r.Context(), id, manifest, path); err != nil {
		_ = os.RemoveAll(directory)
		http.Error(w, "release record could not be saved", 500)
		return
	}
	s.auditUpdate(r, current, "agent_update_release_upload", "succeeded", "")
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "version": manifest.Version, "architecture": manifest.Architecture})
}

func (s *Server) requestAgentUpdate(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	var body struct {
		ReleaseID string `json:"releaseId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !validUUID(body.ReleaseID) {
		http.Error(w, "release ID is invalid", 400)
		return
	}
	if _, err := s.updates.GetRelease(r.Context(), body.ReleaseID); err != nil {
		if errors.Is(err, coreupdates.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "release unavailable", 500)
		}
		return
	}
	task, err := s.updates.CreateTask(r.Context(), nodeID, body.ReleaseID, "manual", "", 0)
	if errors.Is(err, coreupdates.ErrConflict) {
		http.Error(w, "node already has an active Agent update", 409)
		return
	}
	if err != nil {
		http.Error(w, "Agent update task could not be created", 500)
		return
	}
	s.auditUpdate(r, current, "agent_update_request", "succeeded", nodeID)
	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) handleAgentUpdateArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.secureAgentRequest(r) {
		http.Error(w, "secure Agent transport required", http.StatusUpgradeRequired)
		return
	}
	credential, ok := authorizationCredential(r.Header.Get("Authorization"), "Bearer")
	if !ok || !validDeviceCredential(credential) {
		http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
		return
	}
	if _, err := s.agents.IdentityByCredential(r.Context(), auth.DigestToken(credential)); err != nil {
		http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 7 || parts[3] != "agent-updates" || parts[4] != "releases" || !validUUID(parts[5]) || (parts[6] != "artifact" && parts[6] != "manifest") {
		http.NotFound(w, r)
		return
	}
	release, err := s.updates.GetRelease(r.Context(), parts[5])
	if errors.Is(err, coreupdates.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "release unavailable", 500)
		return
	}
	if parts[6] == "manifest" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(release.Manifest)
		return
	}
	path, err := s.updates.ArtifactsPath(r.Context(), release.ID)
	if err != nil {
		http.Error(w, "release unavailable", 404)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "release artifact unavailable", 404)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != release.Manifest.Size {
		http.Error(w, "release artifact unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Content-Disposition", "attachment; filename=\"nodedance-agent\"")
	http.ServeContent(w, r, "nodedance-agent", info.ModTime(), file)
}

func (s *Server) auditUpdate(r *http.Request, current *session, action, outcome, nodeID string) {
	target := audit.Target{}
	if nodeID != "" {
		target = audit.Target{Kind: audit.TargetNode, ID: nodeID}
	}
	actor := sql.NullInt64{}
	if current != nil {
		actor = sql.NullInt64{Int64: 1, Valid: true}
	}
	ctx, remote := context.Background(), "unknown"
	if r != nil {
		ctx = r.Context()
		remote = s.effectiveRemoteAddr(r)
	}
	_ = audit.Record(ctx, s.store.DB, audit.Event{OccurredAt: s.now(), Action: action, Outcome: outcome, ActorID: actor, RemoteAddr: remote, Target: target})
}

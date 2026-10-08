package server

import (
	"context"
	"errors"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type dockerFrame struct {
	sequence uint64
	batch    protocol.DockerBatch
}

func (s *Server) bindDockerConnection(ctx context.Context, identity coredocker.Identity, generation uint64) error {
	s.dockerMu.Lock()
	defer s.dockerMu.Unlock()
	current := s.docker
	if current == nil {
		current = coredocker.NewStore()
	}
	candidate := current.Clone()
	if err := candidate.BindConnection(identity, generation); err != nil {
		return err
	}
	saved, ok := candidate.PersistentNode(identity.NodeID)
	if !ok {
		return errors.New("bound Docker node state is missing")
	}
	if err := persistDockerNode(ctx, s.store.DB, saved, s.now()); err != nil {
		return err
	}
	s.docker = candidate
	return nil
}

func (s *Server) runDockerFrames(ctx context.Context, connection *agentConnection, identity coredocker.Identity, failures chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-connection.dockerFrames:
			if err := s.acceptDockerFrame(ctx, identity, connection.generation, frame); err != nil {
				select {
				case failures <- err:
				default:
				}
				return
			}
		}
	}
}

func (s *Server) acceptDockerFrame(ctx context.Context, identity coredocker.Identity, generation uint64, frame dockerFrame) error {
	changed := false
	s.dockerMu.Lock()
	current := s.docker
	if current == nil {
		s.dockerMu.Unlock()
		return errors.New("Docker store is unavailable")
	}
	// Check the authenticated socket generation before inspecting the batch.
	// In particular, an invalid payload already queued on a superseded socket
	// must not make the newer generation's last-good inventory stale.
	if err := current.ValidateConnection(identity, generation); err != nil {
		s.dockerMu.Unlock()
		return err
	}
	candidate := current.Clone()
	before := current.Revision(identity.NodeID)
	beforeSaved, hasBefore := current.PersistentNode(identity.NodeID)
	err := candidate.Accept(identity, generation, frame.sequence, frame.batch, s.now())
	if err != nil {
		if errors.Is(err, coredocker.ErrStaleSequence) {
			s.dockerMu.Unlock()
			return nil
		}
		if frame.batch.FullSnapshot && isRejectedDockerSnapshot(err) {
			current.MarkStale(identity.NodeID, "docker_snapshot_rejected")
		}
		s.dockerMu.Unlock()
		return err
	}
	after := candidate.Revision(identity.NodeID)
	saved, ok := candidate.PersistentNode(identity.NodeID)
	if !ok {
		s.dockerMu.Unlock()
		return errors.New("accepted Docker node state is missing")
	}
	if after != before {
		if err := persistDockerNode(ctx, s.store.DB, saved, s.now()); err != nil {
			current.MarkStale(identity.NodeID, "docker_persistence_failed")
			s.dockerMu.Unlock()
			return err
		}
		changed = true
	} else if hasBefore && saved.HealthReceived.After(beforeSaved.HealthReceived) {
		if err := persistDockerHealth(ctx, s.store.DB, saved, s.now()); err != nil {
			current.MarkStale(identity.NodeID, "docker_persistence_failed")
			s.dockerMu.Unlock()
			return err
		}
	}
	// Adopt staged chunks too: they are private bounded state and are never
	// visible through API/dashboard until the final chunk commits.
	s.docker = candidate
	s.dockerMu.Unlock()
	if changed {
		s.metrics.Notify(identity.NodeID)
	}
	return nil
}

func isRejectedDockerSnapshot(err error) bool {
	return errors.Is(err, coredocker.ErrInvalidBatch) ||
		errors.Is(err, coredocker.ErrInvalidSnapshot) ||
		errors.Is(err, coredocker.ErrSnapshotExpired) ||
		errors.Is(err, coredocker.ErrSnapshotLimit)
}

func (s *Server) dockerSnapshot(nodeID string, lease *coredocker.Lease, now time.Time) (coredocker.View, bool) {
	view, _, ok := s.dockerSnapshotWithRevision(nodeID, lease, now)
	return view, ok
}

func (s *Server) dockerSnapshotWithRevision(nodeID string, lease *coredocker.Lease, now time.Time) (coredocker.View, uint64, bool) {
	s.dockerMu.Lock()
	store := s.docker
	if store == nil {
		s.dockerMu.Unlock()
		return coredocker.View{}, 0, false
	}
	view, ok := store.SnapshotAt(nodeID, lease, now)
	revision := store.Revision(nodeID)
	s.dockerMu.Unlock()
	return view, revision, ok
}

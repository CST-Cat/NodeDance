package server

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	coreupdates "github.com/CST-Cat/NodeDance/internal/core/updates"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func (s *Server) agentUpdateScheduler() {
	defer s.agentWait.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if s.agentContext.Err() != nil {
			return
		}
		s.runAgentUpdateScheduler(s.agentContext)
		select {
		case <-s.agentContext.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) runAgentUpdateScheduler(ctx context.Context) {
	settings, err := s.updates.Settings(ctx)
	if err != nil {
		return
	}
	now := s.now().UTC()
	if settings.AutoEnabled && !settings.CampaignPaused && settings.ReleaseID != "" && insideUpdateWindow(now, settings.WindowStartMinute, settings.WindowEndMinute) {
		release, releaseErr := s.updates.GetRelease(ctx, settings.ReleaseID)
		nodes, nodesErr := s.agents.ListNodes(ctx)
		if releaseErr == nil && nodesErr == nil {
			targets := compatibleAgentUpdateTargets(nodes, release)
			if len(targets) > 0 {
				_ = s.updates.StartCampaign(ctx, targets, settings.ReleaseID, now.Format("2006-01-02"), settings.BatchSize)
			}
		}
	}
	pending, err := s.updates.Pending(ctx, 64)
	if err != nil {
		return
	}
	for _, task := range pending {
		s.dispatchAgentUpdate(ctx, task)
	}
}

func compatibleAgentUpdateTargets(nodes []agents.Node, release coreupdates.Release) []string {
	targets := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.Status != "online" || !hasCapability(node.Capabilities, protocol.CapabilityAgentUpdatesPreparedAck) {
			continue
		}
		if node.Permissions.OS != release.OS || node.Permissions.Architecture != release.Architecture {
			continue
		}
		targets = append(targets, node.NodeID)
	}
	return targets
}

func insideUpdateWindow(now time.Time, start, end int) bool {
	minute := now.Hour()*60 + now.Minute()
	if start == end {
		return false
	}
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

func (s *Server) dispatchAgentUpdate(ctx context.Context, task coreupdates.Task) {
	connection := s.agentConnectionForNode(task.NodeID)
	if connection == nil {
		s.deferAgentUpdate(ctx, task, "Agent is offline")
		return
	}
	if connection.stagedUpdateTaskID == task.ID {
		// This connection has a durable staged journal for this task and will
		// resend its prepared report. Never dispatch the artifact command twice.
		return
	}
	if !connection.updateEnabled {
		s.failAgentUpdate(ctx, task, "Agent does not support signed updates")
		return
	}
	if connection.updateBaseURL == "" {
		s.deferAgentUpdate(ctx, task, "Core has no secure Agent artifact URL")
		return
	}
	connection.taskMu.RLock()
	busy := len(connection.taskOutstanding) > 0
	connection.taskMu.RUnlock()
	connection.streamMu.Lock()
	busy = busy || len(connection.streams) > 0
	connection.streamMu.Unlock()
	connection.fileMu.Lock()
	busy = busy || len(connection.fileTransfers) > 0
	connection.fileMu.Unlock()
	busy = busy || s.terminals != nil && s.terminals.hasLiveConnection(connection, s.now())
	if busy {
		s.deferAgentUpdate(ctx, task, "deferred while Agent task, stream, terminal session, or file transfer is active")
		return
	}
	release, err := s.updates.GetRelease(ctx, task.ReleaseID)
	if err != nil {
		s.failAgentUpdate(ctx, task, "release is unavailable")
		return
	}
	manifest, err := json.Marshal(release.Manifest)
	if err != nil {
		s.failAgentUpdate(ctx, task, "release metadata is invalid")
		return
	}
	payload, err := json.Marshal(protocol.AgentUpdateCommand{TaskID: task.ID, Manifest: manifest, ArtifactURL: strings.TrimSuffix(connection.updateBaseURL, "/") + "/api/v1/agent-updates/releases/" + url.PathEscape(release.ID) + "/artifact", CoreVersion: s.version})
	if err != nil {
		s.failAgentUpdate(ctx, task, "update command could not be encoded")
		return
	}
	if err := s.updates.SetTaskStatus(ctx, task.ID, "dispatched", ""); err != nil {
		return
	}
	command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeAgentUpdate, Generation: connection.generation, RequestID: task.ID, Payload: payload}
	select {
	case connection.commands <- command:
	default:
		s.deferAgentUpdate(ctx, task, "Agent control queue is full")
	}
}

func (s *Server) deferAgentUpdate(ctx context.Context, task coreupdates.Task, reason string) {
	if task.Status != "deferred" || task.Reason != reason {
		_ = s.updates.SetTaskStatus(ctx, task.ID, "deferred", reason)
	}
}
func (s *Server) failAgentUpdate(ctx context.Context, task coreupdates.Task, reason string) {
	_ = s.updates.FailTask(ctx, task.ID, reason)
}

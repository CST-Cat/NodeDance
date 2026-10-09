package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	coremetrics "github.com/CST-Cat/NodeDance/internal/core/metrics"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const agentHelloTimeout = 5 * time.Second

type agentConnection struct {
	conn                     *websocket.Conn
	ctx                      context.Context
	cancel                   context.CancelFunc
	agentID                  string
	nodeID                   string
	generation               uint64
	metricsEnabled           bool
	dockerEnabled            bool
	taskEnabled              bool
	streamEnabled            bool
	composeEnabled           bool
	imagesEnabled            bool
	terminalEnabled          bool
	filesEnabled             bool
	imageResponseMu          sync.Mutex
	imageResponses           map[string]chan protocol.ImageListResponse
	composeEditorEnabled     bool
	taskSignal               chan struct{}
	taskMu                   sync.RWMutex
	taskJournalID            string
	taskCapacity             int
	taskSlots                int
	taskSynced               bool
	taskOutstanding          map[string]struct{}
	taskReconcileOutstanding map[string]struct{}
	taskReconcileAttempted   map[string]struct{}
	taskSnapshot             *agentTaskSnapshot
	dockerFrames             chan dockerFrame
	commands                 chan protocol.Envelope
	streamMu                 sync.Mutex
	streams                  map[string]*coreBrowserStream
	streamTombstones         map[string]struct{}
	streamTombstoneOrder     []string
	composeMu                sync.Mutex
	composeWaiters           map[string]composeWaiter
	fileMu                   sync.Mutex
	fileTransfers            map[string]*coreFileTransfer
	fileTombstones           map[string]struct{}
	fileTombstoneOrder       []string
	rebuildPlanMu            sync.Mutex
	rebuildPlanWaiters       map[string]rebuildPlanWaiter
	leaseUpdates             chan time.Time
	watchMu                  sync.Mutex
	watchCancel              context.CancelFunc
	watchStopped             bool
	watchDone                chan struct{}
}

type agentRead struct {
	typeID websocket.MessageType
	data   []byte
	err    error
}

func (c *agentConnection) close() {
	if c == nil {
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.conn != nil {
		_ = c.conn.CloseNow()
	}
	c.closeBrowserStreams()
	c.closeFileTransfers()
	c.stopLeaseWatch()
}

func (c *agentConnection) stopLeaseWatch() {
	c.watchMu.Lock()
	c.watchStopped = true
	if c.watchCancel != nil {
		c.watchCancel()
		c.watchCancel = nil
	}
	c.watchMu.Unlock()
}

func (c *agentConnection) enqueue(command protocol.Envelope) {
	select {
	case c.commands <- command:
	default:
		// The rotation request is durable in SQLite and will be included in the
		// next welcome if a bounded live-command queue is full.
	}
}

func (c *agentConnection) tryEnqueue(command protocol.Envelope) bool {
	if c == nil {
		return false
	}
	select {
	case c.commands <- command:
		return true
	default:
		return false
	}
}

func (c *agentConnection) noteHeartbeat(acceptedAt time.Time) {
	select {
	case c.leaseUpdates <- acceptedAt:
	default:
		select {
		case <-c.leaseUpdates:
		default:
		}
		select {
		case c.leaseUpdates <- acceptedAt:
		default:
		}
	}
}

func (s *Server) agentOfflineSweeper() {
	defer s.agentWait.Done()
	ticker := time.NewTicker(s.agentSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.agentContext.Done():
			return
		case <-ticker.C:
			s.expireImageCredentials(s.now())
			s.expireAgentLeases(s.agentContext)
		}
	}
}

func (s *Server) expireAgentLeases(ctx context.Context) {
	leases, err := s.agents.MarkExpiredLeases(ctx, s.agentOfflineTimeout)
	if err != nil {
		return
	}
	for _, lease := range leases {
		s.metrics.Notify(lease.NodeID)
		s.closeAgentConnection(lease.AgentID, lease.Generation)
	}
}

// watchAgentLease remains alive for the full lease even after the WebSocket
// drops. It uses persisted heartbeat timestamps and a generation CAS so an
// old connection can never expire a newer lease.
func (s *Server) watchAgentLease(connection *agentConnection, identity agents.Identity, initial time.Time) {
	watchCtx, cancel, ok := s.startAgentLeaseWatcher(connection, identity.AgentID)
	if !ok {
		return
	}
	defer cancel()
	defer func() {
		s.finishAgentLeaseWatcher(identity.AgentID, connection)
		close(connection.watchDone)
		s.agentWait.Done()
	}()
	lastSeen := initial
	timer := time.NewTimer(max(time.Duration(0), lastSeen.Add(s.agentOfflineTimeout).Sub(s.now())))
	defer timer.Stop()
	for {
		select {
		case <-watchCtx.Done():
			return
		case seen := <-connection.leaseUpdates:
			if seen.After(lastSeen) {
				lastSeen = seen
			}
			resetLeaseTimer(timer, max(time.Duration(0), lastSeen.Add(s.agentOfflineTimeout).Sub(s.now())))
		case <-timer.C:
			expired, err := s.agents.ExpireLease(watchCtx, identity.NodeID, connection.generation, s.agentOfflineTimeout)
			if err != nil {
				return
			}
			if expired {
				s.metrics.Notify(identity.NodeID)
				s.closeAgentConnection(identity.AgentID, connection.generation)
				return
			}
			current, active, err := s.agents.ActiveLeaseLastSeen(watchCtx, identity.NodeID, connection.generation)
			if err != nil || !active {
				return
			}
			lastSeen = current
			resetLeaseTimer(timer, max(time.Duration(0), lastSeen.Add(s.agentOfflineTimeout).Sub(s.now())))
		}
	}
}

func (s *Server) beginAgentHandler() bool {
	s.agentLifecycleMu.Lock()
	defer s.agentLifecycleMu.Unlock()
	if s.agentClosing {
		return false
	}
	s.agentWait.Add(1)
	return true
}

func (s *Server) startAgentLeaseWatcher(connection *agentConnection, agentID string) (context.Context, context.CancelFunc, bool) {
	s.agentLifecycleMu.Lock()
	defer s.agentLifecycleMu.Unlock()
	if s.agentClosing {
		return nil, nil, false
	}
	s.agentConnectionsMu.Lock()
	previous := s.agentLeaseWatchers[agentID]
	if previous != nil && previous.generation >= connection.generation {
		s.agentConnectionsMu.Unlock()
		return nil, nil, false
	}
	if previous != nil {
		delete(s.agentLeaseWatchers, agentID)
		previous.stopLeaseWatch()
	}
	s.agentConnectionsMu.Unlock()
	// Do not let disconnected generations accumulate timer goroutines. The
	// ordinary disconnect keeps its watcher until this newer authenticated
	// generation takes ownership.
	if previous != nil && previous.watchDone != nil {
		<-previous.watchDone
	}
	watchCtx, cancel := context.WithCancel(s.agentContext)
	connection.watchMu.Lock()
	if connection.watchStopped {
		connection.watchMu.Unlock()
		cancel()
		return nil, nil, false
	}
	connection.watchCancel = cancel
	connection.watchDone = make(chan struct{})
	connection.watchMu.Unlock()
	s.agentConnectionsMu.Lock()
	s.agentLeaseWatchers[agentID] = connection
	s.agentConnectionsMu.Unlock()
	s.agentWait.Add(1)
	return watchCtx, cancel, true
}

func (s *Server) finishAgentLeaseWatcher(agentID string, connection *agentConnection) {
	s.agentConnectionsMu.Lock()
	if s.agentLeaseWatchers[agentID] == connection {
		delete(s.agentLeaseWatchers, agentID)
	}
	s.agentConnectionsMu.Unlock()
}

func resetLeaseTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (s *Server) closeAgentConnection(agentID string, expectedGeneration uint64) {
	var closed *agentConnection
	s.agentConnectionsMu.Lock()
	connection := s.agentConnections[agentID]
	if connection != nil && (expectedGeneration == 0 || connection.generation == expectedGeneration) {
		delete(s.agentConnections, agentID)
		connection.close()
		closed = connection
	}
	s.agentConnectionsMu.Unlock()
	if closed != nil && s.terminals != nil {
		s.terminals.closeAgent(s, closed.agentID, closed.generation, "Agent disconnected")
	}
}

func (s *Server) detachAgentConnection(agentID string, expectedGeneration uint64) {
	var detached *agentConnection
	s.agentConnectionsMu.Lock()
	if connection := s.agentConnections[agentID]; connection != nil && connection.generation == expectedGeneration {
		delete(s.agentConnections, agentID)
		detached = connection
	}
	s.agentConnectionsMu.Unlock()
	if detached != nil && s.terminals != nil {
		s.terminals.closeAgent(s, detached.agentID, detached.generation, "Agent disconnected")
	}
}

func (s *Server) installAgentConnection(connection *agentConnection, agentID string) bool {
	s.agentConnectionsMu.Lock()
	old := s.agentConnections[agentID]
	if old != nil && old.generation >= connection.generation {
		s.agentConnectionsMu.Unlock()
		return false
	}
	s.agentConnections[agentID] = connection
	s.agentConnectionsMu.Unlock()
	if old != nil {
		old.close()
		if s.terminals != nil {
			s.terminals.closeAgent(s, old.agentID, old.generation, "Agent reconnected")
		}
	}
	return true
}

func (s *Server) handleAgentWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	credential, ok := authorizationCredential(r.Header.Get("Authorization"), "Bearer")
	if !ok || !validDeviceCredential(credential) {
		http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
		return
	}
	if !s.secureAgentRequest(r) {
		http.Error(w, "secure Agent transport required", http.StatusUpgradeRequired)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins are not permitted on the Agent channel", http.StatusForbidden)
		return
	}
	if !s.beginAgentHandler() {
		http.Error(w, "Core is shutting down", http.StatusServiceUnavailable)
		return
	}
	defer s.agentWait.Done()
	digest := auth.DigestToken(credential)
	identity, err := s.agents.IdentityByCredential(r.Context(), digest)
	if errors.Is(err, agents.ErrUnauthorized) || errors.Is(err, agents.ErrRevoked) {
		http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "Agent authentication failed", http.StatusInternalServerError)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	conn.SetReadLimit(protocol.MaxMessageBytes)
	defer conn.Close(websocket.StatusNormalClosure, "Agent connection closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	firstCtx, firstCancel := context.WithTimeout(ctx, agentHelloTimeout)
	messageType, raw, err := conn.Read(firstCtx)
	firstCancel()
	if err != nil {
		s.closeAgentProtocol(conn, protocolCloseStatus(err), "Agent hello was not received")
		return
	}
	if messageType != websocket.MessageText {
		s.closeAgentProtocol(conn, websocket.StatusUnsupportedData, "Agent messages must be text JSON")
		return
	}
	envelope, err := decodeAgentEnvelope(raw)
	if err != nil {
		s.writeAgentError(ctx, conn, "invalid_json", "Agent hello is invalid")
		s.closeAgentProtocol(conn, websocket.StatusInvalidFramePayloadData, "invalid Agent hello")
		return
	}
	if envelope.Version < protocol.MinimumVersion || envelope.Version > protocol.CurrentVersion {
		s.writeAgentError(ctx, conn, "incompatible_protocol", "Agent protocol version is not supported")
		s.closeAgentProtocol(conn, websocket.StatusPolicyViolation, "incompatible Agent protocol")
		return
	}
	if envelope.Type != protocol.TypeHello || envelope.Generation != 0 || envelope.Sequence != 0 || len(envelope.Payload) == 0 {
		s.writeAgentError(ctx, conn, "invalid_hello", "first Agent message must be hello")
		s.closeAgentProtocol(conn, websocket.StatusPolicyViolation, "invalid Agent hello")
		return
	}
	var hello protocol.Hello
	if err := decodeAgentPayload(envelope.Payload, &hello); err != nil || !validHello(hello) {
		s.writeAgentError(ctx, conn, "invalid_hello", "Agent identity or capabilities are invalid")
		s.closeAgentProtocol(conn, websocket.StatusPolicyViolation, "invalid Agent hello")
		return
	}
	if hello.AgentID != identity.AgentID || hello.NodeID != identity.NodeID {
		s.writeAgentError(ctx, conn, "identity_mismatch", "device credential is not bound to the claimed identity")
		s.closeAgentProtocol(conn, websocket.StatusPolicyViolation, "Agent identity mismatch")
		return
	}
	capabilities, err := json.Marshal(hello.Capabilities)
	if err != nil {
		s.closeAgentProtocol(conn, websocket.StatusInternalError, "could not record Agent capabilities")
		return
	}
	lease, err := s.agents.BeginConnection(ctx, identity, digest, envelope.Version, hello.AgentVersion, string(capabilities), hello.Permissions)
	if errors.Is(err, agents.ErrUnauthorized) || errors.Is(err, agents.ErrRevoked) || errors.Is(err, agents.ErrConflict) {
		s.writeAgentError(ctx, conn, "credential_rejected", "Agent credential is no longer valid")
		s.closeAgentProtocol(conn, websocket.StatusPolicyViolation, "Agent credential rejected")
		return
	}
	if err != nil {
		s.closeAgentProtocol(conn, websocket.StatusInternalError, "could not create Agent connection lease")
		return
	}
	connectionCtx, connectionCancel := context.WithCancel(r.Context())
	negotiatedCapabilities := negotiateCapabilities(hello.Capabilities)
	managed := &agentConnection{conn: conn, cancel: connectionCancel, agentID: identity.AgentID, generation: lease.ConnectionGeneration,
		ctx:            connectionCtx,
		metricsEnabled: hasCapability(negotiatedCapabilities, protocol.CapabilityMetrics),
		dockerEnabled:  hasCapability(negotiatedCapabilities, protocol.CapabilityDocker),
		taskEnabled:    hasCapability(negotiatedCapabilities, protocol.CapabilityTaskBridge),
		streamEnabled:  hasCapability(negotiatedCapabilities, protocol.CapabilityContainerStreams), nodeID: identity.NodeID,
		composeEnabled:  hasCapability(negotiatedCapabilities, protocol.CapabilityCompose),
		imagesEnabled:   hasCapability(negotiatedCapabilities, protocol.CapabilityImages),
		terminalEnabled: hasCapability(negotiatedCapabilities, protocol.CapabilityTerminal),
		filesEnabled:    hasCapability(negotiatedCapabilities, protocol.CapabilityFiles),
		imageResponses:  make(map[string]chan protocol.ImageListResponse),
		taskSignal:      make(chan struct{}, 1), taskOutstanding: make(map[string]struct{}),
		composeEditorEnabled:     hasCapability(negotiatedCapabilities, protocol.CapabilityComposeEditor),
		taskReconcileOutstanding: make(map[string]struct{}), taskReconcileAttempted: make(map[string]struct{}),
		commands: make(chan protocol.Envelope, 32), leaseUpdates: make(chan time.Time, 1)}
	if managed.dockerEnabled {
		managed.dockerFrames = make(chan dockerFrame, 16)
	}
	if managed.streamEnabled {
		managed.streams = make(map[string]*coreBrowserStream)
		managed.streamTombstones = make(map[string]struct{})
	}
	if managed.composeEnabled {
		managed.composeWaiters = make(map[string]composeWaiter)
	}
	if managed.filesEnabled {
		managed.fileTransfers = make(map[string]*coreFileTransfer)
		managed.fileTombstones = make(map[string]struct{})
	}
	go s.watchAgentLease(managed, lease.Identity, lease.LastSeenAt)
	if !s.installAgentConnection(managed, identity.AgentID) {
		managed.close()
		s.closeAgentProtocol(conn, websocket.StatusPolicyViolation, "a newer Agent connection is already active")
		return
	}
	metricsIdentity := coremetrics.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID}
	if err := s.metrics.BindConnection(metricsIdentity, lease.ConnectionGeneration); err != nil {
		s.closeAgentConnection(identity.AgentID, lease.ConnectionGeneration)
		s.closeAgentProtocol(conn, websocket.StatusPolicyViolation, "could not bind metrics connection")
		return
	}
	if managed.dockerEnabled {
		if err := s.bindDockerConnection(ctx, coredocker.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID}, lease.ConnectionGeneration); err != nil {
			s.closeAgentConnection(identity.AgentID, lease.ConnectionGeneration)
			s.closeAgentProtocol(conn, websocket.StatusInternalError, "could not bind Docker inventory connection")
			return
		}
	}
	defer s.metrics.UnbindConnection(metricsIdentity, lease.ConnectionGeneration)
	s.metrics.Notify(identity.NodeID)
	defer s.detachAgentConnection(identity.AgentID, lease.ConnectionGeneration)
	welcome := protocol.Welcome{
		AgentID: lease.AgentID, NodeID: lease.NodeID, Generation: lease.ConnectionGeneration,
		HeartbeatIntervalSecs: protocol.HeartbeatIntervalSeconds, OfflineAfterSecs: protocol.OfflineAfterSeconds,
		Capabilities:        negotiatedCapabilities,
		RotationRequestedID: lease.RotationRequestedID, CredentialRotationDone: lease.RotationCommitted,
	}
	if err := s.writeAgentEnvelope(connectionCtx, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeWelcome, Generation: lease.ConnectionGeneration, Payload: marshalAgentPayload(welcome)}); err != nil {
		return
	}
	s.runAgentConnection(connectionCtx, managed, lease.Identity)
}

func (s *Server) runAgentConnection(ctx context.Context, connection *agentConnection, identity agents.Identity) {
	if connection.composeEnabled && connection.composeEditorEnabled {
		go s.reconcileUnknownComposeEditorOperations(ctx, connection)
	}
	readMessages := make(chan agentRead, 1)
	go func() {
		for {
			messageType, data, err := connection.conn.Read(ctx)
			select {
			case readMessages <- agentRead{typeID: messageType, data: data, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	dockerFailures := make(chan error, 1)
	var dockerWorkerDone chan struct{}
	if connection.dockerEnabled {
		workerCtx, cancelWorker := context.WithCancel(ctx)
		dockerWorkerDone = make(chan struct{})
		go func() {
			defer close(dockerWorkerDone)
			s.runDockerFrames(workerCtx, connection, coredocker.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID}, dockerFailures)
		}()
		defer func() {
			cancelWorker()
			<-dockerWorkerDone
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case command := <-connection.commands:
			if command.Generation != connection.generation {
				clear(command.Payload)
				continue
			}
			err := s.writeAgentEnvelope(ctx, connection.conn, command)
			clear(command.Payload)
			if err != nil {
				return
			}
		case <-connection.taskSignal:
			if connection.taskEnabled {
				if err := s.dispatchAgentTasks(ctx, connection, identity); err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusInternalError, "could not dispatch durable task")
					return
				}
			}
		case err := <-dockerFailures:
			if err != nil {
				s.closeAgentProtocol(connection.conn, websocket.StatusInternalError, "could not process Docker inventory")
				return
			}
		case message := <-readMessages:
			if message.err != nil {
				return
			}
			if message.typeID != websocket.MessageText {
				s.closeAgentProtocol(connection.conn, websocket.StatusUnsupportedData, "Agent messages must be text JSON")
				return
			}
			envelope, err := decodeAgentEnvelope(message.data)
			if err != nil {
				s.writeAgentError(ctx, connection.conn, "invalid_json", "Agent message is invalid")
				s.closeAgentProtocol(connection.conn, websocket.StatusInvalidFramePayloadData, "invalid Agent message")
				return
			}
			if envelope.Version < protocol.MinimumVersion || envelope.Version > protocol.CurrentVersion {
				s.writeAgentError(ctx, connection.conn, "incompatible_protocol", "Agent protocol version is not supported")
				s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "incompatible Agent protocol")
				return
			}
			if envelope.Generation != connection.generation {
				s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "stale Agent connection generation")
				return
			}
			switch envelope.Type {
			case protocol.TypeHeartbeat:
				var heartbeat protocol.Heartbeat
				if err := decodeAgentPayload(envelope.Payload, &heartbeat); err != nil || len(heartbeat.Capabilities) != 0 {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid Agent heartbeat")
					return
				}
				acceptedAt, err := s.agents.AcceptHeartbeat(ctx, identity, connection.generation, envelope.Sequence, s.agentOfflineTimeout)
				if errors.Is(err, agents.ErrStaleConnection) {
					_, _ = s.agents.ExpireLease(ctx, identity.NodeID, connection.generation, s.agentOfflineTimeout)
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "stale Agent heartbeat")
					return
				}
				if err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusInternalError, "could not record Agent heartbeat")
					return
				}
				connection.noteHeartbeat(acceptedAt)
				s.metrics.Notify(identity.NodeID)
				ack := protocol.HeartbeatAck{AcceptedAt: acceptedAt.UnixNano()}
				if err := s.writeAgentEnvelope(ctx, connection.conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeatAck, Generation: connection.generation, Sequence: envelope.Sequence, Payload: marshalAgentPayload(ack)}); err != nil {
					return
				}
			case protocol.TypeMetrics:
				if !connection.metricsEnabled {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent metrics capability was not negotiated")
					return
				}
				var report protocol.MetricsSnapshot
				if envelope.Sequence == 0 || decodeAgentPayload(envelope.Payload, &report) != nil || protocol.ValidateAgentMetricsSnapshot(report) != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid Agent metrics report")
					return
				}
				lastSeen, active, err := s.agents.ActiveLeaseLastSeen(ctx, identity.NodeID, connection.generation)
				if err != nil {
					return
				}
				receivedAt := s.now()
				if !active || receivedAt.Before(lastSeen) || !receivedAt.Before(lastSeen.Add(s.agentOfflineTimeout)) {
					continue
				}
				metricsIdentity := coremetrics.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID}
				if err := s.metrics.Accept(metricsIdentity, connection.generation, envelope.Sequence, report, receivedAt); err != nil {
					// A duplicate or delayed telemetry frame is discarded without
					// changing heartbeat sequence or validity.
					continue
				}
				if err := s.history.AppendSnapshot(ctx, identity.NodeID, report, receivedAt); err != nil {
					// Do not silently create a hole in durable history after Core has
					// accepted the live sample. A reconnect can resume when SQLite is
					// available; the missing interval remains an explicit chart gap.
					return
				}
				s.metrics.Notify(identity.NodeID)
			case protocol.TypeDocker:
				if !connection.dockerEnabled || envelope.Sequence == 0 {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent Docker capability was not negotiated")
					return
				}
				batch, err := protocol.UnmarshalDockerBatch(envelope.Payload)
				if err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid Agent Docker inventory")
					return
				}
				frame := dockerFrame{sequence: envelope.Sequence, batch: batch}
				select {
				case connection.dockerFrames <- frame:
				default:
					// A lost ordered chunk invalidates the current snapshot. Force a
					// reconnect so the Agent starts a fresh full inventory scan.
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Docker inventory queue overflow")
					return
				}
			case protocol.TypeTerminalFrame:
				if !connection.terminalEnabled || envelope.Sequence != 0 {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent terminal capability was not negotiated")
					return
				}
				var frame protocol.TerminalFrame
				if err := decodeAgentPayload(envelope.Payload, &frame); err != nil || protocol.ValidateTerminalFrame(frame, true) != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid Agent terminal frame")
					return
				}
				s.terminals.handleAgentFrame(s, connection, identity, frame)
			case protocol.TypeRotatePrepare:
				var rotation protocol.RotatePrepare
				if err := decodeAgentPayload(envelope.Payload, &rotation); err != nil || !validUUID(rotation.RotationID) || !validDeviceCredential(rotation.NewCredential) {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid credential rotation message")
					return
				}
				err := s.agents.PrepareRotation(ctx, identity, connection.generation, rotation.RotationID, auth.DigestToken(rotation.NewCredential))
				if errors.Is(err, agents.ErrConflict) || errors.Is(err, agents.ErrRevoked) || errors.Is(err, agents.ErrStaleConnection) {
					s.writeAgentError(ctx, connection.conn, "rotation_rejected", "credential rotation is no longer valid")
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "credential rotation rejected")
					return
				}
				if err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusInternalError, "could not persist pending credential")
					return
				}
				accepted := protocol.RotateAccepted{RotationID: rotation.RotationID}
				if err := s.writeAgentEnvelope(ctx, connection.conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeRotateAccepted, Generation: connection.generation, RequestID: rotation.RotationID, Payload: marshalAgentPayload(accepted)}); err != nil {
					return
				}
			case protocol.TypeTaskJournalHello, protocol.TypeTaskSnapshotPage, protocol.TypeTaskReport:
				if !connection.taskEnabled {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent task bridge was not negotiated")
					return
				}
				if err := s.handleAgentTaskMessage(ctx, connection, identity, envelope); err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid or unpersisted Agent task message")
					return
				}
			case protocol.TypeImageListResponse:
				if !connection.imagesEnabled || envelope.Sequence != 0 || envelope.RequestID == "" {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent image capability was not negotiated")
					return
				}
				var response protocol.ImageListResponse
				if decodeImagePayload(envelope.Payload, &response) != nil || protocol.ValidateImageListResponse(envelope, response, connection.generation) != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid Agent image list response")
					return
				}
				connection.resolveImageResponse(envelope.RequestID, response)
			case protocol.TypeContainerStreamReady, protocol.TypeContainerLog, protocol.TypeContainerStats,
				protocol.TypeContainerStreamError, protocol.TypeContainerStreamEnd, protocol.TypeContainerStreamHeartbeat:
				if !connection.streamEnabled {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent container streams were not negotiated")
					return
				}
				if err := s.handleAgentContainerStreamMessage(connection, envelope); err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid Agent container stream frame")
					return
				}
			case protocol.TypeComposeResponse:
				if !connection.composeEnabled || envelope.Sequence != 0 || envelope.RequestID == "" {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent Compose capability was not negotiated")
					return
				}
				if err := s.handleAgentComposeResponse(ctx, connection, identity.NodeID, envelope); err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid or unpersisted Agent Compose response")
					return
				}
			case protocol.TypeFileResponse, protocol.TypeFileChunk:
				if !connection.filesEnabled {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent file service was not negotiated")
					return
				}
				if err := s.handleAgentFileMessage(connection, envelope); err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid Agent file transfer frame")
					return
				}
			case protocol.TypeContainerRebuildPlanResponse:
				if !connection.taskEnabled || envelope.Sequence != 0 {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent task bridge was not negotiated")
					return
				}
				if err := s.handleAgentRebuildPlanResponse(connection, envelope); err != nil {
					s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "invalid container rebuild plan response")
					return
				}
			case protocol.TypeHello:
				s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "Agent hello is only valid at connection start")
				return
			default:
				s.writeAgentError(ctx, connection.conn, "unknown_message", "Agent message type is not supported")
				s.closeAgentProtocol(connection.conn, websocket.StatusPolicyViolation, "unknown Agent message")
				return
			}
		}
	}
}

func (s *Server) writeAgentEnvelope(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	defer clear(data)
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}

func (s *Server) writeAgentError(ctx context.Context, conn *websocket.Conn, code, message string) {
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeProtocolError, Payload: marshalAgentPayload(protocol.ProtocolError{Code: code, Message: message})}
	_ = s.writeAgentEnvelope(ctx, conn, envelope)
}

func (s *Server) closeAgentProtocol(conn *websocket.Conn, status websocket.StatusCode, message string) {
	_ = conn.Close(status, message)
}

func decodeAgentEnvelope(raw []byte) (protocol.Envelope, error) {
	var envelope protocol.Envelope
	if len(raw) == 0 || len(raw) > protocol.MaxMessageBytes {
		return envelope, errors.New("Agent frame length is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return envelope, errors.New("trailing Agent JSON data")
	}
	return envelope, nil
}

func decodeAgentPayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return errors.New("Agent payload length is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing Agent payload data")
	}
	return nil
}

func marshalAgentPayload(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func validHello(hello protocol.Hello) bool {
	if !validUUID(hello.AgentID) || !validUUID(hello.NodeID) || strings.TrimSpace(hello.AgentVersion) == "" || len(hello.AgentVersion) > 80 || len(hello.Capabilities) > 32 || !validRuntimePermissions(hello.Permissions) {
		return false
	}
	seen := make(map[string]struct{}, len(hello.Capabilities))
	for _, capability := range hello.Capabilities {
		if len(capability) == 0 || len(capability) > 80 {
			return false
		}
		for _, char := range capability {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '-' || char == '_') {
				return false
			}
		}
		if _, duplicate := seen[capability]; duplicate {
			return false
		}
		seen[capability] = struct{}{}
	}
	return true
}

func negotiateCapabilities(reported []string) []string {
	supported := map[string]struct{}{"agent.heartbeat.v1": {}, "agent.rotation.v1": {}, "agent.os-permissions.v1": {}, protocol.CapabilityMetrics: {}, protocol.CapabilityDocker: {}, protocol.CapabilityTaskBridge: {}, protocol.CapabilityContainerStreams: {}, protocol.CapabilityCompose: {}, protocol.CapabilityImages: {}, protocol.CapabilityTerminal: {}, protocol.CapabilityFiles: {}, protocol.CapabilityComposeEditor: {}}
	result := make([]string, 0, len(reported))
	for _, capability := range reported {
		if _, ok := supported[capability]; ok {
			result = append(result, capability)
		}
	}
	return result
}

func hasCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func validRuntimePermissions(value protocol.RuntimePermissions) bool {
	if value.OS == "" || len(value.OS) > 32 || value.Architecture == "" || len(value.Architecture) > 32 || value.EffectiveUID < 0 || value.EffectiveGID < 0 || len(value.SupplementaryGroups) > 128 {
		return false
	}
	for _, group := range value.SupplementaryGroups {
		if group < 0 {
			return false
		}
	}
	return true
}

func protocolCloseStatus(err error) websocket.StatusCode {
	if status := websocket.CloseStatus(err); status != -1 {
		return status
	}
	return websocket.StatusPolicyViolation
}

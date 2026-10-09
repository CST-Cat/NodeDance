package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"
	"github.com/CST-Cat/NodeDance/internal/agent/containerstreams"
	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	agentfiles "github.com/CST-Cat/NodeDance/internal/agent/files"
	hostmetrics "github.com/CST-Cat/NodeDance/internal/agent/metrics"
	agentprobes "github.com/CST-Cat/NodeDance/internal/agent/probes"
	agentterminal "github.com/CST-Cat/NodeDance/internal/agent/terminal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
	mobyclient "github.com/moby/moby/client"
)

const (
	connectTimeout          = 10 * time.Second
	rotationAckTimeout      = 5 * time.Second
	heartbeatAckTimeout     = 15 * time.Second
	minimumReconnectWait    = time.Second
	maximumReconnectWait    = 30 * time.Second
	maximumReconnectAttempt = 5
)

var errReconnectAfterRotation = errors.New("credential rotation acknowledged")

type socketRead struct {
	typeID websocket.MessageType
	data   []byte
	err    error
}

// reconnectBackoff tracks failed connection attempts. A validated WELCOME
// starts a fresh retry sequence, even if the connection later drops.
type reconnectBackoff struct {
	attempt int
}

func (b *reconnectBackoff) connected() {
	b.attempt = 0
}

func (b *reconnectBackoff) failed() {
	if b.attempt < maximumReconnectAttempt {
		b.attempt++
	}
}

func (b reconnectBackoff) delay() time.Duration {
	return reconnectDelay(b.attempt)
}

func DefaultConfigPath() (string, error) {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve Agent config directory: %w", err)
	}
	return configHome + string(os.PathSeparator) + "nodedance-agent" + string(os.PathSeparator) + "agent.json", nil
}

// Run keeps an enrolled Agent connected with bounded exponential backoff and
// jitter. It never opens an inbound listener; all protocol traffic is outbound.
func Run(ctx context.Context, configPath, version string, stderr io.Writer) error {
	if configPath == "" {
		var err error
		configPath, err = DefaultConfigPath()
		if err != nil {
			return err
		}
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load Agent credentials: %w", err)
	}
	if config.AgentID == "" || config.NodeID == "" || config.EnrollmentToken != "" {
		return errors.New("Agent enrollment is incomplete; run enroll or recover before starting")
	}
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	collector := hostmetrics.NewCollector()
	collectorCtx, cancelCollector := context.WithCancel(ctx)
	metricUpdates := collector.Start(collectorCtx)
	defer func() {
		cancelCollector()
		<-collector.Done()
	}()
	sharedDocker, dockerErr := agentdocker.NewSDKEngine(os.Getenv("DOCKER_HOST"))
	if dockerErr == nil {
		defer sharedDocker.Close()
	} else if stderr != nil {
		fmt.Fprintln(stderr, "Docker Engine unavailable; host monitoring remains active")
	}
	taskBridge, bridgeErr := openTaskBridge(ctx, configPath, config.NodeID, sharedDocker)
	if bridgeErr != nil {
		if stderr != nil {
			fmt.Fprintln(stderr, "Agent task bridge unavailable; host monitoring remains active")
		}
	} else if taskBridge != nil {
		defer func() {
			if err := taskBridge.close(); err != nil && stderr != nil {
				fmt.Fprintln(stderr, "Agent task bridge shutdown did not fully complete")
			}
		}()
	}
	fileService, fileErr := openAgentFileService(configPath)
	if fileErr != nil && stderr != nil {
		fmt.Fprintln(stderr, "Agent file service unavailable; monitoring and task execution remain active")
	}
	if fileService != nil {
		defer fileService.Close()
	}
	var backoff reconnectBackoff
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		config, err = LoadConfig(configPath)
		if err != nil {
			return fmt.Errorf("reload Agent credentials: %w", err)
		}
		established := false
		connectionErr := runConnection(ctx, configPath, config, version, metricUpdates, taskBridge, sharedDocker, func() {
			established = true
		}, fileService)
		if established {
			backoff.connected()
		}
		if errors.Is(connectionErr, errReconnectAfterRotation) {
			backoff.connected()
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		delay := backoff.delay()
		if stderr != nil {
			fmt.Fprintf(stderr, "Agent connection unavailable; retrying in %s (%s)\n", delay, safeConnectionError(connectionErr))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
		backoff.failed()
	}
}

func runConnection(ctx context.Context, configPath string, config Config, version string, metricUpdates <-chan hostmetrics.Snapshot, taskBridge *taskBridgeRuntime, sharedDocker *agentdocker.SDKEngine, onEstablished func(), fileService *agentfiles.Service) error {
	credential := config.Credential
	pendingCredentialAlreadyActive := false
	if config.PendingCredential != "" {
		client, err := newHTTPClient(config.CAFile)
		if err == nil {
			identity, lookupErr := lookupIdentity(ctx, client, config, config.PendingCredential)
			client.CloseIdleConnections()
			if lookupErr == nil && identity.AgentID == config.AgentID && identity.NodeID == config.NodeID {
				credential = config.PendingCredential
				if identity.CredentialState == "active" {
					pendingCredentialAlreadyActive = true
				}
			}
		}
	}
	permissions, err := currentRuntimePermissions()
	if err != nil {
		return err
	}
	tlsConfig, err := newTLSConfig(config.CAFile)
	if err != nil {
		return err
	}
	websocketURL, err := agentWebSocketURL(config.Server)
	if err != nil {
		return err
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, connectTimeout)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig, Proxy: http.ProxyFromEnvironment}}
	conn, _, err := websocket.Dial(dialCtx, websocketURL, &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + credential}},
	})
	cancelDial()
	if err != nil {
		client.CloseIdleConnections()
		return errors.New("Core WebSocket connection failed")
	}
	defer client.CloseIdleConnections()
	conn.SetReadLimit(protocol.MaxMessageBytes)
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	defer cancelConnection()
	defer conn.CloseNow()
	var streamBridge *containerStreamBridge
	if sharedDocker != nil {
		streamEngine, streamErr := containerstreams.NewEngine(sharedDocker.Client())
		if streamErr == nil {
			streamBridge, _ = newContainerStreamBridge(streamEngine)
		}
	}
	if streamBridge != nil {
		defer streamBridge.Close()
	}

	composeBridge, composeAvailable := newSDKComposeBridge(sharedDocker)

	hello := protocol.Hello{
		AgentID: config.AgentID, NodeID: config.NodeID, AgentVersion: version,
		Capabilities: []string{"agent.heartbeat.v1", "agent.rotation.v1", "agent.os-permissions.v1", protocol.CapabilityMetrics, protocol.CapabilityDocker, protocol.CapabilityTerminal, protocol.CapabilityProbes},
		Permissions:  permissions,
	}
	if taskBridge != nil {
		hello.Capabilities = append(hello.Capabilities, protocol.CapabilityTaskBridge)
		if taskBridge.images != nil {
			hello.Capabilities = append(hello.Capabilities, protocol.CapabilityImages)
		}
	}
	if streamBridge != nil {
		hello.Capabilities = append(hello.Capabilities, protocol.CapabilityContainerStreams)
	}
	if composeAvailable {
		hello.Capabilities = append(hello.Capabilities, protocol.CapabilityCompose)
	}
	if fileService != nil {
		hello.Capabilities = append(hello.Capabilities, protocol.CapabilityFiles)
	}
	if err := writeSocketEnvelope(connectionCtx, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello, Payload: encodePayload(hello)}); err != nil {
		return errors.New("send Agent hello failed")
	}
	readCtx, cancelRead := context.WithTimeout(connectionCtx, agentHelloDeadline)
	messageType, raw, err := conn.Read(readCtx)
	cancelRead()
	if err != nil {
		return errors.New("Core welcome was not received")
	}
	if messageType != websocket.MessageText {
		return errors.New("Core returned a non-text welcome")
	}
	envelope, err := decodeSocketEnvelope(raw)
	if err != nil || envelope.Version != protocol.CurrentVersion || envelope.Type != protocol.TypeWelcome || envelope.Generation == 0 {
		return errors.New("Core welcome is invalid")
	}
	var welcome protocol.Welcome
	if err := decodeSocketPayload(envelope.Payload, &welcome); err != nil || !validWelcome(config, envelope.Generation, welcome) {
		return errors.New("Core welcome is invalid")
	}
	if onEstablished != nil {
		onEstablished()
	}
	if welcome.CredentialRotationDone || pendingCredentialAlreadyActive {
		if credential != config.PendingCredential || config.PendingCredential == "" || config.PendingRotationID == "" {
			return errors.New("Core confirmed an unexpected credential rotation")
		}
		config.Credential = config.PendingCredential
		config.PendingCredential = ""
		config.PendingRotationID = ""
		if err := SaveConfig(configPath, config, false); err != nil {
			return fmt.Errorf("persist committed Agent credential rotation: %w", err)
		}
	}

	reads := make(chan socketRead, 1)
	go socketReadLoop(connectionCtx, conn, reads)
	if welcome.RotationRequestedID != "" {
		if err := prepareRotation(connectionCtx, conn, reads, configPath, config, welcome.Generation, welcome.RotationRequestedID,
			func(ctx context.Context, envelope protocol.Envelope) error {
				return writeSocketEnvelope(ctx, conn, envelope)
			}); err != nil {
			return err
		}
		return errReconnectAfterRotation
	}
	if welcome.CredentialRotationDone {
		// The first connection authenticated the new verifier and committed it;
		// reconnect once more using the now-active local credential.
		return errReconnectAfterRotation
	}
	if !containsCapability(welcome.Capabilities, protocol.CapabilityMetrics) {
		metricUpdates = nil
	}
	if !containsCapability(welcome.Capabilities, protocol.CapabilityContainerStreams) && streamBridge != nil {
		_ = streamBridge.Close()
		streamBridge = nil
	}
	if !containsCapability(welcome.Capabilities, protocol.CapabilityFiles) {
		fileService = nil
	}
	return runHeartbeatLoop(connectionCtx, conn, reads, welcome.Generation, configPath, metricUpdates, sharedDocker,
		containsCapability(welcome.Capabilities, protocol.CapabilityDocker), taskBridge,
		containsCapability(welcome.Capabilities, protocol.CapabilityTaskBridge),
		containsCapability(welcome.Capabilities, protocol.CapabilityImages), streamBridge,
		containsCapability(welcome.Capabilities, protocol.CapabilityContainerStreams), composeBridge,
		containsCapability(welcome.Capabilities, protocol.CapabilityCompose),
		containsCapability(welcome.Capabilities, protocol.CapabilityTerminal), config.Shell, config.NodeID,
		containsCapability(welcome.Capabilities, protocol.CapabilityProbes), fileService)
}

const agentHelloDeadline = 5 * time.Second

func runHeartbeatLoop(ctx context.Context, conn *websocket.Conn, reads <-chan socketRead, generation uint64, configPath string, metricUpdates <-chan hostmetrics.Snapshot, sharedDocker *agentdocker.SDKEngine, dockerEnabled bool, taskBridge *taskBridgeRuntime, taskBridgeEnabled, imagesEnabled bool, streamBridge *containerStreamBridge, streamBridgeEnabled bool, composeBridge *agentcompose.Bridge, composeBridgeEnabled, terminalEnabled bool, hostShell, nodeID string, probesEnabled bool, fileService *agentfiles.Service) (returnErr error) {
	ticker := time.NewTicker(time.Duration(protocol.HeartbeatIntervalSeconds) * time.Second)
	defer ticker.Stop()
	ackTimer := time.NewTimer(heartbeatAckTimeout)
	defer ackTimer.Stop()
	writer := newSocketEnvelopeWriter(ctx, conn)
	defer func() {
		if !writer.closeAndWait() && returnErr == nil {
			returnErr = errors.New("Agent WebSocket writer did not stop")
		}
	}()
	var terminalFrames chan protocol.TerminalFrame
	if terminalEnabled {
		var dockerClient *mobyclient.Client
		if sharedDocker != nil {
			dockerClient = sharedDocker.Client()
		}
		manager := agentterminal.NewManager(agentterminal.NewSystemProvider(hostShell, dockerClient))
		terminalFrames = make(chan protocol.TerminalFrame, 64)
		terminalCtx, cancelTerminal := context.WithCancel(ctx)
		terminalDone := make(chan struct{})
		go func() {
			defer close(terminalDone)
			for {
				select {
				case <-terminalCtx.Done():
					manager.CloseAll()
					return
				case frame := <-terminalFrames:
					err := manager.Handle(terminalCtx, frame, func(out protocol.TerminalFrame) error {
						if err := protocol.ValidateTerminalFrame(out, true); err != nil {
							return err
						}
						payload, err := json.Marshal(out)
						if err != nil {
							return err
						}
						return writer.offerTerminal(terminalCtx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTerminalFrame, Generation: generation, Payload: payload})
					})
					if err != nil {
						response := protocol.TerminalFrame{StreamID: frame.StreamID, Action: protocol.TerminalActionError, Message: terminalSafeError(frame.Action, err)}
						payload, marshalErr := json.Marshal(response)
						if marshalErr == nil {
							_ = writer.offerTerminal(terminalCtx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTerminalFrame, Generation: generation, Payload: payload})
						}
					}
				}
			}
		}()
		defer func() {
			cancelTerminal()
			select {
			case <-terminalDone:
			case <-time.After(5 * time.Second):
				if returnErr == nil {
					returnErr = errors.New("Agent terminal sessions did not stop")
				}
			}
		}()
	}
	var taskMessages chan protocol.Envelope
	var taskBridgeDone <-chan error
	if taskBridge != nil && taskBridgeEnabled {
		taskMessages = make(chan protocol.Envelope, 64)
		taskCtx, cancelTask := context.WithCancel(ctx)
		done := make(chan error, 1)
		finished := make(chan struct{})
		taskBridgeDone = done
		go func() {
			defer close(finished)
			done <- runTaskBridgeSession(taskCtx, writer, taskBridge.runner, taskBridge.rebuild, generation, taskBridge.nodeID, taskBridge.journal.JournalID(), taskMessages)
		}()
		defer func() {
			cancelTask()
			timer := time.NewTimer(6 * time.Second)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				if returnErr == nil {
					returnErr = errors.New("Agent task bridge did not stop")
				}
			}
		}()
	}
	var streamMessages chan protocol.Envelope
	var streamBridgeDone <-chan error
	if streamBridge != nil && streamBridgeEnabled {
		streamMessages = make(chan protocol.Envelope, 16)
		streamCtx, cancelStream := context.WithCancel(ctx)
		done := make(chan error, 1)
		finished := make(chan struct{})
		streamBridgeDone = done
		go func() {
			defer close(finished)
			done <- streamBridge.run(streamCtx, writer, streamMessages, generation)
		}()
		defer func() {
			cancelStream()
			timer := time.NewTimer(6 * time.Second)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				if returnErr == nil {
					returnErr = errors.New("Agent container stream bridge did not stop")
				}
			}
		}()
	}
	var composeMessages chan protocol.Envelope
	var composeBridgeDone <-chan error
	if composeBridge != nil && composeBridgeEnabled {
		composeMessages = make(chan protocol.Envelope, 16)
		composeCtx, cancelCompose := context.WithCancel(ctx)
		done := make(chan error, 1)
		finished := make(chan struct{})
		composeBridgeDone = done
		go func() {
			defer close(finished)
			done <- composeBridge.Run(composeCtx, composeWriterAdapter{writer: writer}, composeMessages, generation)
		}()
		defer func() {
			cancelCompose()
			timer := time.NewTimer(6 * time.Second)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				if returnErr == nil {
					returnErr = errors.New("Agent Compose bridge did not stop")
				}
			}
		}()
	}
	var imageMessages chan protocol.Envelope
	var imageBridgeDone <-chan error
	if taskBridge != nil && taskBridge.images != nil && imagesEnabled {
		imageMessages = make(chan protocol.Envelope, 8)
		imageCtx, cancelImage := context.WithCancel(ctx)
		done := make(chan error, 1)
		finished := make(chan struct{})
		imageBridgeDone = done
		go func() {
			defer close(finished)
			done <- runAgentImageSession(imageCtx, writer, taskBridge.images, generation, imageMessages)
		}()
		defer func() {
			cancelImage()
			timer := time.NewTimer(6 * time.Second)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				if returnErr == nil {
					returnErr = errors.New("Agent image bridge did not stop")
				}
			}
		}()
	}
	var fileMessages chan protocol.Envelope
	var fileBridgeDone <-chan error
	if fileService != nil {
		fileMessages = make(chan protocol.Envelope, 16)
		fileCtx, cancelFile := context.WithCancel(ctx)
		done := make(chan error, 1)
		finished := make(chan struct{})
		fileBridgeDone = done
		bridge := newAgentFileBridge(fileService, generation, writer)
		go func() {
			defer close(finished)
			done <- bridge.run(fileCtx, fileMessages)
		}()
		defer func() {
			cancelFile()
			timer := time.NewTimer(6 * time.Second)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				if returnErr == nil {
					returnErr = errors.New("Agent file bridge did not stop")
				}
			}
		}()
	}
	var probeMessages chan protocol.Envelope
	var probeBridgeDone <-chan error
	if probesEnabled {
		probeMessages = make(chan protocol.Envelope, 64)
		probeCtx, cancelProbe := context.WithCancel(ctx)
		done := make(chan error, 1)
		finished := make(chan struct{})
		probeBridgeDone = done
		bridge := agentprobes.NewBridge(agentprobes.NewExecutor())
		go func() {
			defer close(finished)
			done <- bridge.Run(probeCtx, writer, generation, nodeID, probeMessages)
		}()
		defer func() {
			cancelProbe()
			timer := time.NewTimer(6 * time.Second)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				if returnErr == nil {
					returnErr = errors.New("Agent service probe bridge did not stop")
				}
			}
		}()
	}
	var dockerDone <-chan error
	if dockerEnabled {
		var engine agentdocker.Engine
		if sharedDocker == nil {
			engine = unavailableDockerEngine{err: errors.New("Docker Engine host must be a local unix socket")}
		} else {
			engine = sharedDocker
		}
		observer := &socketDockerObserver{writer: writer, generation: generation}
		discoverer, err := agentdocker.NewDiscoverer(engine, observer, agentdocker.Options{})
		if err == nil {
			dockerCtx, cancelDocker := context.WithCancel(ctx)
			dockerResult := make(chan error, 1)
			dockerFinished := make(chan struct{})
			dockerDone = dockerResult
			go func() {
				dockerResult <- discoverer.Run(dockerCtx)
				close(dockerFinished)
			}()
			// This defer is installed after the writer's join, so LIFO ordering
			// stops and joins Docker observation before the socket writer closes.
			defer func() {
				cancelDocker()
				select {
				case <-dockerFinished:
				case <-time.After(5 * time.Second):
					if returnErr == nil {
						returnErr = errors.New("Agent Docker observer did not stop")
					}
				}
			}()
		}
	}
	var sentSequence, acknowledgedSequence uint64
	var metricsSequence uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ackTimer.C:
			return errors.New("Core heartbeat acknowledgement timed out")
		case <-ticker.C:
			sentSequence++
			envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
				Generation: generation, Sequence: sentSequence, Payload: encodePayload(protocol.Heartbeat{})}
			if err := writer.send(ctx, envelope); err != nil {
				return errors.New("send Agent heartbeat failed")
			}
		case snapshot, ok := <-metricUpdates:
			if !ok {
				metricUpdates = nil
				continue
			}
			metricsSequence++
			wireMetrics := hostmetrics.ToProtocolMetrics(snapshot)
			payload, err := protocol.MarshalMetricsSnapshot(wireMetrics)
			if err != nil {
				// Invalid detail never blocks the independent heartbeat loop. The
				// converter bounds detail lists and reports any truncation.
				continue
			}
			writer.offerMetrics(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeMetrics,
				Generation: generation, Sequence: metricsSequence, Payload: payload})
		case err := <-writer.failures:
			if err != nil {
				return errors.New("Agent WebSocket writer failed")
			}
		case <-writer.streamFailures:
			if streamBridge != nil {
				// A single bounded notification may represent several failed
				// streams. Drain the durable pending set so a full notification
				// channel can never leave a browser stream hanging indefinitely.
				for requestID := range writer.takeStreamFailures() {
					go streamBridge.fail(ctx, writer, generation, requestID, "slow_consumer")
				}
			}
		case err := <-dockerDone:
			if err != nil {
				return errors.New("Agent Docker observer stopped unexpectedly")
			}
			dockerDone = nil
		case err := <-taskBridgeDone:
			if err != nil {
				return fmt.Errorf("Agent task bridge stopped unexpectedly: %w", err)
			}
			taskBridgeDone = nil
		case err := <-streamBridgeDone:
			if err != nil {
				return err
			}
			streamBridgeDone = nil
		case err := <-composeBridgeDone:
			if err != nil {
				return fmt.Errorf("Agent Compose bridge stopped unexpectedly: %w", err)
			}
			composeBridgeDone = nil
		case err := <-imageBridgeDone:
			if err != nil {
				return fmt.Errorf("Agent image bridge stopped unexpectedly: %w", err)
			}
			imageBridgeDone = nil
		case err := <-fileBridgeDone:
			if err != nil {
				return fmt.Errorf("Agent file bridge stopped unexpectedly: %w", err)
			}
			fileBridgeDone = nil
		case err := <-probeBridgeDone:
			if err != nil {
				return fmt.Errorf("Agent service probe bridge stopped unexpectedly: %w", err)
			}
			probeBridgeDone = nil
		case message := <-reads:
			if message.err != nil {
				return errors.New("Core Agent connection closed")
			}
			if message.typeID != websocket.MessageText {
				return errors.New("Core sent a non-text Agent frame")
			}
			envelope, err := decodeSocketEnvelope(message.data)
			if err != nil || envelope.Version != protocol.CurrentVersion || envelope.Generation != generation {
				return errors.New("Core Agent frame is invalid")
			}
			switch envelope.Type {
			case protocol.TypeHeartbeatAck:
				var ack protocol.HeartbeatAck
				if err := decodeSocketPayload(envelope.Payload, &ack); err != nil || envelope.Sequence <= acknowledgedSequence || envelope.Sequence > sentSequence {
					return errors.New("Core heartbeat acknowledgement is invalid")
				}
				acknowledgedSequence = envelope.Sequence
				resetTimer(ackTimer, heartbeatAckTimeout)
			case protocol.TypeRotateRequest:
				if !isUUID(envelope.RequestID) {
					return errors.New("Core credential rotation request is invalid")
				}
				currentConfig, err := LoadConfig(configPath)
				if err != nil {
					return fmt.Errorf("reload Agent credentials for rotation: %w", err)
				}
				if err := prepareRotation(ctx, conn, reads, configPath, currentConfig, generation, envelope.RequestID, writer.send); err != nil {
					return err
				}
				return errReconnectAfterRotation
			case protocol.TypeTaskJournalStatus, protocol.TypeTaskSnapshotRequest, protocol.TypeTaskDispatch,
				protocol.TypeTaskReconcile, protocol.TypeTaskReportAck, protocol.TypeTaskCancelRequest,
				protocol.TypeContainerRebuildPlanRequest:
				if taskMessages == nil {
					return errors.New("Core sent task work without a negotiated task bridge")
				}
				select {
				case taskMessages <- envelope:
				default:
					return errors.New("Agent task control queue is full")
				}
			case protocol.TypeContainerStreamOpen, protocol.TypeContainerStreamClose:
				if streamMessages == nil || envelope.Sequence != 0 || protocol.ValidateContainerStreamEnvelope(envelope, generation) != nil {
					return errors.New("Core container stream request is invalid or was not negotiated")
				}
				if envelope.Type == protocol.TypeContainerStreamClose {
					// Cancellation must not depend on spare command-queue capacity.
					streamBridge.stop(envelope.RequestID)
				}
				select {
				case streamMessages <- envelope:
				default:
					if envelope.Type == protocol.TypeContainerStreamOpen {
						// A local queue limit affects only this stream. Do not let a
						// burst of open requests tear down the Agent heartbeat socket.
						go streamBridge.reject(ctx, writer, generation, envelope.RequestID, "stream_limit")
					}
				}
			case protocol.TypeComposeRequest:
				if composeMessages == nil || envelope.Sequence != 0 || envelope.RequestID == "" || envelope.Generation != generation {
					return errors.New("Core Compose request is invalid or was not negotiated")
				}
				var request protocol.ComposeRequest
				if decodeSocketPayload(envelope.Payload, &request) != nil || request.OperationID != envelope.RequestID || protocol.ValidateComposeRequest(request) != nil {
					return errors.New("Core Compose request failed validation")
				}
				select {
				case composeMessages <- envelope:
				case <-ctx.Done():
					return nil
				default:
					response := protocol.ComposeResponse{OperationID: request.OperationID, Status: "failed", ErrorCode: "agent_busy"}
					if err := writer.send(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeComposeResponse,
						Generation: generation, RequestID: request.OperationID, Payload: encodePayload(response)}); err != nil {
						return err
					}
				}
			case protocol.TypeImageListRequest:
				var request protocol.ImageListRequest
				if imageMessages == nil || decodeSocketPayload(envelope.Payload, &request) != nil ||
					protocol.ValidateImageListRequest(envelope, request, generation) != nil {
					return errors.New("Core image list request is invalid or was not negotiated")
				}
				select {
				case imageMessages <- envelope:
				default:
					go func() {
						_ = sendAgentImageResponse(ctx, writer, generation, envelope.RequestID,
							protocol.ImageListResponse{Page: request.Page, ErrorCode: "engine_unavailable", Images: []protocol.ImageSummary{}})
					}()
				}
			case protocol.TypeTerminalFrame:
				if !terminalEnabled || terminalFrames == nil {
					return errors.New("Core requested terminal without negotiated capability")
				}
				var frame protocol.TerminalFrame
				if envelope.Sequence != 0 || decodeSocketPayload(envelope.Payload, &frame) != nil || protocol.ValidateTerminalFrame(frame, false) != nil {
					return errors.New("Core terminal frame is invalid")
				}
				select {
				case terminalFrames <- frame:
				case <-ctx.Done():
					return nil
				default:
					return errors.New("Core terminal command queue overflow")
				}
			case protocol.TypeFileRequest, protocol.TypeFileCancel:
				if fileMessages == nil || envelope.Sequence != 0 || len(envelope.Payload) == 0 || len(envelope.Payload) > protocol.MaxFileControlBytes {
					return errors.New("Core file request is invalid or was not negotiated")
				}
				select {
				case fileMessages <- envelope:
				default:
					return errors.New("Agent file control queue is full")
				}
			case protocol.TypeProbeDispatch:
				if probeMessages == nil || envelope.Sequence != 0 {
					return errors.New("Core sent service probe work without a negotiated probe capability")
				}
				select {
				case probeMessages <- envelope:
				default:
					return errors.New("Core service probe queue exceeded its bound")
				}
			case protocol.TypeProtocolError:
				return errors.New("Core rejected Agent protocol message")
			default:
				return errors.New("Core sent an unsupported Agent frame")
			}
		}
	}
}

func terminalSafeError(action string, err error) string {
	if errors.Is(err, agentterminal.ErrSessionLimit) {
		return "node terminal session limit reached"
	}
	if action == protocol.TerminalActionOpen {
		return "could not open terminal for this target"
	}
	return "terminal operation failed"
}

func prepareRotation(ctx context.Context, conn *websocket.Conn, reads <-chan socketRead, configPath string, config Config, generation uint64, rotationID string,
	send func(context.Context, protocol.Envelope) error) error {
	if !isUUID(rotationID) {
		return errors.New("Core credential rotation ID is invalid")
	}
	if config.PendingCredential != "" {
		if config.PendingRotationID != rotationID {
			return errors.New("saved pending credential belongs to a different rotation request")
		}
	} else {
		credential, err := NewCredential()
		if err != nil {
			return fmt.Errorf("generate pending Agent credential: %w", err)
		}
		config.PendingCredential = credential
		config.PendingRotationID = rotationID
		if err := SaveConfig(configPath, config, false); err != nil {
			return fmt.Errorf("persist pending Agent credential before sending it: %w", err)
		}
	}
	rotation := protocol.RotatePrepare{RotationID: rotationID, NewCredential: config.PendingCredential}
	if err := send(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeRotatePrepare,
		Generation: generation, RequestID: rotationID, Payload: encodePayload(rotation)}); err != nil {
		return errors.New("send credential rotation prepare failed")
	}
	timer := time.NewTimer(rotationAckTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return errors.New("Core did not acknowledge pending Agent credential")
		case message := <-reads:
			if message.err != nil {
				return errors.New("Core connection closed before credential acknowledgement")
			}
			if message.typeID != websocket.MessageText {
				return errors.New("Core sent a non-text rotation acknowledgement")
			}
			envelope, err := decodeSocketEnvelope(message.data)
			if err != nil || envelope.Version != protocol.CurrentVersion || envelope.Generation != generation {
				return errors.New("Core rotation acknowledgement is invalid")
			}
			if envelope.Type == protocol.TypeProtocolError {
				return errors.New("Core rejected credential rotation")
			}
			if envelope.Type != protocol.TypeRotateAccepted || envelope.RequestID != rotationID {
				continue
			}
			var accepted protocol.RotateAccepted
			if err := decodeSocketPayload(envelope.Payload, &accepted); err != nil || accepted.RotationID != rotationID {
				return errors.New("Core rotation acknowledgement is invalid")
			}
			return nil
		}
	}
}

func socketReadLoop(ctx context.Context, conn *websocket.Conn, messages chan<- socketRead) {
	for {
		messageType, data, err := conn.Read(ctx)
		select {
		case messages <- socketRead{typeID: messageType, data: data, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func writeSocketEnvelope(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope) error {
	return writeSocketEnvelopeWithTimeout(ctx, conn, envelope, 5*time.Second)
}

func writeSocketEnvelopeWithTimeout(ctx context.Context, conn *websocket.Conn, envelope protocol.Envelope, timeout time.Duration) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}

func containsCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func decodeSocketEnvelope(raw []byte) (protocol.Envelope, error) {
	var envelope protocol.Envelope
	if len(raw) == 0 || len(raw) > protocol.MaxMessageBytes {
		return envelope, errors.New("Agent protocol frame size is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return envelope, errors.New("Agent protocol frame has trailing data")
	}
	return envelope, nil
}

func decodeSocketPayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return errors.New("Agent protocol payload size is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("Agent protocol payload has trailing data")
	}
	return nil
}

func encodePayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func validWelcome(config Config, generation uint64, welcome protocol.Welcome) bool {
	if welcome.AgentID != config.AgentID || welcome.NodeID != config.NodeID || welcome.Generation != generation ||
		welcome.HeartbeatIntervalSecs != protocol.HeartbeatIntervalSeconds || welcome.OfflineAfterSecs != protocol.OfflineAfterSeconds || len(welcome.Capabilities) > 32 {
		return false
	}
	if welcome.RotationRequestedID != "" && !isUUID(welcome.RotationRequestedID) {
		return false
	}
	seen := map[string]struct{}{}
	for _, capability := range welcome.Capabilities {
		if _, duplicate := seen[capability]; duplicate || len(capability) == 0 || len(capability) > 80 {
			return false
		}
		seen[capability] = struct{}{}
	}
	return true
}

func currentRuntimePermissions() (protocol.RuntimePermissions, error) {
	groups, err := os.Getgroups()
	if err != nil {
		return protocol.RuntimePermissions{}, fmt.Errorf("read supplementary groups: %w", err)
	}
	if len(groups) > 128 {
		return protocol.RuntimePermissions{}, errors.New("Agent supplementary group list exceeds protocol limit")
	}
	return protocol.RuntimePermissions{OS: runtime.GOOS, Architecture: runtime.GOARCH, EffectiveUID: os.Geteuid(), EffectiveGID: os.Getegid(), SupplementaryGroups: groups}, nil
}

func agentWebSocketURL(server string) (string, error) {
	parsed, err := url.Parse(server)
	if err != nil {
		return "", errors.New("Agent server URL is invalid")
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", errors.New("Agent server URL scheme is unsupported")
	}
	parsed.Path = "/ws/v1/agent"
	return parsed.String(), nil
}

func reconnectDelay(attempt int) time.Duration {
	minimum, maximum := reconnectDelayRange(attempt)
	width := maximum - minimum
	if width <= 0 {
		return minimum
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(width)+1))
	if err != nil {
		return minimum + width/2
	}
	return minimum + time.Duration(value.Int64())
}

func reconnectDelayRange(attempt int) (time.Duration, time.Duration) {
	base := minimumReconnectWait
	for index := 0; index < attempt && base < maximumReconnectWait; index++ {
		base *= 2
		if base > maximumReconnectWait {
			base = maximumReconnectWait
		}
	}
	spread := int64(base / 5)
	minimum := base - time.Duration(spread)
	if minimum < minimumReconnectWait {
		minimum = minimumReconnectWait
	}
	maximum := base + time.Duration(spread)
	if maximum > maximumReconnectWait {
		maximum = maximumReconnectWait
	}
	return minimum, maximum
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func safeConnectionError(err error) string {
	if err == nil {
		return "connection closed"
	}
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(message) > 180 {
		message = message[:180]
	}
	return message
}

func openAgentFileService(configPath string) (*agentfiles.Service, error) {
	root := os.Getenv("NODEDANCE_AGENT_FILE_ROOT")
	if root == "" {
		return nil, nil
	}
	stateDir, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return nil, fmt.Errorf("resolve Agent state directory for file access: %w", err)
	}
	root, err = ValidateFileRoot(root, stateDir)
	if err != nil {
		return nil, err
	}
	limit := protocol.DefaultFileLimit
	if configured := strings.TrimSpace(os.Getenv("NODEDANCE_AGENT_MAX_FILE_BYTES")); configured != "" {
		parsed, err := strconv.ParseInt(configured, 10, 64)
		if err != nil || parsed < 1 || parsed > protocol.MaxFileSize {
			return nil, errors.New("Agent file transfer limit is invalid")
		}
		limit = parsed
	}
	return agentfiles.New(root, limit)
}

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
	"runtime"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const (
	connectTimeout       = 10 * time.Second
	rotationAckTimeout   = 5 * time.Second
	heartbeatAckTimeout  = 15 * time.Second
	minimumReconnectWait = time.Second
	maximumReconnectWait = 30 * time.Second
)

var errReconnectAfterRotation = errors.New("credential rotation acknowledged")

type socketRead struct {
	typeID websocket.MessageType
	data   []byte
	err    error
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
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil
		}
		config, err = LoadConfig(configPath)
		if err != nil {
			return fmt.Errorf("reload Agent credentials: %w", err)
		}
		connectionErr := runConnection(ctx, configPath, config, version)
		if errors.Is(connectionErr, errReconnectAfterRotation) {
			attempt = -1
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		delay := reconnectDelay(attempt)
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
	}
}

func runConnection(ctx context.Context, configPath string, config Config, version string) error {
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

	hello := protocol.Hello{
		AgentID: config.AgentID, NodeID: config.NodeID, AgentVersion: version,
		Capabilities: []string{"agent.heartbeat.v1", "agent.rotation.v1", "agent.os-permissions.v1"},
		Permissions:  permissions,
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
		if err := prepareRotation(connectionCtx, conn, reads, configPath, config, welcome.Generation, welcome.RotationRequestedID); err != nil {
			return err
		}
		return errReconnectAfterRotation
	}
	if welcome.CredentialRotationDone {
		// The first connection authenticated the new verifier and committed it;
		// reconnect once more using the now-active local credential.
		return errReconnectAfterRotation
	}
	return runHeartbeatLoop(connectionCtx, conn, reads, welcome.Generation, configPath)
}

const agentHelloDeadline = 5 * time.Second

func runHeartbeatLoop(ctx context.Context, conn *websocket.Conn, reads <-chan socketRead, generation uint64, configPath string) error {
	ticker := time.NewTicker(time.Duration(protocol.HeartbeatIntervalSeconds) * time.Second)
	defer ticker.Stop()
	ackTimer := time.NewTimer(heartbeatAckTimeout)
	defer ackTimer.Stop()
	var sentSequence, acknowledgedSequence uint64
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
			if err := writeSocketEnvelope(ctx, conn, envelope); err != nil {
				return errors.New("send Agent heartbeat failed")
			}
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
				if err := prepareRotation(ctx, conn, reads, configPath, currentConfig, generation, envelope.RequestID); err != nil {
					return err
				}
				return errReconnectAfterRotation
			case protocol.TypeProtocolError:
				return errors.New("Core rejected Agent protocol message")
			default:
				return errors.New("Core sent an unsupported Agent frame")
			}
		}
	}
}

func prepareRotation(ctx context.Context, conn *websocket.Conn, reads <-chan socketRead, configPath string, config Config, generation uint64, rotationID string) error {
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
	if err := writeSocketEnvelope(ctx, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeRotatePrepare,
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
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
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
	base := minimumReconnectWait
	for index := 0; index < attempt && base < maximumReconnectWait; index++ {
		base *= 2
		if base > maximumReconnectWait {
			base = maximumReconnectWait
		}
	}
	spread := int64(base / 5)
	if spread <= 0 {
		return base
	}
	value, err := rand.Int(rand.Reader, big.NewInt(spread*2+1))
	if err != nil {
		return base
	}
	return base - time.Duration(spread) + time.Duration(value.Int64())
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

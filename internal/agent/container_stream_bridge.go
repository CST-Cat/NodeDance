package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/containerstreams"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const maxAgentContainerStreams = 16

type streamEnvelopeWriter interface {
	offerStream(context.Context, protocol.Envelope) error
	sendStreamTerminal(context.Context, protocol.Envelope) error
}

type agentContainerStream struct {
	id          string
	kind        string
	containerID string
	cancel      context.CancelFunc
	done        chan struct{}
	sequenceMu  sync.Mutex
	emitMu      sync.Mutex
	sequence    uint64
}

func (s *agentContainerStream) nextSequence() uint64 {
	s.sequenceMu.Lock()
	defer s.sequenceMu.Unlock()
	s.sequence++
	return s.sequence
}

// containerStreamBridge owns all read-only Engine streams for one Agent
// connection. It uses the shared containerstreams readers, and every stream
// is cancelled and joined when Core disconnects or closes its request.
type containerStreamBridge struct {
	engine  containerstreams.Engine
	stats   *containerstreams.StatsManager
	mu      sync.Mutex
	streams map[string]*agentContainerStream
	closed  bool
	wg      sync.WaitGroup
}

func newContainerStreamBridge(engine containerstreams.Engine) (*containerStreamBridge, error) {
	manager, err := containerstreams.NewStatsManager(engine, containerstreams.StatsManagerOptions{})
	if err != nil {
		return nil, err
	}
	return &containerStreamBridge{engine: engine, stats: manager, streams: make(map[string]*agentContainerStream)}, nil
}

func (b *containerStreamBridge) run(ctx context.Context, writer streamEnvelopeWriter, commands <-chan protocol.Envelope, generation uint64) error {
	defer b.closeAll()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			b.sendHeartbeats(ctx, writer, generation)
		case envelope, ok := <-commands:
			if !ok {
				return errors.New("container stream command channel closed")
			}
			if err := protocol.ValidateContainerStreamEnvelope(envelope, generation); err != nil {
				return errors.New("Core sent an invalid container stream command")
			}
			switch envelope.Type {
			case protocol.TypeContainerStreamOpen:
				var request protocol.ContainerStreamOpen
				if err := decodeSocketPayload(envelope.Payload, &request); err != nil {
					return errors.New("Core container stream request could not be decoded")
				}
				if err := b.start(ctx, writer, envelope, request, generation); err != nil {
					if errors.Is(err, errContainerStreamLimit) {
						_ = b.reject(ctx, writer, generation, envelope.RequestID, "stream_limit")
						continue
					}
					if errors.Is(err, errContainerStreamDuplicate) {
						return errors.New("Core reused an active container stream request ID")
					}
					return errors.New("Core container stream request was rejected")
				}
			case protocol.TypeContainerStreamClose:
				b.stop(envelope.RequestID)
			default:
				return errors.New("Core sent a non-control container stream message")
			}
		}
	}
}

var (
	errContainerStreamLimit     = errors.New("Agent container stream limit reached")
	errContainerStreamDuplicate = errors.New("Agent container stream request ID is already active")
)

func (b *containerStreamBridge) start(parent context.Context, writer streamEnvelopeWriter, envelope protocol.Envelope, request protocol.ContainerStreamOpen, generation uint64) error {
	if err := protocol.ValidateContainerStreamOpen(request); err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("Agent container stream bridge is closed")
	}
	if _, exists := b.streams[envelope.RequestID]; exists {
		b.mu.Unlock()
		return errContainerStreamDuplicate
	}
	if len(b.streams) >= maxAgentContainerStreams {
		b.mu.Unlock()
		return errContainerStreamLimit
	}
	ctx, cancel := context.WithCancel(parent)
	stream := &agentContainerStream{id: envelope.RequestID, kind: request.Kind, containerID: request.ContainerID, cancel: cancel, done: make(chan struct{})}
	b.streams[stream.id] = stream
	b.wg.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.wg.Done()
		defer close(stream.done)
		defer b.remove(stream)
		if request.Kind == protocol.StreamLogs {
			b.runLogs(ctx, writer, stream, request, generation)
			return
		}
		b.runStats(ctx, writer, stream, generation)
	}()
	return nil
}

func (b *containerStreamBridge) runLogs(ctx context.Context, writer streamEnvelopeWriter, stream *agentContainerStream, request protocol.ContainerStreamOpen, generation uint64) {
	reader, err := containerstreams.OpenLogs(ctx, b.engine, request.ContainerID, containerstreams.LogsOptions{
		Tail: request.Tail, Follow: request.Follow, Timestamps: request.Timestamps,
		ShowStdout: request.ShowStdout, ShowStderr: request.ShowStderr,
	})
	if err != nil {
		b.writeStreamError(ctx, writer, generation, stream, "engine_error")
		return
	}
	defer reader.Close()
	if b.writeStreamFrame(ctx, writer, generation, stream, protocol.TypeContainerStreamReady,
		protocol.ContainerStreamReady{Kind: stream.kind, ContainerID: stream.containerID}) != nil {
		return
	}
	errorsIn := reader.Errors
	frames := reader.Frames
	for frames != nil {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errorsIn:
			if !ok {
				errorsIn = nil
				continue
			}
			if err != nil && ctx.Err() == nil {
				b.writeStreamError(ctx, writer, generation, stream, "engine_error")
			}
			return
		case frame, ok := <-frames:
			if !ok {
				if ctx.Err() == nil {
					_ = b.writeStreamTerminal(ctx, writer, generation, stream, protocol.TypeContainerStreamEnd, protocol.ContainerStreamClose{})
				}
				return
			}
			payload := protocol.ContainerStreamLog{ContainerID: stream.containerID, Channel: string(frame.Channel), Data: frame.Data}
			if b.writeStreamFrame(ctx, writer, generation, stream, protocol.TypeContainerLog, payload) != nil {
				b.writeStreamError(ctx, writer, generation, stream, "slow_consumer")
				return
			}
		}
	}
}

func (b *containerStreamBridge) runStats(ctx context.Context, writer streamEnvelopeWriter, stream *agentContainerStream, generation uint64) {
	subscription, err := b.stats.Subscribe(ctx, stream.containerID)
	if err != nil {
		b.writeStreamError(ctx, writer, generation, stream, "engine_error")
		return
	}
	defer subscription.Close()
	if b.writeStreamFrame(ctx, writer, generation, stream, protocol.TypeContainerStreamReady,
		protocol.ContainerStreamReady{Kind: stream.kind, ContainerID: stream.containerID}) != nil {
		return
	}
	errorsIn := subscription.Errors
	updates := subscription.Updates
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errorsIn:
			if !ok {
				errorsIn = nil
				continue
			}
			if err != nil && ctx.Err() == nil {
				b.writeStreamError(ctx, writer, generation, stream, "engine_error")
			}
			return
		case snapshot, ok := <-updates:
			if !ok {
				if ctx.Err() == nil {
					b.writeStreamError(ctx, writer, generation, stream, "engine_error")
				}
				return
			}
			payload, err := encodeStreamStats(snapshot)
			if err != nil || b.writeStreamFrame(ctx, writer, generation, stream, protocol.TypeContainerStats, payload) != nil {
				b.writeStreamError(ctx, writer, generation, stream, "slow_consumer")
				return
			}
		}
	}
}

func encodeStreamStats(snapshot containerstreams.StatsSnapshot) (protocol.ContainerStreamStats, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return protocol.ContainerStreamStats{}, err
	}
	var wireSnapshot protocol.ContainerStatsSnapshot
	if err := json.Unmarshal(encoded, &wireSnapshot); err != nil {
		return protocol.ContainerStreamStats{}, err
	}
	return protocol.ContainerStreamStats{Snapshot: wireSnapshot}, nil
}

func (b *containerStreamBridge) writeStreamFrame(ctx context.Context, writer streamEnvelopeWriter, generation uint64, stream *agentContainerStream, kind string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(encoded) > protocol.MaxContainerStreamPayloadBytes {
		return errors.New("container stream frame exceeds protocol limit")
	}
	stream.emitMu.Lock()
	defer stream.emitMu.Unlock()
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: kind, Generation: generation, Sequence: stream.nextSequence(), RequestID: stream.id, Payload: encoded}
	if err := protocol.ValidateContainerStreamEnvelope(envelope, generation); err != nil {
		return err
	}
	return writer.offerStream(ctx, envelope)
}

func (b *containerStreamBridge) writeStreamError(ctx context.Context, writer streamEnvelopeWriter, generation uint64, stream *agentContainerStream, code string) {
	if ctx.Err() != nil {
		return
	}
	_ = b.writeStreamTerminal(ctx, writer, generation, stream, protocol.TypeContainerStreamError, protocol.ContainerStreamError{Code: code})
}

func (b *containerStreamBridge) reject(ctx context.Context, writer streamEnvelopeWriter, generation uint64, requestID, code string) error {
	encoded, err := json.Marshal(protocol.ContainerStreamError{Code: code})
	if err != nil {
		return err
	}
	return writer.sendStreamTerminal(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerStreamError,
		Generation: generation, Sequence: 1, RequestID: requestID, Payload: encoded})
}

func (b *containerStreamBridge) writeStreamTerminal(ctx context.Context, writer streamEnvelopeWriter, generation uint64, stream *agentContainerStream, kind string, payload any) error {
	stream.emitMu.Lock()
	defer stream.emitMu.Unlock()
	return b.writeTerminal(ctx, writer, generation, stream.id, stream.nextSequence(), kind, payload)
}

func (b *containerStreamBridge) writeTerminal(ctx context.Context, writer streamEnvelopeWriter, generation uint64, requestID string, sequence uint64, kind string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: kind, Generation: generation, Sequence: sequence, RequestID: requestID, Payload: encoded}
	if err := protocol.ValidateContainerStreamEnvelope(envelope, generation); err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return writer.sendStreamTerminal(writeCtx, envelope)
}

func (b *containerStreamBridge) fail(ctx context.Context, writer streamEnvelopeWriter, generation uint64, requestID, code string) {
	b.mu.Lock()
	stream := b.streams[requestID]
	b.mu.Unlock()
	if stream == nil {
		return
	}
	stream.cancel()
	b.writeStreamError(ctx, writer, generation, stream, code)
}

func (b *containerStreamBridge) stop(requestID string) {
	b.mu.Lock()
	stream := b.streams[requestID]
	b.mu.Unlock()
	if stream != nil {
		stream.cancel()
	}
}

func (b *containerStreamBridge) remove(stream *agentContainerStream) {
	stream.cancel()
	b.mu.Lock()
	if b.streams[stream.id] == stream {
		delete(b.streams, stream.id)
	}
	b.mu.Unlock()
}

func (b *containerStreamBridge) closeAll() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	for _, stream := range b.streams {
		stream.cancel()
	}
	b.mu.Unlock()
	b.wg.Wait()
	_ = b.stats.Close()
}

func (b *containerStreamBridge) sendHeartbeats(ctx context.Context, writer streamEnvelopeWriter, generation uint64) {
	b.mu.Lock()
	streams := make([]*agentContainerStream, 0, len(b.streams))
	for _, stream := range b.streams {
		streams = append(streams, stream)
	}
	b.mu.Unlock()
	for _, stream := range streams {
		stream.emitMu.Lock()
		payload := json.RawMessage(`{}`)
		envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerStreamHeartbeat,
			Generation: generation, Sequence: stream.nextSequence(), RequestID: stream.id, Payload: payload}
		if err := protocol.ValidateContainerStreamEnvelope(envelope, generation); err != nil || writer.offerStream(ctx, envelope) != nil {
			stream.cancel()
			go b.writeStreamError(ctx, writer, generation, stream, "slow_consumer")
		}
		stream.emitMu.Unlock()
	}
}

func (b *containerStreamBridge) Close() error {
	b.closeAll()
	return nil
}

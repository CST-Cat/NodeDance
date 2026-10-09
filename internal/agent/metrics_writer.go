package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const (
	controlWriteTimeout = 5 * time.Second
	metricWriteTimeout  = time.Second
	dockerWriteTimeout  = 2 * time.Second
	streamWriteTimeout  = time.Second
	maxDockerWriteBurst = 8
	maxStreamWriteBurst = 4
)

var (
	errDockerWriterQueueFull   = errors.New("Agent Docker frame queue is full")
	errStreamWriterQueueFull   = errors.New("Agent container stream queue is full")
	errStreamWriterUnavailable = errors.New("Agent container stream writer is unavailable")
)

type envelopeWrite struct {
	envelope protocol.Envelope
	result   chan error
	stream   bool
}

// socketEnvelopeWriter serializes all post-handshake writes. Control messages
// have a reserved queue and are selected ahead of coalesced metric snapshots.
// A metrics write has a short deadline so a slow peer cannot hold heartbeat
// traffic behind an obsolete telemetry frame.
type socketEnvelopeWriter struct {
	conn                  *websocket.Conn
	ctx                   context.Context
	cancel                context.CancelFunc
	high                  chan envelopeWrite
	docker                chan protocol.Envelope
	streams               chan protocol.Envelope
	metrics               chan protocol.Envelope
	streamFailures        chan struct{}
	streamFailureMu       sync.Mutex
	failedStreams         map[string]error
	pendingStreamFailures map[string]error
	failedStreamOrder     []string
	failures              chan error
	done                  chan struct{}
}

func newSocketEnvelopeWriter(ctx context.Context, conn *websocket.Conn) *socketEnvelopeWriter {
	writerCtx, cancel := context.WithCancel(ctx)
	w := &socketEnvelopeWriter{
		conn: conn, ctx: writerCtx, cancel: cancel,
		high: make(chan envelopeWrite, 8), metrics: make(chan protocol.Envelope, 1),
		docker: make(chan protocol.Envelope, 16), streams: make(chan protocol.Envelope, 32),
		streamFailures: make(chan struct{}, 1), failedStreams: make(map[string]error),
		pendingStreamFailures: make(map[string]error),
		failures:              make(chan error, 1), done: make(chan struct{}),
	}
	go w.run()
	return w
}

// offerStream keeps log and on-demand stats data in a separate low-priority,
// strictly bounded queue. A slow peer fails only the requested stream.
func (w *socketEnvelopeWriter) offerStream(ctx context.Context, envelope protocol.Envelope) error {
	if w == nil || w.streams == nil {
		return errStreamWriterUnavailable
	}
	w.streamFailureMu.Lock()
	_, failed := w.failedStreams[envelope.RequestID]
	w.streamFailureMu.Unlock()
	if failed {
		return errStreamWriterUnavailable
	}
	select {
	case w.streams <- envelope:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return errStreamWriterUnavailable
	default:
		return errStreamWriterQueueFull
	}
}

func (w *socketEnvelopeWriter) sendStreamTerminal(ctx context.Context, envelope protocol.Envelope) error {
	request := envelopeWrite{envelope: envelope, result: make(chan error, 1), stream: true}
	select {
	case w.high <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return errStreamWriterUnavailable
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return errStreamWriterUnavailable
	}
}

func (w *socketEnvelopeWriter) takeStreamFailures() map[string]error {
	w.streamFailureMu.Lock()
	defer w.streamFailureMu.Unlock()
	failed := w.pendingStreamFailures
	w.pendingStreamFailures = make(map[string]error)
	return failed
}

func (w *socketEnvelopeWriter) clearStreamFailure(requestID string) {
	w.streamFailureMu.Lock()
	delete(w.failedStreams, requestID)
	w.streamFailureMu.Unlock()
}

func (w *socketEnvelopeWriter) markStreamFailure(requestID string, err error) {
	if requestID == "" {
		return
	}
	w.streamFailureMu.Lock()
	if _, exists := w.failedStreams[requestID]; exists {
		w.streamFailureMu.Unlock()
		return
	}
	w.failedStreams[requestID] = err
	w.pendingStreamFailures[requestID] = err
	w.failedStreamOrder = append(w.failedStreamOrder, requestID)
	if len(w.failedStreamOrder) > 128 {
		oldest := w.failedStreamOrder[0]
		w.failedStreamOrder = w.failedStreamOrder[1:]
		delete(w.failedStreams, oldest)
	}
	w.streamFailureMu.Unlock()
	select {
	case w.streamFailures <- struct{}{}:
	default:
	}
}

// offerDocker preserves FIFO ordering and never overwrites a snapshot chunk.
// If the bounded queue fills, the observer treats this batch as lost and asks
// the discoverer for a new authoritative full snapshot.
func (w *socketEnvelopeWriter) offerDocker(ctx context.Context, envelope protocol.Envelope) error {
	select {
	case w.docker <- envelope:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return errors.New("Agent socket writer stopped")
	default:
		return errDockerWriterQueueFull
	}
}

// closeAndWait cancels queued work and joins the single socket writer before
// the Agent can reconnect and create another writer for the same process.
func (w *socketEnvelopeWriter) closeAndWait() bool {
	w.cancel()
	timer := time.NewTimer(controlWriteTimeout + time.Second)
	defer timer.Stop()
	select {
	case <-w.done:
		return true
	case <-timer.C:
		return false
	}
}

func (w *socketEnvelopeWriter) send(ctx context.Context, envelope protocol.Envelope) error {
	request := envelopeWrite{envelope: envelope, result: make(chan error, 1)}
	select {
	case w.high <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return errors.New("Agent socket writer stopped")
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return errors.New("Agent socket writer stopped")
	}
}

// Send exposes the serialized control lane to internal feature bridges without
// giving them direct access to the WebSocket connection.
func (w *socketEnvelopeWriter) Send(ctx context.Context, envelope protocol.Envelope) error {
	return w.send(ctx, envelope)
}

// offerMetrics keeps at most the latest not-yet-written snapshot. Losing an
// intermediate telemetry sample is preferable to delaying heartbeat/control.
func (w *socketEnvelopeWriter) offerMetrics(envelope protocol.Envelope) {
	select {
	case w.metrics <- envelope:
		return
	default:
	}
	select {
	case <-w.metrics:
	default:
	}
	select {
	case w.metrics <- envelope:
	case <-w.ctx.Done():
	default:
	}
}

func (w *socketEnvelopeWriter) run() {
	defer close(w.done)
	dockerBurst := 0
	streamBurst := 0
	for {
		select {
		case <-w.ctx.Done():
			return
		default:
		}
		// Check control work before waiting on either queue, which avoids
		// selecting a queued metrics frame while a heartbeat is already ready.
		select {
		case request := <-w.high:
			w.write(request.envelope, request.result, controlWriteTimeout, request.stream)
			continue
		default:
		}
		// Snapshot/event frames remain FIFO, but an always-nonempty Docker
		// queue must not starve the latest coalesced host metrics sample.
		if dockerBurst >= maxDockerWriteBurst {
			select {
			case envelope := <-w.metrics:
				w.write(envelope, nil, metricWriteTimeout, false)
				dockerBurst = 0
				continue
			default:
			}
		}
		if streamBurst >= maxStreamWriteBurst {
			select {
			case envelope := <-w.metrics:
				w.write(envelope, nil, metricWriteTimeout, false)
				streamBurst = 0
				dockerBurst = 0
				continue
			default:
			}
			select {
			case envelope := <-w.docker:
				w.write(envelope, nil, dockerWriteTimeout, false)
				dockerBurst++
				streamBurst = 0
				continue
			default:
			}
			streamBurst = 0
		}
		select {
		case envelope := <-w.docker:
			w.write(envelope, nil, dockerWriteTimeout, false)
			dockerBurst++
			streamBurst = 0
			continue
		default:
		}
		select {
		case <-w.ctx.Done():
			return
		case request := <-w.high:
			w.write(request.envelope, request.result, controlWriteTimeout, request.stream)
		case envelope := <-w.docker:
			w.write(envelope, nil, dockerWriteTimeout, false)
			dockerBurst++
			streamBurst = 0
		case envelope := <-w.streams:
			w.writeStream(envelope)
			streamBurst++
		case envelope := <-w.metrics:
			w.write(envelope, nil, metricWriteTimeout, false)
			dockerBurst = 0
			streamBurst = 0
		}
	}
}

func (w *socketEnvelopeWriter) writeStream(envelope protocol.Envelope) {
	w.streamFailureMu.Lock()
	_, alreadyFailed := w.failedStreams[envelope.RequestID]
	w.streamFailureMu.Unlock()
	if alreadyFailed {
		return
	}
	err := writeSocketEnvelopeWithTimeout(w.ctx, w.conn, envelope, streamWriteTimeout)
	if err == nil {
		return
	}
	w.markStreamFailure(envelope.RequestID, err)
}

func (w *socketEnvelopeWriter) write(envelope protocol.Envelope, result chan error, timeout time.Duration, stream bool) {
	err := writeSocketEnvelopeWithTimeout(w.ctx, w.conn, envelope, timeout)
	if result != nil {
		result <- err
	}
	if err != nil {
		if stream {
			w.markStreamFailure(envelope.RequestID, err)
		} else {
			select {
			case w.failures <- err:
			default:
			}
		}
	}
}

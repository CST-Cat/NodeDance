package agent

import (
	"context"
	"errors"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const (
	controlWriteTimeout   = 5 * time.Second
	metricWriteTimeout    = time.Second
	dockerWriteTimeout    = 2 * time.Second
	terminalWriteTimeout  = 2 * time.Second
	maxDockerWriteBurst   = 8
	maxTerminalWriteBurst = 8
)

var errDockerWriterQueueFull = errors.New("Agent Docker frame queue is full")
var errTerminalWriterQueueFull = errors.New("Agent terminal output queue is full")

type envelopeWrite struct {
	envelope protocol.Envelope
	result   chan error
}

// socketEnvelopeWriter serializes all post-handshake writes. Control messages
// have a reserved queue and are selected ahead of coalesced metric snapshots.
// A metrics write has a short deadline so a slow peer cannot hold heartbeat
// traffic behind an obsolete telemetry frame.
type socketEnvelopeWriter struct {
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	high     chan envelopeWrite
	docker   chan protocol.Envelope
	terminal chan protocol.Envelope
	metrics  chan protocol.Envelope
	failures chan error
	done     chan struct{}
}

func newSocketEnvelopeWriter(ctx context.Context, conn *websocket.Conn) *socketEnvelopeWriter {
	writerCtx, cancel := context.WithCancel(ctx)
	w := &socketEnvelopeWriter{
		conn: conn, ctx: writerCtx, cancel: cancel,
		high: make(chan envelopeWrite, 8), metrics: make(chan protocol.Envelope, 1),
		docker: make(chan protocol.Envelope, 16), terminal: make(chan protocol.Envelope, 16),
		failures: make(chan error, 1), done: make(chan struct{}),
	}
	go w.run()
	return w
}

// offerTerminal preserves output order in a bounded queue. A saturated slow
// browser cannot grow Agent memory or hold heartbeat writes behind terminal IO.
func (w *socketEnvelopeWriter) offerTerminal(ctx context.Context, envelope protocol.Envelope) error {
	select {
	case w.terminal <- envelope:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return errors.New("Agent socket writer stopped")
	default:
		return errTerminalWriterQueueFull
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
	terminalBurst := 0
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
			w.write(request.envelope, request.result, controlWriteTimeout)
			continue
		default:
		}
		if terminalBurst >= maxTerminalWriteBurst {
			select {
			case envelope := <-w.metrics:
				w.write(envelope, nil, metricWriteTimeout)
				terminalBurst = 0
				continue
			default:
			}
			select {
			case envelope := <-w.docker:
				w.write(envelope, nil, dockerWriteTimeout)
				dockerBurst++
				terminalBurst = 0
				continue
			default:
			}
		}
		select {
		case envelope := <-w.terminal:
			w.write(envelope, nil, terminalWriteTimeout)
			terminalBurst++
			continue
		default:
		}
		// Snapshot/event frames remain FIFO, but an always-nonempty Docker
		// queue must not starve the latest coalesced host metrics sample.
		if dockerBurst >= maxDockerWriteBurst {
			select {
			case envelope := <-w.metrics:
				w.write(envelope, nil, metricWriteTimeout)
				dockerBurst = 0
				continue
			default:
			}
		}
		select {
		case envelope := <-w.docker:
			w.write(envelope, nil, dockerWriteTimeout)
			dockerBurst++
			continue
		default:
		}
		select {
		case <-w.ctx.Done():
			return
		case request := <-w.high:
			w.write(request.envelope, request.result, controlWriteTimeout)
		case envelope := <-w.terminal:
			w.write(envelope, nil, terminalWriteTimeout)
			terminalBurst++
		case envelope := <-w.docker:
			w.write(envelope, nil, dockerWriteTimeout)
			dockerBurst++
		case envelope := <-w.metrics:
			w.write(envelope, nil, metricWriteTimeout)
			dockerBurst = 0
			terminalBurst = 0
		}
	}
}

func (w *socketEnvelopeWriter) write(envelope protocol.Envelope, result chan error, timeout time.Duration) {
	err := writeSocketEnvelopeWithTimeout(w.ctx, w.conn, envelope, timeout)
	if result != nil {
		result <- err
	}
	if err != nil {
		select {
		case w.failures <- err:
		default:
		}
	}
}

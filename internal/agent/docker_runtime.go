package agent

import (
	"context"
	"errors"
	"sync"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// socketDockerObserver translates the discovery component's safe DTO into
// generation-bound WebSocket frames. The writer queue is bounded FIFO; it
// never coalesces snapshot chunks.
type socketDockerObserver struct {
	writer     *socketEnvelopeWriter
	generation uint64
	mu         sync.Mutex
	sequence   uint64
}

func (o *socketDockerObserver) ApplyDockerBatch(ctx context.Context, batch agentdocker.Batch) error {
	if o == nil || o.writer == nil || o.generation == 0 {
		return errors.New("Docker observer is not bound to an Agent connection")
	}
	converted, err := agentdocker.ToProtocolBatch(batch)
	if err != nil {
		return err
	}
	payload, err := protocol.MarshalDockerBatch(converted)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.sequence++
	sequence := o.sequence
	o.mu.Unlock()
	envelope := protocol.Envelope{
		Version: protocol.CurrentVersion, Type: protocol.TypeDocker,
		Generation: o.generation, Sequence: sequence, Payload: payload,
	}
	return o.writer.offerDocker(ctx, envelope)
}

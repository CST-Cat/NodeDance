package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

type writerTestChannel struct {
	protocol.AgentChannel
	write func(protocol.Envelope)
}

func (c writerTestChannel) Write(_ context.Context, _ websocket.MessageType, data []byte) error {
	var envelope protocol.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	c.write(envelope)
	return nil
}

func TestWriterServesEveryLaneUnderContinuousDockerLoad(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &socketEnvelopeWriter{
		ctx: ctx, cancel: cancel, done: make(chan struct{}), high: make(chan envelopeWrite, 1),
		docker: make(chan protocol.Envelope, 2), metrics: make(chan protocol.Envelope, 1),
		terminal: make(chan protocol.Envelope, 1), streams: make(chan protocol.Envelope, 1), files: make(chan protocol.Envelope, 1),
	}
	lanes := map[string]chan protocol.Envelope{"docker": w.docker, "metrics": w.metrics, "terminal": w.terminal, "streams": w.streams, "files": w.files}
	for name, queue := range lanes {
		queue <- protocol.Envelope{Type: name}
	}
	w.docker <- protocol.Envelope{Type: "docker", Sequence: 1}
	w.high <- envelopeWrite{envelope: protocol.Envelope{Type: "control"}}
	counts := make(map[string]int)
	writes := 0
	controlFirst := false
	controlPreempted := false
	dockerOrdered := true
	w.conn = writerTestChannel{write: func(envelope protocol.Envelope) {
		if writes == 0 {
			controlFirst = envelope.Type == "control"
		}
		writes++
		if writes == 11 {
			controlPreempted = envelope.Type == "control"
		}
		if envelope.Type == "docker" {
			dockerOrdered = dockerOrdered && envelope.Sequence == uint64(counts["docker"])
			envelope.Sequence += 2
		}
		counts[envelope.Type]++
		if queue := lanes[envelope.Type]; queue != nil {
			queue <- envelope
		}
		// No timer races or producer scheduling assumptions: every data lane
		// is synchronously replenished before the next scheduling decision.
		if writes == 101 {
			cancel()
		}
		if writes == 10 {
			w.high <- envelopeWrite{envelope: protocol.Envelope{Type: "control"}}
		}
	}}
	go w.run()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not stop")
	}
	if !controlFirst {
		t.Error("ready control message lost priority")
	}
	if !controlPreempted {
		t.Error("control message did not interrupt the current data burst")
	}
	if !dockerOrdered {
		t.Error("Docker frames lost FIFO ordering")
	}
	for name := range lanes {
		if counts[name] < 3 {
			t.Errorf("%s starved: %d writes out of %d", name, counts[name], writes)
		}
	}
}

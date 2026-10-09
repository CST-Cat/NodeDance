package probes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const MaxConcurrent = 4

type EnvelopeWriter interface {
	Send(context.Context, protocol.Envelope) error
}

type Bridge struct {
	Executor *Executor
	Slots    chan struct{}
}

func NewBridge(executor *Executor) *Bridge {
	if executor == nil {
		executor = NewExecutor()
	}
	return &Bridge{Executor: executor, Slots: make(chan struct{}, MaxConcurrent)}
}

func (b *Bridge) Run(ctx context.Context, writer EnvelopeWriter, generation uint64, nodeID string, incoming <-chan protocol.Envelope) error {
	if b == nil || b.Executor == nil || b.Slots == nil || generation == 0 || nodeID == "" {
		return errors.New("Agent probe bridge is not initialized")
	}
	type outcome struct {
		report protocol.ProbeReport
	}
	results := make(chan outcome, MaxConcurrent)
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		select {
		case <-ctx.Done():
			return nil
		case completed := <-results:
			if err := b.writeReport(ctx, writer, generation, completed.report); err != nil {
				return err
			}
		case envelope, ok := <-incoming:
			if !ok {
				return errors.New("Agent probe command channel closed")
			}
			var dispatch protocol.ProbeDispatch
			if envelope.Type != protocol.TypeProbeDispatch || envelope.Sequence != 0 || decodeProbePayload(envelope.Payload, &dispatch) != nil ||
				protocol.ValidateProbeDispatch(envelope, dispatch, nodeID, generation) != nil {
				return errors.New("Core service probe dispatch is invalid")
			}
			select {
			case b.Slots <- struct{}{}:
				workers.Add(1)
				go func(dispatch protocol.ProbeDispatch) {
					defer workers.Done()
					defer func() { <-b.Slots }()
					results <- outcome{report: b.Executor.Execute(ctx, dispatch)}
				}(dispatch)
			default:
				report := protocol.ProbeReport{ProbeID: dispatch.ProbeID, RunID: dispatch.RunID, NodeID: nodeID,
					Status: protocol.ProbeResultUnknown, ErrorCode: "capacity"}
				if err := b.writeReport(ctx, writer, generation, report); err != nil {
					return err
				}
			}
		}
	}
}

func (b *Bridge) writeReport(ctx context.Context, writer EnvelopeWriter, generation uint64, report protocol.ProbeReport) error {
	if report.Status == "" {
		return errors.New("Agent probe executor returned an empty result")
	}
	return writer.Send(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeProbeReport,
		Generation: generation, RequestID: report.RunID, Payload: mustMarshalProbe(report)})
}

func decodeProbePayload(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("probe payload has trailing data")
	}
	return nil
}

func mustMarshalProbe(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

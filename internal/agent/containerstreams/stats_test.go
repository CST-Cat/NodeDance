package containerstreams

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type statsFixtureEngine struct {
	body  io.ReadCloser
	opens atomic.Int32
}

func (e *statsFixtureEngine) OpenStats(context.Context, string) (io.ReadCloser, error) {
	e.opens.Add(1)
	return e.body, nil
}

func TestSharedStatsStreamPreservesWireValuesAndSubscriberIsolation(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { reader.Close(); writer.Close() })
	engine := &statsFixtureEngine{body: reader}
	manager, err := NewStatsManager(engine, StatsManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id := strings.Repeat("a", 64)
	first, err := manager.Subscribe(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := manager.Subscribe(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	const used = uint64(9007199254740993)
	go func() {
		_, _ = io.WriteString(writer, `{"id":"`+id+`","read":"2026-10-12T12:00:00Z","memory_stats":{"usage":9007199254740993,"limit":18014398509481986,"stats":{"inactive_file":0}}}`+"\n")
	}()
	receive := func(sub *StatsSubscription) protocol.ContainerStatsSnapshot {
		t.Helper()
		select {
		case snapshot, ok := <-sub.Updates:
			if !ok {
				t.Fatal("stats stream closed before delivering a sample")
			}
			return snapshot
		case <-ctx.Done():
			t.Fatal("stats sample timed out")
		}
		return protocol.ContainerStatsSnapshot{}
	}
	a, b := receive(first), receive(second)
	if engine.opens.Load() != 1 {
		t.Fatal("subscribers opened duplicate Engine streams")
	}
	if a.Memory.State != "ok" || a.Memory.Value == nil || a.Memory.Value.UsedBytes != used || a.Memory.Value.UsedPercent != 50 {
		t.Fatalf("memory precision lost: %+v", a.Memory)
	}
	if a.CPUPercent.State != "unknown" || a.CPUPercent.Value != nil || a.Network.State != "unknown" || a.BlockIO.State != "unknown" {
		t.Fatalf("missing samples became fabricated values: %+v", a)
	}
	data, err := json.Marshal(protocol.ContainerStreamStats{Snapshot: a})
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateContainerStreamEnvelope(protocol.Envelope{
		Version: protocol.CurrentVersion, Type: protocol.TypeContainerStats, Generation: 1, Sequence: 1,
		RequestID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Payload: data,
	}, 1); err != nil {
		t.Fatalf("shared sample violates wire contract: %v", err)
	}
	a.Memory.Value.UsedBytes = 1
	*a.Memory.SampledAt = time.Time{}
	*a.ObservedAt = time.Time{}
	if b.Memory.Value.UsedBytes != used || b.Memory.SampledAt.IsZero() || b.ObservedAt.IsZero() {
		t.Fatal("subscribers share mutable sample memory")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

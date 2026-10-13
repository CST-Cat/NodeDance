package containerrebuild

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"path/filepath"
	"testing"
	"time"
)

type waitingEngine struct{ Engine }

func (waitingEngine) Inspect(ctx context.Context, _ string) (Container, error) {
	<-ctx.Done()
	return Container{}, ctx.Err()
}

type uncertainJournal struct{ TaskJournal }

func (uncertainJournal) MarkUnknown(context.Context, string) error { return nil }
func (uncertainJournal) Get(context.Context, string) (taskjournal.Snapshot, error) {
	return taskjournal.Snapshot{Status: taskstate.Unknown}, nil
}

func TestRebuildTotalTimeoutBoundsInspection(t *testing.T) {
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "private", "rebuild.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManager(waitingEngine{}, uncertainJournal{}, store, Options{OperationTimeout: time.Second, VerifyTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.executeNew(ctx, Request{TaskID: "fixture"}); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unverified rebuild succeeded")
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("operation timeout was not applied")
	}
}

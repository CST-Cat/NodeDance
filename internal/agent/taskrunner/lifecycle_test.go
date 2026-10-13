package taskrunner

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recoveryJournal struct {
	Journal
	entered, release chan struct{}
	ctx              context.Context
}

func (j *recoveryJournal) RecoverInterrupted(ctx context.Context) (int64, error) {
	j.ctx = ctx
	close(j.entered)
	<-j.release
	return 0, nil
}

type unusedExecutor struct{ Executor }

func TestStopJoinsStartupRecovery(t *testing.T) {
	j := &recoveryJournal{entered: make(chan struct{}), release: make(chan struct{})}
	r, err := New("node", j, unusedExecutor{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- r.Start(context.Background()) }()
	<-j.entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = r.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stop returned before recovery exited: %v", err)
	}
	if j.ctx.Err() == nil {
		t.Error("recovery context was not canceled")
	}
	close(j.release)
	if err := <-started; !errors.Is(err, ErrStopped) {
		t.Errorf("startup after stop: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

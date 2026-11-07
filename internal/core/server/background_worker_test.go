package server

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBackgroundWorkerContainsPanic(t *testing.T) {
	s := &Server{}
	s.agentContext, s.agentCancel = context.WithCancel(context.Background())
	defer s.agentCancel()
	s.agentWait.Add(1)
	s.runBackgroundWorker("test-worker", func() {
		panic("boom")
	})
	if !waitGroupSettled(&s.agentWait) {
		t.Fatal("panicking worker did not release its wait count")
	}
}

func waitGroupSettled(group *sync.WaitGroup) bool {
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

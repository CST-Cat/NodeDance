package agent

import (
	"context"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestSweepRsyncSessionsReapsOnlyExpiredDeadAttempts(t *testing.T) {
	now := time.Now()
	bridge := &agentFileBridge{rsyncSessions: map[string]*agentRsyncSession{}}
	add := func(key string, expiresAt time.Time, used, received, signaled bool) *agentRsyncSession {
		ctx, cancel := context.WithCancel(context.Background())
		session := &agentRsyncSession{
			request: protocol.FileRsyncRequest{Grant: protocol.FileStreamGrant{ExpiresAt: expiresAt}},
			ctx:     ctx, cancel: cancel,
			done: make(chan struct{}), signal: make(chan protocol.FileRsyncRequest, 1),
			used: used, received: received, signaled: signaled,
		}
		bridge.rsyncSessions[key] = session
		return session
	}
	expired, fresh := now.Add(-time.Second), now.Add(time.Minute)
	deadPrepare := add("prepare/dead", expired, false, false, false)
	livePrepare := add("prepare/live", expired, true, false, false)
	donePrepare := add("prepare/received", expired, false, true, false)
	deadSend := add("send/dead", expired, false, false, false)
	startedSend := add("send/started", expired, false, false, true)
	freshPrepare := add("prepare/fresh", fresh, false, false, false)

	bridge.sweepRsyncSessions()

	for key, want := range map[string]bool{
		"prepare/dead": false, "prepare/live": true, "prepare/received": true,
		"send/dead": false, "send/started": true, "prepare/fresh": true,
	} {
		if _, got := bridge.rsyncSessions[key]; got != want {
			t.Fatalf("session %q kept=%t, want %t", key, got, want)
		}
	}
	for _, session := range []*agentRsyncSession{deadPrepare, deadSend} {
		if session.ctx.Err() == nil {
			t.Fatal("reaped session was not cancelled")
		}
	}
	for _, session := range []*agentRsyncSession{livePrepare, donePrepare, startedSend, freshPrepare} {
		if session.ctx.Err() != nil {
			t.Fatal("kept session was cancelled")
		}
	}
}

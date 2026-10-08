package server

import (
	"sync"
	"time"
)

type loginLimit struct {
	startedAt time.Time
	attempts  int
	blockedTo time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	entries  map[string]loginLimit
	max      int
	duration time.Duration
	now      func() time.Time
	capacity int
}

func newLoginLimiter(max int, duration time.Duration, now func() time.Time) *loginLimiter {
	return &loginLimiter{entries: make(map[string]loginLimit), max: max, duration: duration, now: now, capacity: 4096}
}

// reserve atomically counts an attempt before expensive password verification.
// The threshold request is allowed to complete, while later concurrent requests
// observe its lockout immediately.
func (l *loginLimiter) reserve(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	state, exists := l.entries[key]
	if exists && now.Before(state.blockedTo) {
		return false
	}
	if exists && (!state.blockedTo.IsZero() || now.Sub(state.startedAt) >= l.duration) {
		delete(l.entries, key)
		exists = false
	}
	if !exists && len(l.entries) >= l.capacity {
		for address, entry := range l.entries {
			if !entry.blockedTo.IsZero() && !now.Before(entry.blockedTo) || entry.blockedTo.IsZero() && now.Sub(entry.startedAt) >= l.duration {
				delete(l.entries, address)
			}
		}
		if len(l.entries) >= l.capacity {
			return false
		}
	}
	if !exists {
		state = loginLimit{startedAt: now}
	}
	state.attempts++
	if state.attempts >= l.max {
		state.blockedTo = now.Add(l.duration)
	}
	l.entries[key] = state
	return true
}

func (l *loginLimiter) succeeded(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

func (s *Server) acquirePasswordSlot() bool {
	select {
	case s.passwordSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releasePasswordSlot() {
	<-s.passwordSlots
}

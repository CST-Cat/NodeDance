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

// guestLimiter is a per-client token bucket over the public guest endpoints.
// The global guest slots bound total concurrency; this keeps one address from
// holding every slot while other visitors still see the display.
type guestLimiter struct {
	mu       sync.Mutex
	entries  map[string]*guestRate
	rate     float64
	burst    float64
	capacity int
	now      func() time.Time
}

type guestRate struct {
	tokens float64
	last   time.Time
}

func newGuestLimiter(now func() time.Time) *guestLimiter {
	return &guestLimiter{entries: make(map[string]*guestRate), rate: 2, burst: 8, capacity: 4096, now: now}
}

func (l *guestLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry, exists := l.entries[key]
	if !exists {
		if len(l.entries) >= l.capacity {
			l.gc(now)
		}
		if len(l.entries) >= l.capacity {
			return false
		}
		entry = &guestRate{tokens: l.burst, last: now}
		l.entries[key] = entry
	}
	if elapsed := now.Sub(entry.last).Seconds(); elapsed > 0 {
		entry.tokens = min(l.burst, entry.tokens+elapsed*l.rate)
		entry.last = now
	}
	if entry.tokens < 1 {
		return false
	}
	entry.tokens--
	return true
}

func (l *guestLimiter) gc(now time.Time) {
	for address, entry := range l.entries {
		if now.Sub(entry.last) > 10*time.Minute {
			delete(l.entries, address)
		}
	}
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

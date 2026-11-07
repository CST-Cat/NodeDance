package server

import (
	"testing"
	"time"
)

func TestGuestLimiterBoundsEachAddress(t *testing.T) {
	current := time.Unix(0, 0)
	limiter := newGuestLimiter(func() time.Time { return current })
	for attempt := 0; attempt < 8; attempt++ {
		if !limiter.allow("203.0.113.9") {
			t.Fatalf("burst request %d rejected", attempt)
		}
	}
	if limiter.allow("203.0.113.9") {
		t.Fatal("exhausted address was not throttled")
	}
	if !limiter.allow("198.51.100.7") {
		t.Fatal("another address was throttled by the first")
	}
	current = current.Add(2 * time.Second)
	if !limiter.allow("203.0.113.9") {
		t.Fatal("throttled address did not refill over time")
	}
}

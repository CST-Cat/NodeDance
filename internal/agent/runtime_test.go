package agent

import (
	"strconv"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestLegacyCoreWelcomeDoesNotEnablePreparedAgentUpdates(t *testing.T) {
	oldCoreCapabilities := []string{protocol.CapabilityAgentUpdates, protocol.CapabilityMetrics}
	if preparedAgentUpdatesEnabled(oldCoreCapabilities) {
		t.Fatal("legacy Core welcome enabled updates without prepared-ACK support")
	}
	if !preparedAgentUpdatesEnabled([]string{protocol.CapabilityAgentUpdatesPreparedAck}) {
		t.Fatal("prepared-ACK Core capability did not enable updates")
	}
}

func TestReconnectDelayRangeHasDeterministicBounds(t *testing.T) {
	tests := []struct {
		attempt int
		wantMin time.Duration
		wantMax time.Duration
	}{
		{attempt: -1, wantMin: time.Second, wantMax: 1200 * time.Millisecond},
		{attempt: 0, wantMin: time.Second, wantMax: 1200 * time.Millisecond},
		{attempt: 1, wantMin: 1600 * time.Millisecond, wantMax: 2400 * time.Millisecond},
		{attempt: 2, wantMin: 3200 * time.Millisecond, wantMax: 4800 * time.Millisecond},
		{attempt: 3, wantMin: 6400 * time.Millisecond, wantMax: 9600 * time.Millisecond},
		{attempt: 4, wantMin: 12800 * time.Millisecond, wantMax: 19200 * time.Millisecond},
		{attempt: 5, wantMin: 24 * time.Second, wantMax: 30 * time.Second},
		{attempt: 6, wantMin: 24 * time.Second, wantMax: 30 * time.Second},
		{attempt: 1000, wantMin: 24 * time.Second, wantMax: 30 * time.Second},
	}
	for _, test := range tests {
		t.Run("attempt_"+strconv.Itoa(test.attempt), func(t *testing.T) {
			minimum, maximum := reconnectDelayRange(test.attempt)
			if minimum != test.wantMin || maximum != test.wantMax {
				t.Fatalf("reconnect delay range for attempt %d = [%s,%s], want [%s,%s]",
					test.attempt, minimum, maximum, test.wantMin, test.wantMax)
			}
			if minimum < minimumReconnectWait || maximum > maximumReconnectWait || minimum > maximum {
				t.Fatalf("invalid reconnect bounds for attempt %d: [%s,%s]", test.attempt, minimum, maximum)
			}
		})
	}
}

func TestReconnectDelaySamplesStayWithinMaximum(t *testing.T) {
	for attempt := 0; attempt <= maximumReconnectAttempt+2; attempt++ {
		minimum, maximum := reconnectDelayRange(attempt)
		for sample := 0; sample < 100; sample++ {
			delay := reconnectDelay(attempt)
			if delay < minimum || delay > maximum || delay > maximumReconnectWait {
				t.Fatalf("attempt %d sampled delay %s outside [%s,%s]", attempt, delay, minimum, maximum)
			}
		}
	}
}

func TestReconnectBackoffResetsAfterEstablishedConnection(t *testing.T) {
	var backoff reconnectBackoff
	for range 12 {
		backoff.failed()
	}
	if backoff.attempt != maximumReconnectAttempt {
		t.Fatalf("failed attempts were not capped: got %d, want %d", backoff.attempt, maximumReconnectAttempt)
	}
	if delay := backoff.delay(); delay < 24*time.Second || delay > maximumReconnectWait {
		t.Fatalf("capped backoff delay = %s, want at most %s", delay, maximumReconnectWait)
	}

	backoff.connected()
	if backoff.attempt != 0 {
		t.Fatalf("validated connection did not reset retry attempt: got %d, want 0", backoff.attempt)
	}
	minimum, maximum := reconnectDelayRange(backoff.attempt)
	if minimum != minimumReconnectWait || maximum != 1200*time.Millisecond {
		t.Fatalf("post-connection retry range = [%s,%s], want [1s,1.2s]", minimum, maximum)
	}
	backoff.failed()
	if backoff.attempt != 1 {
		t.Fatalf("retry after an established connection started at attempt %d, want 1", backoff.attempt)
	}
}

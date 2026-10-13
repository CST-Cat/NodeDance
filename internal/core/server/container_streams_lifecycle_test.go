package server

import (
	"context"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestSlowContainerStreamIsIsolatedAndLateFramesIgnored(t *testing.T) {
	c := &agentConnection{ctx: context.Background(), generation: 1, streams: make(map[string]*coreBrowserStream)}
	c.setCapabilities([]string{protocol.CapabilityContainerStreams})
	makeStream := func(id string) *coreBrowserStream {
		s := &coreBrowserStream{requestID: id, containerID: strings.Repeat("a", 64), kind: protocol.StreamLogs, updates: make(chan protocol.Envelope, 1), done: make(chan struct{})}
		if err := c.addBrowserStream(s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	slow := makeStream("01234567-89ab-4cde-8123-456789abcdef")
	other := makeStream("11234567-89ab-4cde-8123-456789abcdef")
	frame := func(s *coreBrowserStream, seq uint64) protocol.Envelope {
		return protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerLog, Generation: 1, RequestID: s.requestID, Sequence: seq, Payload: marshalAgentPayload(protocol.ContainerStreamLog{ContainerID: s.containerID, Channel: "stdout", Data: []byte("log")})}
	}
	for seq := uint64(1); seq <= 2; seq++ {
		if _, _, err := c.takeAgentStream(frame(slow, seq)); err != nil {
			t.Fatal(err)
		}
	}
	if !slow.isClosed() || len(c.streams) != 1 || other.isClosed() {
		t.Fatal("slow stream was not isolated")
	}
	if stream, ignored, err := c.takeAgentStream(frame(slow, 3)); err != nil || stream != nil || !ignored {
		t.Fatal("late frame was not ignored", err)
	}
	if _, _, err := c.takeAgentStream(frame(other, 1)); err != nil {
		t.Fatal("healthy stream damaged", err)
	}
	c.closeBrowserStreams()
	if !other.isClosed() || len(c.streams) != 0 {
		t.Fatal("disconnect retained streams")
	}
}

package containerstreams

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMultiplexedLogsDecodeStreamsAndNeverExposeHeaders(t *testing.T) {
	stdout := []byte("stdout 你好\n")
	stderr := []byte("stderr 錯誤\n")
	input := append(multiplexedFrame(1, stdout), multiplexedFrame(2, stderr)...)
	frames := make(chan LogFrame, 4)
	if err := copyMultiplexedLogs(context.Background(), bytes.NewReader(input), frames); err != nil {
		t.Fatal(err)
	}
	close(frames)

	var gotStdout, gotStderr []byte
	for frame := range frames {
		switch frame.Channel {
		case LogStdout:
			gotStdout = append(gotStdout, frame.Data...)
		case LogStderr:
			gotStderr = append(gotStderr, frame.Data...)
		default:
			t.Fatalf("unexpected channel %q", frame.Channel)
		}
		if bytes.Contains(frame.Data, []byte{1, 0, 0, 0}) || bytes.Contains(frame.Data, []byte{2, 0, 0, 0}) {
			t.Fatalf("Docker multiplex header leaked into payload %q", frame.Data)
		}
	}
	if !bytes.Equal(gotStdout, stdout) || !bytes.Equal(gotStderr, stderr) {
		t.Fatalf("decoded stdout=%q stderr=%q", gotStdout, gotStderr)
	}
}

func TestRawTTYLogPreservesBytesThatLookLikeDockerHeaders(t *testing.T) {
	input := append([]byte{1, 0, 0, 0, 0, 0, 0, 4, 2, 0, 0, 0}, []byte("TTY 你好")...)
	frames := make(chan LogFrame, 2)
	if err := copyRawLogs(context.Background(), bytes.NewReader(input), frames); err != nil {
		t.Fatal(err)
	}
	close(frames)
	var got []byte
	for frame := range frames {
		if frame.Channel != LogStdout {
			t.Fatalf("TTY bytes reported on %q", frame.Channel)
		}
		got = append(got, frame.Data...)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("raw TTY bytes changed: got %v want %v", got, input)
	}
}

func TestLargeLogFrameIsChunkedAndBackpressured(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4*1024*1024)
	frames := make(chan LogFrame, 1)
	done := make(chan error, 1)
	go func() {
		done <- copyMultiplexedLogs(context.Background(), bytes.NewReader(multiplexedFrame(1, payload)), frames)
		close(frames)
	}()

	var total int
	var largest int
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				if total != len(payload) {
					t.Fatalf("decoded %d bytes, want %d", total, len(payload))
				}
				if largest > LogChunkBytes || cap(frames) != LogQueueChunks && cap(frames) != 1 {
					t.Fatalf("frame size %d or channel capacity %d is unbounded", largest, cap(frames))
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
			if len(frame.Data) > largest {
				largest = len(frame.Data)
			}
			total += len(frame.Data)
		case <-time.After(5 * time.Second):
			t.Fatal("large log stream stalled despite an active consumer")
		}
	}
}

func TestMultiplexedLogRejectsInvalidOrTruncatedFrames(t *testing.T) {
	cases := map[string][]byte{
		"invalid stream type":   {3, 0, 0, 0, 0, 0, 0, 1, 'x'},
		"nonzero reserved byte": {1, 0, 1, 0, 0, 0, 0, 1, 'x'},
		"short header":          {1, 0, 0},
		"short payload":         {1, 0, 0, 0, 0, 0, 0, 3, 'x'},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			frames := make(chan LogFrame, 1)
			err := copyMultiplexedLogs(context.Background(), bytes.NewReader(input), frames)
			if !errors.Is(err, ErrMalformedLogStream) {
				t.Fatalf("decoder error = %v, want ErrMalformedLogStream", err)
			}
			if len(frames) > 0 {
				t.Fatalf("malformed frame emitted payload: %+v", <-frames)
			}
		})
	}
}

func TestNormalizeLogsOptionsBoundsHistoryAndSelection(t *testing.T) {
	defaulted, err := normalizeLogsOptions(LogsOptions{})
	if err != nil || defaulted.Tail != "all" || !defaulted.ShowStdout || !defaulted.ShowStderr {
		t.Fatalf("default options = %+v, error %v", defaulted, err)
	}
	if _, err := normalizeLogsOptions(LogsOptions{Tail: strings.Repeat("9", 64)}); err == nil {
		t.Fatal("oversized tail integer was accepted")
	}
	if _, err := normalizeLogsOptions(LogsOptions{Follow: true, Until: time.Now()}); err == nil {
		t.Fatal("bounded history was allowed to follow")
	}
	if _, err := normalizeLogsOptions(LogsOptions{Since: time.Unix(2, 0), Until: time.Unix(1, 0)}); err == nil {
		t.Fatal("until earlier than since was accepted")
	}
}

func multiplexedFrame(stream byte, payload []byte) []byte {
	frame := make([]byte, 8+len(payload))
	frame[0] = stream
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

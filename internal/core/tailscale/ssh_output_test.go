package tailscale

import (
	"bytes"
	"fmt"
	"strconv"
	"sync"
	"testing"
)

func TestBoundedOutputConcurrentWritersPreserveEachStreamOrder(t *testing.T) {
	const writesPerStream = 4096
	output := &boundedOutput{}
	var writers sync.WaitGroup
	writeErrors := make(chan error, 2)
	for _, stream := range []byte{'A', 'B'} {
		stream := stream
		writers.Add(1)
		go func() {
			defer writers.Done()
			for sequence := 0; sequence < writesPerStream; sequence++ {
				chunk := []byte(fmt.Sprintf("%c%06d\n", stream, sequence))
				if written, err := output.Write(chunk); err != nil || written != len(chunk) {
					writeErrors <- fmt.Errorf("stream %c write %d = %d, %v", stream, sequence, written, err)
					return
				}
			}
		}()
	}
	writers.Wait()
	close(writeErrors)
	for err := range writeErrors {
		t.Error(err)
	}

	got := output.Bytes()
	if len(got) != 64<<10 {
		t.Fatalf("concurrent output length=%d, want exact 64 KiB bound", len(got))
	}
	lines := bytes.Split(got, []byte("\n"))
	if len(lines) != writesPerStream*2+1 || len(lines[len(lines)-1]) != 0 {
		t.Fatalf("concurrent output has %d lines, want %d complete records", len(lines)-1, writesPerStream*2)
	}
	next := map[byte]int{'A': 0, 'B': 0}
	for index, line := range lines[:len(lines)-1] {
		if len(line) != 7 || (line[0] != 'A' && line[0] != 'B') {
			t.Fatalf("output record %d is corrupted: %q", index, line)
		}
		sequence, err := strconv.Atoi(string(line[1:]))
		if err != nil || sequence != next[line[0]] {
			t.Fatalf("stream %c record %q is out of order, want sequence %d (parse error %v)", line[0], line, next[line[0]], err)
		}
		next[line[0]]++
	}
	if next['A'] != writesPerStream || next['B'] != writesPerStream {
		t.Fatalf("record counts are A=%d B=%d, want %d each", next['A'], next['B'], writesPerStream)
	}

	firstByte := got[0]
	got[0] ^= 0xff
	if snapshot := output.Bytes(); snapshot[0] != firstByte {
		t.Fatal("Bytes returned a mutable view into concurrent output storage")
	}
	if written, err := output.Write([]byte("overflow")); err != nil || written != len("overflow") {
		t.Fatalf("write beyond limit = %d, %v; want accepted write count with no error", written, err)
	}
	if length := len(output.Bytes()); length != 64<<10 {
		t.Fatalf("output exceeded the 64 KiB bound: %d", length)
	}
}

func TestBoundedOutputTruncatesWritesAt64KiB(t *testing.T) {
	input := bytes.Repeat([]byte{'x'}, (64<<10)+19)
	var output boundedOutput
	written, err := output.Write(input)
	if err != nil || written != len(input) {
		t.Fatalf("large bounded write = %d, %v; want original input length %d", written, err, len(input))
	}
	got := output.Bytes()
	if len(got) != 64<<10 || !bytes.Equal(got, input[:64<<10]) {
		t.Fatalf("large output retained %d bytes or wrong prefix, want exact first 64 KiB", len(got))
	}
}

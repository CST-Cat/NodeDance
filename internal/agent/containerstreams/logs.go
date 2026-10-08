package containerstreams

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	LogChunkBytes   = 32 * 1024
	LogQueueChunks  = 8
	logCloseTimeout = 5 * time.Second
	maxLogTail      = 1_000_000
)

var (
	ErrMalformedLogStream = errors.New("Docker returned a malformed log stream")
	ErrLogReaderClose     = errors.New("Docker log reader did not stop after close")
)

type LogChannel string

const (
	LogStdout LogChannel = "stdout"
	LogStderr LogChannel = "stderr"
)

// LogsOptions describes a bounded Engine query. Empty Tail means all retained
// history. If both output flags are false, both streams are selected.
type LogsOptions struct {
	Tail       string
	Since      time.Time
	Until      time.Time
	Follow     bool
	Timestamps bool
	ShowStdout bool
	ShowStderr bool
}

// LogFrame contains at most LogChunkBytes of decoded output. Data never
// contains Docker's eight-byte non-TTY multiplex header.
type LogFrame struct {
	Channel LogChannel `json:"channel"`
	Data    []byte     `json:"data"`
}

// LogReader streams decoded frames through a bounded channel. Close cancels
// the Engine request, closes the body, and waits for its reader goroutine.
type LogReader struct {
	Frames <-chan LogFrame
	Errors <-chan error

	cancel   context.CancelFunc
	body     io.ReadCloser
	done     chan struct{}
	once     sync.Once
	closeErr error
}

func OpenLogs(ctx context.Context, engine Engine, id string, options LogsOptions) (*LogReader, error) {
	if ctx == nil || engine == nil {
		return nil, errors.New("Docker log context and engine are required")
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	options, err := normalizeLogsOptions(options)
	if err != nil {
		return nil, err
	}
	info, err := engine.Inspect(ctx, id)
	if err != nil {
		return nil, errors.New("inspect Docker container before reading logs")
	}
	if info.ID != id {
		return nil, errors.New("Docker returned a different container ID")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	body, err := engine.OpenLogs(streamCtx, id, options)
	if err != nil {
		cancel()
		return nil, errors.New("open Docker log stream")
	}
	if body == nil {
		cancel()
		return nil, errors.New("Docker log engine returned an empty stream body")
	}
	return newLogReader(streamCtx, cancel, body, info.TTY), nil
}

func normalizeLogsOptions(options LogsOptions) (LogsOptions, error) {
	if options.Tail == "" {
		options.Tail = "all"
	}
	if options.Tail != "all" {
		tail, err := strconv.ParseUint(options.Tail, 10, 32)
		if err != nil || tail > maxLogTail || strings.TrimSpace(options.Tail) != options.Tail {
			return LogsOptions{}, errors.New("Docker log tail must be all or an integer from 0 to 1000000")
		}
	}
	if !options.Until.IsZero() && options.Follow {
		return LogsOptions{}, errors.New("a bounded log history cannot also follow")
	}
	if !options.Since.IsZero() && !options.Until.IsZero() && options.Until.Before(options.Since) {
		return LogsOptions{}, errors.New("Docker log until time precedes since time")
	}
	if !options.ShowStdout && !options.ShowStderr {
		options.ShowStdout = true
		options.ShowStderr = true
	}
	return options, nil
}

func newLogReader(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, tty bool) *LogReader {
	frames := make(chan LogFrame, LogQueueChunks)
	errorsOut := make(chan error, 1)
	reader := &LogReader{Frames: frames, Errors: errorsOut, cancel: cancel, body: body, done: make(chan struct{})}
	stopClose := context.AfterFunc(ctx, func() { _ = body.Close() })
	go func() {
		defer close(reader.done)
		defer close(frames)
		defer close(errorsOut)
		defer stopClose()
		defer body.Close()
		var err error
		if tty {
			err = copyRawLogs(ctx, body, frames)
		} else {
			err = copyMultiplexedLogs(ctx, body, frames)
		}
		if err != nil && ctx.Err() == nil {
			select {
			case errorsOut <- err:
			default:
			}
		}
	}()
	return reader
}

func (r *LogReader) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		r.cancel()
		if r.body != nil {
			if err := r.body.Close(); err != nil && !errors.Is(err, context.Canceled) {
				r.closeErr = err
			}
		}
		select {
		case <-r.done:
		case <-time.After(logCloseTimeout):
			r.closeErr = ErrLogReaderClose
		}
	})
	return r.closeErr
}

func copyRawLogs(ctx context.Context, source io.Reader, frames chan<- LogFrame) error {
	buffer := make([]byte, LogChunkBytes)
	for {
		count, err := source.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			if sendLogFrame(ctx, frames, LogFrame{Channel: LogStdout, Data: data}) != nil {
				return nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read raw Docker logs: %w", err)
		}
	}
}

func copyMultiplexedLogs(ctx context.Context, source io.Reader, frames chan<- LogFrame) error {
	var header [8]byte
	for {
		_, err := io.ReadFull(source, header[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrMalformedLogStream
		}
		for _, reserved := range header[1:4] {
			if reserved != 0 {
				return ErrMalformedLogStream
			}
		}
		var channel LogChannel
		switch header[0] {
		case 1:
			channel = LogStdout
		case 2:
			channel = LogStderr
		default:
			return ErrMalformedLogStream
		}
		remaining := binary.BigEndian.Uint32(header[4:])
		for remaining > 0 {
			chunkSize := uint32(LogChunkBytes)
			if remaining < chunkSize {
				chunkSize = remaining
			}
			data := make([]byte, int(chunkSize))
			if _, err := io.ReadFull(source, data); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return ErrMalformedLogStream
			}
			if err := sendLogFrame(ctx, frames, LogFrame{Channel: channel, Data: data}); err != nil {
				return nil
			}
			remaining -= chunkSize
		}
	}
}

func sendLogFrame(ctx context.Context, frames chan<- LogFrame, frame LogFrame) error {
	select {
	case frames <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validateID(id string) error {
	if len(id) != 64 {
		return ErrInvalidContainerID
	}
	for _, char := range id {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return ErrInvalidContainerID
		}
	}
	return nil
}

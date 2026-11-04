package filetransport

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

type Connection interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Reader(context.Context) (websocket.MessageType, io.Reader, error)
	Write(context.Context, websocket.MessageType, []byte) error
	SetReadLimit(int64)
	CloseNow() error
	Close(websocket.StatusCode, string) error
}

type Stream struct {
	ctx        context.Context
	conn       Connection
	reader     io.Reader
	eof        bool
	mu         sync.Mutex
	writtenEOF bool
}

func New(ctx context.Context, conn Connection) *Stream {
	conn.SetReadLimit(protocol.MaxFileStreamMessage)
	return &Stream{ctx: ctx, conn: conn}
}

func (s *Stream) Read(p []byte) (int, error) {
	for {
		if s.eof {
			return 0, io.EOF
		}
		if s.reader == nil {
			kind, reader, err := s.conn.Reader(s.ctx)
			if err != nil {
				return 0, err
			}
			if kind == websocket.MessageText {
				data, err := io.ReadAll(io.LimitReader(reader, 4))
				if err != nil || string(data) != "EOF" {
					return 0, errors.New("invalid file stream EOF")
				}
				s.eof = true
				return 0, io.EOF
			}
			if kind != websocket.MessageBinary {
				return 0, errors.New("invalid file stream message")
			}
			s.reader = reader
		}
		n, err := s.reader.Read(p)
		if errors.Is(err, io.EOF) {
			s.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writtenEOF {
		return 0, io.ErrClosedPipe
	}
	if err := s.conn.Write(s.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Stream) CloseWrite() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writtenEOF {
		return nil
	}
	s.writtenEOF = true
	return s.conn.Write(s.ctx, websocket.MessageText, []byte("EOF"))
}

func Forward(ctx context.Context, from, to *websocket.Conn) error {
	from.SetReadLimit(protocol.MaxFileStreamMessage)
	for {
		kind, reader, err := from.Reader(ctx)
		if err != nil {
			return err
		}
		writer, err := to.Writer(ctx, kind)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, reader)
		closeErr := writer.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

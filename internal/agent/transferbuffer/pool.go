// Package transferbuffer reuses fixed-size file buffers. Each lease belongs to
// one transfer, and Release clears file contents before returning it to the pool.
package transferbuffer

import (
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"sync"
)

type Buffer [protocol.MaxFileChunkBytes]byte

var buffers = sync.Pool{New: func() any { return new(Buffer) }}

func Acquire() *Buffer { return buffers.Get().(*Buffer) }

// Release must be called exactly once, after every reader/writer has finished.
func Release(b *Buffer) { clear(b[:]); buffers.Put(b) }

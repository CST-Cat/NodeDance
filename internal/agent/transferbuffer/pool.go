package transferbuffer

import (
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"sync"
)

type Buffer [protocol.MaxFileChunkBytes]byte

var buffers = sync.Pool{New: func() any { return new(Buffer) }}

func Acquire() *Buffer { return buffers.Get().(*Buffer) }

func Release(b *Buffer) { clear(b[:]); buffers.Put(b) }

package transferbuffer

import "testing"

func TestReleasedFileContentsAreCleared(t *testing.T) {
	b := Acquire()
	for i := range b {
		b[i] = 0xa5
	}
	Release(b)
	// The pointer is inspected only after return, without acquiring another lease.
	// The pool can evict it, but it must already have been scrubbed.
	for _, v := range b {
		if v != 0 {
			t.Fatal("file content retained in pool")
		}
	}
}

var benchmarkSink *Buffer

func BenchmarkTransferBuffer(b *testing.B) {
	b.Run("allocate", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			v := new(Buffer)
			v[0] = 1
			benchmarkSink = v
		}
	})
	b.Run("reuse-and-clear", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			v := Acquire()
			v[0] = 1
			Release(v)
		}
	})
}

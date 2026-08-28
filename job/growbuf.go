package job

import (
	"context"
	"io"
	"sync"
)

// growBuf is an append-only byte buffer with blocking, offset-based reads:
// a Read past the current length blocks until more data is written or the
// buffer is closed, at which point it returns io.EOF. This is what gives
// stdout/stderr/events multiple independent readers at their own offset,
// each seeing a live tail, for free — a grown-buffer-plus-offset-Read is
// exactly the shape server.File.Read already wants.
//
// A short (non-empty) read with a nil error is intentional here, not a
// bug: server.File.Read's contract (see p9/server's doc comment) is
// io.ReaderAt-like, and per io.ReaderAt's actual contract a conforming
// implementation blocks for *all* of len(p) or an error — which doesn't
// fit an indefinitely-growing live stream. Consumers must read this kind
// of file with the client's plain Read (or io.Copy through it), not
// ReadAt, exactly as the two client methods are meant to be used
// differently.
type growBuf struct {
	mu     sync.Mutex
	cond   *sync.Cond
	data   []byte
	closed bool
}

func newGrowBuf() *growBuf {
	b := &growBuf{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *growBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	b.data = append(b.data, p...)
	b.cond.Broadcast()
	return len(p), nil
}

func (b *growBuf) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.cond.Broadcast()
}

// Read blocks until offset < len(data) or the buffer is closed, then
// returns whatever's available from offset (possibly less than len(p)).
// It honors ctx cancellation (a Tflush arrives as ctx.Done()) by racing a
// watcher goroutine against the blocking cond.Wait.
func (b *growBuf) Read(ctx context.Context, offset int64, p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for offset >= int64(len(b.data)) {
		if b.closed {
			return 0, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		woken := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				b.cond.Broadcast()
			case <-woken:
			}
		}()
		b.cond.Wait()
		close(woken)
	}
	return copy(p, b.data[offset:]), nil
}

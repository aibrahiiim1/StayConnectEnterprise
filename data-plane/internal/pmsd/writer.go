package pmsd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// writeReq: body=="" is an activity-only signal (reset the idle timer, write nothing). A non-nil ack receives
// the write result.
type writeReq struct {
	body string
	ack  chan error
	// financial marks a PS from the financial relay; only SubmitFinancial sets it, and the writer routes it to
	// guardedConn.writeFinancialFrame, which accepts a PS and nothing else.
	financial bool
	// guard (financial only) runs on the writer goroutine immediately before the first byte; a non-empty code
	// means do not write (GuardRefusedError). It must be fast and non-blocking.
	guard func() string
}

// serialWriter is the sole owner of the outbound socket: exactly one goroutine (run) calls g.writeFrame, so
// frames never overlap. It owns the idle-LA keepalive and, on cancellation, emits LE under a bounded write
// deadline then closes the transport to release a parked reader. The request channel is bounded.
type serialWriter struct {
	g        *guardedConn
	interval time.Duration // idle keepalive period (0 disables the idle LA)
	reqCh    chan writeReq
	done     chan struct{}

	errOnce sync.Once
	err     error // the write failure that ended ownership (nil on clean cancellation)
}

func newSerialWriter(g *guardedConn, interval time.Duration) *serialWriter {
	return &serialWriter{g: g, interval: interval, reqCh: make(chan writeReq, 8), done: make(chan struct{})}
}

// run: any request, including an activity-only ping, resets the idle timer. On cancellation it writes a
// best-effort LE then closes the transport; a write failure records the error and ends ownership.
func (w *serialWriter) run(ctx context.Context) {
	defer close(w.done)
	var idle *time.Ticker
	var idleC <-chan time.Time
	if w.interval > 0 {
		idle = time.NewTicker(w.interval)
		defer idle.Stop()
		idleC = idle.C
	}
	resetIdle := func() {
		if idle != nil {
			idle.Reset(w.interval)
		}
	}
	for {
		select {
		case <-ctx.Done():
			_ = w.g.writeFrame(pms.BuildLE())
			w.closeConn()
			return
		case req := <-w.reqCh:
			resetIdle()
			if req.financial && req.guard != nil {
				// First-byte boundary: the last point at which the command is provably unwritten.
				if code := req.guard(); code != "" {
					if req.ack != nil {
						req.ack <- &GuardRefusedError{Code: code}
					}
					continue
				}
			}
			if req.body == "" {
				if req.ack != nil {
					req.ack <- nil
				}
				continue
			}
			var err error
			if req.financial {
				err = w.g.writeFinancialFrame(req.body)
			} else {
				err = w.g.writeFrame(req.body)
			}
			if req.ack != nil {
				req.ack <- err
			}
			if err != nil {
				w.fail(err)
				return
			}
		case <-idleC:
			if err := w.g.writeFrame(pms.BuildLA()); err != nil {
				w.fail(err)
				return
			}
		}
	}
}

func (w *serialWriter) fail(err error) {
	w.errOnce.Do(func() { w.err = err })
	w.closeConn()
}

func (w *serialWriter) closeConn() {
	if w.g != nil && w.g.c != nil {
		_ = w.g.c.Close() // unblocks a parked reader
	}
}

// Submit is fire-and-forget; a caller never blocks indefinitely behind a stalled socket.
func (w *serialWriter) Submit(ctx context.Context, body string) error {
	select {
	case w.reqCh <- writeReq{body: body}:
		return nil
	case <-ctx.Done():
		return coded(CodeContextCanceled, ctx.Err())
	case <-w.done:
		return w.stoppedErr()
	}
}

// SubmitSync is for critical frames (startup, LA acks, DR) so a write failure is seen immediately.
func (w *serialWriter) SubmitSync(ctx context.Context, body string) error {
	ack := make(chan error, 1)
	select {
	case w.reqCh <- writeReq{body: body, ack: ack}:
	case <-ctx.Done():
		return coded(CodeContextCanceled, ctx.Err())
	case <-w.done:
		return w.stoppedErr()
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return coded(CodeContextCanceled, ctx.Err())
	case <-w.done:
		return w.stoppedErr()
	}
}

// GuardRefusedError proves no byte was written: the guard runs before writeFinancialFrame in the only writing goroutine.
type GuardRefusedError struct{ Code string }

func (e *GuardRefusedError) Error() string { return "pre-write guard refused: " + e.Code }

// SubmitFinancial: enqueued=false means provably nothing was written. Once taken, the write may be partial
// or complete whatever error follows, so treat any err as UNKNOWN. A guard refusal returns
// (true, *GuardRefusedError), which is provably NOT written.
func (w *serialWriter) SubmitFinancial(ctx context.Context, body string, guard func() string) (enqueued bool, err error) {
	ack := make(chan error, 1)
	select {
	case w.reqCh <- writeReq{body: body, ack: ack, financial: true, guard: guard}:
	case <-ctx.Done():
		return false, coded(CodeContextCanceled, ctx.Err())
	case <-w.done:
		return false, w.stoppedErr()
	}
	select {
	case err := <-ack:
		return true, err
	case <-ctx.Done():
		return true, coded(CodeContextCanceled, ctx.Err())
	case <-w.done:
		return true, w.stoppedErr()
	}
}

// activity is best-effort and non-blocking: a full channel means the writer is already resetting the timer.
func (w *serialWriter) activity() {
	select {
	case w.reqCh <- writeReq{}:
	default:
	}
}

// Reading w.err after observing w.done is safe: run sets it (in fail) before close(w.done).
func (w *serialWriter) stoppedErr() error {
	if w.err != nil {
		return w.err
	}
	return coded(CodeProtocolLinkEnded, errors.New("protocol writer stopped"))
}

package pmsd

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// writeReq is one request to the single serialized protocol writer. body=="" is an ACTIVITY-ONLY signal
// (reset the idle-keepalive timer, write nothing). ack, when non-nil, receives the write result so the caller
// can observe success/failure synchronously.
type writeReq struct {
	body string
	ack  chan error
}

// serialWriter is the SOLE owner of the outbound socket. EXACTLY ONE goroutine (run) ever calls
// g.writeFrame, so no two frames overlap and every LS/LD/LR/LA/DR/LE frame is serialized through one owner.
// It also owns the idle-LA keepalive ticker and, on cancellation, emits a controlled LE within a BOUNDED
// write deadline and then closes the transport so a parked reader is released promptly (independent of the
// heartbeat read timeout). The read loop, cancellation and startup interact ONLY by submitting writeReqs —
// no other goroutine writes. The request channel is BOUNDED (never an unbounded writer queue).
type serialWriter struct {
	g        *guardedConn
	interval time.Duration // idle keepalive period (0 disables the idle LA)
	reqCh    chan writeReq
	done     chan struct{} // closed exactly once, when run returns

	errOnce sync.Once
	err     error // the write failure that ended ownership (nil on clean cancellation)
}

func newSerialWriter(g *guardedConn, interval time.Duration) *serialWriter {
	return &serialWriter{g: g, interval: interval, reqCh: make(chan writeReq, 8), done: make(chan struct{})}
}

// run is the single writer goroutine. It writes startup/ack/DR frames on request and a bare LA whenever the
// connection has been idle for `interval`. ANY request (including an activity-only ping) resets the idle
// timer. On ctx cancellation it writes a best-effort LE (controlled shutdown) THEN closes the transport to
// unblock a parked reader; on any write failure it records the error, closes the transport, and returns
// (ending ownership).
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
			_ = w.g.writeFrame(pms.BuildLE()) // controlled shutdown, bounded by the write deadline
			w.closeConn()                     // release a reader parked in ReadFramedRecord promptly
			return
		case req := <-w.reqCh:
			resetIdle() // any activity resets the keepalive timer
			if req.body == "" {
				if req.ack != nil {
					req.ack <- nil
				}
				continue
			}
			err := w.g.writeFrame(req.body)
			if req.ack != nil {
				req.ack <- err
			}
			if err != nil {
				w.fail(err)
				return // write failure ends the ownership cycle
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
		_ = w.g.c.Close() // idempotent enough for net.Conn; unblocks a parked reader
	}
}

// Submit enqueues a fire-and-forget frame with bounded waiting. It returns when the frame is queued, when the
// caller's context is cancelled, or when the writer has stopped (returning the writer's failure). A caller
// never blocks indefinitely behind a stalled socket; the bounded channel provides back-pressure.
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

// SubmitSync writes a frame and waits for its result. Used for CRITICAL frames (startup LS/LD/LR, LA
// acknowledgements, DR) so a write failure is observed by the caller immediately. Bounded by the caller's
// context and by writer termination.
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

// activity resets the idle keepalive timer without writing. It is best-effort and NON-BLOCKING: if the
// request channel is momentarily full the writer is already draining requests (each of which resets the
// timer), so a dropped ping changes nothing.
func (w *serialWriter) activity() {
	select {
	case w.reqCh <- writeReq{}:
	default:
	}
}

// stoppedErr returns the recorded write failure if any, else a generic link-ended code. Reading w.err after
// observing w.done is safe: run sets w.err (in fail) before returning, and close(w.done) happens-after.
func (w *serialWriter) stoppedErr() error {
	if w.err != nil {
		return w.err
	}
	return coded(CodeProtocolLinkEnded, errors.New("protocol writer stopped"))
}

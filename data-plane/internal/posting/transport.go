package posting

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
)

// Transport is the only way a financial record leaves this process; every Transport is wrapped by
// DarkGuard, which makes egress a single chokepoint.
type Transport interface {
	// SendPS transmits a PS and waits for the PA answering this attempt. The returned PA is verified
	// against pNumber. (pa, nil) is a conclusive match; (nil, err) with NotTransmitted(err) means the
	// bytes provably never left; any other error is UNKNOWN and must not be guessed at.
	SendPS(ctx context.Context, interfaceID string, pNumber int64, body string) (*PA, error)
}

// ErrNotTransmitted marks a failure that provably happened before any byte reached the PMS; only these
// may be retried automatically.
var ErrNotTransmitted = errors.New("PS was not transmitted")

// ErrTransmittedNoAnswer marks a PS that was written but never conclusively answered (UNKNOWN).
var ErrTransmittedNoAnswer = errors.New("PS was transmitted but no PA was conclusively matched")

func NotTransmitted(err error) bool { return errors.Is(err, ErrNotTransmitted) }

// DarkGuard refuses before delegating while DARK, so the inner transport is never called. The refusal is
// classified NOT transmitted, which keeps it out of the UNKNOWN state. Fields and constructor are
// unexported so no caller can build a guard with its own config or swap in an unguarded transport; the
// only exported route is NewProductionEngine.
type DarkGuard struct {
	cfg   Config
	inner Transport

	mu       sync.Mutex
	refusals int
	lastBody string
}

// newDarkGuard is unexported on purpose (an exported one would be an independently configured financial
// sender). inner may be nil in DARK deployments.
func newDarkGuard(cfg Config, inner Transport) *DarkGuard { return &DarkGuard{cfg: cfg, inner: inner} }

func (d *DarkGuard) SendPS(ctx context.Context, interfaceID string, pNumber int64, body string) (*PA, error) {
	if d.cfg.Dark() {
		d.mu.Lock()
		d.refusals++
		d.lastBody = body
		d.mu.Unlock()
		return nil, refusedDark("phase-4 financial transmission is DARK; no PMS bytes may be produced")
	}
	if len(body) < 2 || body[:2] != RecordPS {
		return nil, refusedDark("only a PS record may be sent through the financial transport")
	}
	if !pmsd.IsFinancialRecord(RecordPS) {
		// If PS stopped being classified financial, the read-only allowlist would silently change meaning.
		return nil, refusedDark("PS is no longer classified as a financial record; refusing to transmit")
	}
	// A body P# that differs from the allocated one means the record and allocation have diverged.
	if pnOfBody(body) != pNumber {
		return nil, refusedDark("the PS body does not carry the allocated protocol-attempt reference")
	}
	if d.inner == nil {
		return nil, refusedDark("no financial transport is configured")
	}
	pa, err := d.inner.SendPS(ctx, interfaceID, pNumber, body)
	if err != nil {
		return nil, err
	}
	// An answer for another P# is not this attempt's answer; the engine checks this too.
	if pa != nil && pa.PNumber != pNumber {
		return nil, fail(ErrPAWrongPNumber, "the PA answers a different protocol-attempt reference")
	}
	return pa, nil
}

func pnOfBody(body string) int64 {
	const tag = "|P#"
	i := strings.Index(body, tag)
	if i < 0 {
		return 0
	}
	rest := body[i+len(tag):]
	var n int64
	for k := 0; k < len(rest); k++ {
		c := rest[k]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// Refusals counts refused transmissions, so tests can assert the worker tried and was stopped.
func (d *DarkGuard) Refusals() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.refusals
}

// LastRefusedBody returns the last refused PS body, so tests can assert a complete PS was built.
func (d *DarkGuard) LastRefusedBody() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastBody
}

func refusedDark(msg string) error {
	return &darkError{e: &Error{Code: ErrDarkNoEgress, Msg: msg}}
}

// darkError is both a typed posting error and an ErrNotTransmitted, so a refusal is never taken for UNKNOWN.
type darkError struct{ e *Error }

func (d *darkError) Error() string   { return d.e.Error() }
func (d *darkError) Unwrap() []error { return []error{d.e, ErrNotTransmitted} }

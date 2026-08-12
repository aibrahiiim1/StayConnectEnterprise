package posting

import (
	"context"
	"errors"
	"sync"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
)

// Transport is the ONLY way a financial record can leave this process.
//
// There is deliberately no exported raw writer anywhere in this package. A caller that wants to put a PS on
// a socket has to go through a Transport, and every Transport in the tree is wrapped by DarkGuard, so
// "financial egress" is a single, testable chokepoint rather than a property of how carefully each call
// site was written.
type Transport interface {
	// SendPS transmits a PS record body and waits for its correlated PA.
	//
	// The three-valued return is the entire UNKNOWN contract. (pa, nil) means a PA was conclusively
	// matched. (nil, err) where NotTransmitted(err) is true means the bytes provably never left. (nil, err)
	// where it is false means the outcome is UNKNOWN: the PS may or may not have been applied, and nobody
	// is allowed to guess which.
	SendPS(ctx context.Context, interfaceID, body string) (*PA, error)
}

// ErrNotTransmitted marks a failure that provably happened BEFORE any byte reached the PMS — a refused
// connection, a closed socket, a DARK refusal. Only these may be retried automatically.
var ErrNotTransmitted = errors.New("PS was not transmitted")

// ErrTransmittedNoAnswer marks a PS that was written but never conclusively answered. This is UNKNOWN.
var ErrTransmittedNoAnswer = errors.New("PS was transmitted but no PA was conclusively matched")

// NotTransmitted reports whether an error proves nothing reached the PMS.
func NotTransmitted(err error) bool { return errors.Is(err, ErrNotTransmitted) }

// DarkGuard wraps a Transport and makes DARK a property of the code.
//
// It refuses BEFORE delegating, so with the flags OFF the inner transport is never called, no socket is
// touched and no bytes exist. The refusal is classified as NOT transmitted, which is the truthful
// classification — nothing was sent — and it is what keeps a DARK refusal out of the UNKNOWN state.
//
// It also re-checks the record against the connector's outbound allowlist. pmsd.CheckOutbound is the
// Phase-3 read-only chokepoint and it lists PS and PA as forbidden financial records; asking it here means
// a Phase-4 bug cannot smuggle a PS out through a Phase-3 connector even if the flags were somehow ON.
type DarkGuard struct {
	Cfg   Config
	Inner Transport

	mu       sync.Mutex
	refusals int
	lastBody string
}

// NewDarkGuard wraps inner. inner may be nil in DARK deployments: with the flags OFF it is never reached,
// and a nil inner makes that structural rather than merely true.
func NewDarkGuard(cfg Config, inner Transport) *DarkGuard { return &DarkGuard{Cfg: cfg, Inner: inner} }

// SendPS refuses every financial transmission while DARK.
func (d *DarkGuard) SendPS(ctx context.Context, interfaceID, body string) (*PA, error) {
	if d.Cfg.Dark() {
		d.mu.Lock()
		d.refusals++
		d.lastBody = body
		d.mu.Unlock()
		return nil, refusedDark("phase-4 financial transmission is DARK; no PMS bytes may be produced")
	}
	// Not DARK. The record must still be a financial record the caller is entitled to send, and it must
	// still be one this process knows how to build.
	if len(body) < 2 || body[:2] != RecordPS {
		return nil, refusedDark("only a PS record may be sent through the financial transport")
	}
	if !pmsd.IsFinancialRecord(RecordPS) {
		// Defensive: if PS ever stopped being classified as a financial record, the allowlist that keeps
		// Phase-3 read-only would have silently changed meaning. Refuse rather than inherit the change.
		return nil, refusedDark("PS is no longer classified as a financial record; refusing to transmit")
	}
	if d.Inner == nil {
		return nil, refusedDark("no financial transport is configured")
	}
	return d.Inner.SendPS(ctx, interfaceID, body)
}

// Refusals reports how many financial transmissions this guard has refused. It is the positive no-egress
// counter: a test asserts the worker RAN, tried, and was refused — not merely that a table stayed empty.
func (d *DarkGuard) Refusals() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.refusals
}

// LastRefusedBody returns the last PS body that was refused. It is kept so a test can assert the worker
// really did build a complete, valid PS and was stopped at the wire — rather than never getting that far.
func (d *DarkGuard) LastRefusedBody() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastBody
}

func refusedDark(msg string) error {
	return &darkError{e: &Error{Code: ErrDarkNoEgress, Msg: msg}}
}

// darkError is a DARK refusal that is BOTH a typed posting error and an ErrNotTransmitted. Carrying both
// is the point: CodeOf reports dark_no_egress for the operator, and NotTransmitted reports true for the
// worker, so a refusal can never be mistaken for the UNKNOWN state.
type darkError struct{ e *Error }

func (d *darkError) Error() string   { return d.e.Error() }
func (d *darkError) Unwrap() []error { return []error{d.e, ErrNotTransmitted} }

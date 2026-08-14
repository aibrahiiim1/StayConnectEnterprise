package payment

import (
	"context"
	"os"
	"sync"
)

func osGetenv(k string) string { return os.Getenv(k) }

// productionProvider returns the real provider adapter for this build.
//
// It returns nil, and nil is the honest answer: no adapter has been written, none is authorized, and no
// real provider endpoint may be contacted in this milestone.
//
// It deliberately does NOT return a placeholder adapter named "none". An earlier revision did, and the
// consequence was that the runtime wrote provider='none' into the financial record -- inventing a managed
// identity that does not exist, in the one table that must only ever describe real money. DARK means
// configured financial identity with egress disabled; it does not mean fabricated state. With nil here, a
// build with no adapter fails closed at intent creation instead.
//
// A future adapter is added HERE and must satisfy SupportsClientReference() -- an adapter that cannot carry
// and return StayConnect's durable client reference cannot be correlated and must fail closed rather than
// degrade to guessing which money a notification is about.
func productionProvider(cfg Config) (Provider, error) {
	if cfg.ProviderOn() {
		return nil, fail(ErrConfig,
			"provider execution is enabled but this build has no payment provider adapter; refusing to start")
	}
	return nil, nil
}

// providerGuard is the single chokepoint every provider call passes through.
//
// It refuses BEFORE delegating, so while DARK the adapter is never entered, no socket is touched and no
// request exists. It also refuses an adapter that cannot satisfy the correlation capability, because an
// uncorrelatable payment is one whose callbacks could be applied to the wrong money.
type providerGuard struct {
	cfg   Config
	inner Provider

	mu sync.Mutex
	n  int
}

// preflight is the SIDE-EFFECT-FREE half of the guard.
//
// It answers one question -- could a provider request possibly be produced right now? -- by reading only
// configuration and adapter capability. It opens no connection, sends nothing, and changes nothing, here
// or at the provider. That is what makes it safe to run BEFORE the durable execution transition: a refusal
// from preflight is provably NOT_SENT, so nothing needs to be recorded and no state may be left behind.
//
// Running these checks after crossing the durable boundary was a real defect: a DARK build left every
// intent permanently PENDING with its Settlement stuck IN_PROGRESS, describing an execution that had not
// begun and never would.
func (g *providerGuard) preflight() error {
	if g.cfg.Dark() {
		g.mu.Lock()
		g.n++
		g.mu.Unlock()
		return fail(ErrDarkNoEgress, "phase-4 payment execution is DARK; no provider request may be produced")
	}
	if g.inner == nil {
		return fail(ErrConfig, "no payment provider adapter is configured")
	}
	if !g.inner.SupportsClientReference() {
		return fail(ErrConfig,
			"the provider adapter cannot carry and return a client reference; correlation would have to be "+
				"guessed, so execution is refused")
	}
	return nil
}

// execute crosses the real adapter boundary. Everything preflight would refuse has already been refused.
//
// From the first line of this function onward the request MAY have left, so an adapter that fails to answer
// is not an error to be retried -- it IS the UNKNOWN outcome, and the caller must treat it as money that
// may have moved. That rule is unchanged and deliberately not weakened by the preflight split above.
func (g *providerGuard) execute(ctx context.Context, r Request) (Result, error) {
	if err := g.preflight(); err != nil {
		return Result{}, err
	}
	if r.ClientRef == "" {
		return Result{}, fail(ErrUntrustedInput, "no durable client reference; the intent is not executable")
	}
	res, err := g.inner.Execute(ctx, r)
	if err != nil {
		return Result{Outcome: OutcomeUnknown, ReasonCode: "adapter_error"}, nil
	}
	return res, nil
}

func (g *providerGuard) refusals() int { g.mu.Lock(); defer g.mu.Unlock(); return g.n }

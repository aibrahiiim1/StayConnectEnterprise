package payment

import (
	"context"
	"os"
	"sync"
)

func osGetenv(k string) string { return os.Getenv(k) }

// productionProvider returns the real provider adapter for this build.
//
// It returns nil, and that is the honest answer rather than a placeholder: no provider adapter has been
// written, none is authorized, and no real provider endpoint may be contacted in this milestone. A nil
// provider is safe by construction because the guard refuses before it would be reached.
//
// A future adapter is added HERE and must satisfy SupportsClientReference() -- an adapter that cannot carry
// and return StayConnect's durable client reference cannot be correlated and must fail closed rather than
// degrade to guessing which money a notification is about.
func productionProvider(cfg Config) (Provider, error) {
	if cfg.ProviderOn() {
		return nil, fail(ErrConfig,
			"provider execution is enabled but this build has no payment provider adapter; refusing to start")
	}
	return nilProvider{}, nil
}

// nilProvider names the absence of an adapter without being usable as one.
type nilProvider struct{}

func (nilProvider) Name() string                  { return "none" }
func (nilProvider) SupportsClientReference() bool { return false }
func (nilProvider) Execute(context.Context, Request) (Result, error) {
	return Result{}, fail(ErrDarkNoEgress, "no payment provider adapter is configured")
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

func (g *providerGuard) execute(ctx context.Context, r Request) (Result, error) {
	if g.cfg.Dark() {
		g.mu.Lock()
		g.n++
		g.mu.Unlock()
		return Result{}, fail(ErrDarkNoEgress,
			"phase-4 payment execution is DARK; no provider request may be produced")
	}
	if g.inner == nil {
		return Result{}, fail(ErrDarkNoEgress, "no payment provider adapter is configured")
	}
	if !g.inner.SupportsClientReference() {
		return Result{}, fail(ErrConfig,
			"the provider adapter cannot carry and return a client reference; correlation would have to be "+
				"guessed, so execution is refused")
	}
	if r.ClientRef == "" {
		return Result{}, fail(ErrUntrustedInput, "no durable client reference; the intent is not executable")
	}
	// Every error ABOVE is a pre-send refusal: the adapter was never entered, so nothing was sent and the
	// caller may safely abandon. From here on the request may have left, so an adapter that fails to answer
	// is not an error to be retried -- it IS the UNKNOWN outcome, and the caller must treat it as money that
	// may have moved.
	res, err := g.inner.Execute(ctx, r)
	if err != nil {
		return Result{Outcome: OutcomeUnknown, ReasonCode: "adapter_error"}, nil
	}
	return res, nil
}

func (g *providerGuard) refusals() int { g.mu.Lock(); defer g.mu.Unlock(); return g.n }

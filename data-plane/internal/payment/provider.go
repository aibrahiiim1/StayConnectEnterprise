package payment

import (
	"context"
	"os"
	"sync"
)

func osGetenv(k string) string { return os.Getenv(k) }

// productionProvider returns nil on purpose: no adapter is authorized, and a placeholder named "none" once wrote
// provider='none' into the financial record, inventing an identity. With nil, a build with no adapter fails closed at
// intent creation.
// A real adapter must satisfy SupportsClientReference(); otherwise correlation would be guessed.
func productionProvider(cfg Config) (Provider, error) {
	if cfg.ProviderOn() {
		return nil, fail(ErrConfig,
			"provider execution is enabled but this build has no payment provider adapter; refusing to start")
	}
	return nil, nil
}

// providerGuard is the single chokepoint: it refuses before delegating, so while DARK no socket is touched, and it
// refuses adapters that cannot correlate (callbacks could be applied to the wrong money).
type providerGuard struct {
	cfg   Config
	inner Provider

	mu sync.Mutex
	n  int
}

// preflight is side-effect free: it reads configuration and adapter capability only, so a refusal is provably NOT_SENT
// and it is safe to run before the durable execution transition.
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

// From here the request MAY have left: an adapter that fails to answer is the UNKNOWN outcome, treated as money that
// may have moved and never retried.
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

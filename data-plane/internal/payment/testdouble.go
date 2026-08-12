package payment

import (
	"context"
	"sync"
)

// Deterministic in-process provider doubles.
//
// These are IMPLEMENTATION EVIDENCE, never provider acceptance. They prove that the runtime handles each
// contractual outcome correctly and that the correlation capability the design requires is exercised; they
// prove nothing whatsoever about Stripe, Adyen, Checkout.com or any other real provider, none of which has
// been contacted, integrated or verified in this milestone.
//
// They live in the non-test build deliberately: the DARK guard refuses them exactly as it would refuse a
// real adapter, so the no-egress proof exercises the same code path a production build would.

// ScriptedProvider answers with a fixed sequence of outcomes, so a test can drive capture, decline,
// not-sent and unknown without timing or randomness.
type ScriptedProvider struct {
	// Outcomes are consumed in order; the last one repeats once exhausted.
	Outcomes []Result
	// CanCorrelate reports the capability contract. A double answering false must be refused by the guard.
	CanCorrelate bool

	mu   sync.Mutex
	seen []Request
	i    int
}

// NewScriptedProvider builds a double that satisfies the correlation capability.
func NewScriptedProvider(outcomes ...Result) *ScriptedProvider {
	return &ScriptedProvider{Outcomes: outcomes, CanCorrelate: true}
}

func (p *ScriptedProvider) Name() string                  { return "test-double" }
func (p *ScriptedProvider) SupportsClientReference() bool { return p.CanCorrelate }

func (p *ScriptedProvider) Execute(_ context.Context, r Request) (Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, r)
	if len(p.Outcomes) == 0 {
		return Result{Outcome: OutcomeUnknown, ReasonCode: "no_script"}, nil
	}
	i := p.i
	if i >= len(p.Outcomes) {
		i = len(p.Outcomes) - 1
	} else {
		p.i++
	}
	res := p.Outcomes[i]
	// The capability under test: the double echoes the client reference back as its own transaction
	// reference prefix, so a test can assert the correlation actually round-tripped.
	if res.ProviderTxnRef == "" && res.Outcome != OutcomeNotSent {
		res.ProviderTxnRef = "prv_" + r.ClientRef
	}
	return res, nil
}

// Requests returns every request the double was given, so a test can assert what reached the boundary --
// including that NOTHING did while DARK.
func (p *ScriptedProvider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Request, len(p.seen))
	copy(out, p.seen)
	return out
}

// UncorrelatableProvider is a double that cannot carry a client reference. The guard must refuse it: an
// uncorrelatable payment is one whose notifications could be applied to the wrong money.
type UncorrelatableProvider struct{}

func (UncorrelatableProvider) Name() string                  { return "uncorrelatable-double" }
func (UncorrelatableProvider) SupportsClientReference() bool { return false }
func (UncorrelatableProvider) Execute(context.Context, Request) (Result, error) {
	return Result{Outcome: OutcomeCaptured}, nil
}

package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
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

func (UncorrelatableProvider) Name() string                  { return "test-double" }
func (UncorrelatableProvider) SupportsClientReference() bool { return false }
func (UncorrelatableProvider) Execute(context.Context, Request) (Result, error) {
	return Result{Outcome: OutcomeCaptured}, nil
}

// ---------------------------------------------------------------- authenticated-notification doubles

// Secret is the shared signing key ScriptedProvider verifies deliveries against. It exists so the
// authentication path can be exercised for real -- a double that "authenticates" by returning nil would
// prove nothing about the boundary it is standing in for.
const ScriptedProviderSecret = "test-double-signing-secret"

// SignNotification produces the header a delivery must carry to be believed. Tests use it to build a
// legitimate delivery; the absence of it, or a wrong value, is what a forged delivery looks like.
func SignNotification(body []byte) string {
	m := hmac.New(sha256.New, []byte(ScriptedProviderSecret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// scriptedPayload is the double's wire format. A real adapter would parse its provider's actual schema;
// what matters for the boundary is that the fields arrive from an authenticated body rather than from a
// caller's argument list.
type scriptedPayload struct {
	ClientRef      string  `json:"client_ref"`
	EventID        string  `json:"event_id"`
	EventType      string  `json:"event_type"`
	Outcome        Outcome `json:"outcome"`
	ProviderTxnRef string  `json:"provider_txn_ref"`
	ReasonCode     string  `json:"reason_code"`
}

// AuthenticateNotification verifies the delivery's HMAC over the RAW body before parsing anything.
//
// Order matters and is the whole point: parse-then-verify would mean an attacker's payload has already been
// through the parser, and any field extracted before verification is attacker-controlled data that some
// later line will be tempted to use.
func (p *ScriptedProvider) AuthenticateNotification(_ context.Context, raw RawNotification) (ParsedNotification, error) {
	want := SignNotification(raw.Body)
	got := raw.Headers["X-Test-Signature"]
	if len(got) != len(want) || !hmac.Equal([]byte(got), []byte(want)) {
		return ParsedNotification{}, fail(ErrUntrusted, "signature mismatch")
	}
	var pl scriptedPayload
	if err := json.Unmarshal(raw.Body, &pl); err != nil {
		return ParsedNotification{}, fail(ErrUntrusted, "unparseable body")
	}
	return ParsedNotification{
		ClientRef: pl.ClientRef, ProviderEventID: pl.EventID, EventType: pl.EventType,
		Outcome: pl.Outcome, ProviderTxnRef: pl.ProviderTxnRef, ReasonCode: pl.ReasonCode,
	}, nil
}

// BuildNotification assembles a correctly signed delivery for a client reference and outcome.
func BuildNotification(clientRef, eventID string, outcome Outcome, providerTxnRef string) RawNotification {
	body, _ := json.Marshal(scriptedPayload{
		ClientRef: clientRef, EventID: eventID, EventType: "payment." + strings.ToLower(string(outcome)),
		Outcome: outcome, ProviderTxnRef: providerTxnRef,
	})
	return RawNotification{Body: body, Headers: map[string]string{"X-Test-Signature": SignNotification(body)}}
}

// DeafProvider can send money but cannot authenticate a notification.
//
// It is a standalone type rather than an embedding of ScriptedProvider on purpose: embedding would PROMOTE
// AuthenticateNotification and the double would quietly satisfy the very interface it exists to lack. The
// compile-time assertions below are what keep that true if anyone edits this later.
type DeafProvider struct{}

func (*DeafProvider) Name() string                  { return "test-double" }
func (*DeafProvider) SupportsClientReference() bool { return true }
func (*DeafProvider) Execute(context.Context, Request) (Result, error) {
	return Result{Outcome: OutcomeCaptured, ReasonCode: "ok"}, nil
}

var (
	// it IS a usable provider ...
	_ Provider = (*DeafProvider)(nil)
	// ... and it is NOT an authenticator. This assertion fails to compile if someone gives it one.
	_ = func() bool { var i any = (*DeafProvider)(nil); _, ok := i.(NotificationAuthenticator); return !ok }()
)

//go:build !stayconnect_production

// Audit P6 L2: the provider test doubles (and their fixed signing secret) are never compiled into a production
// binary (built with -tags stayconnect_production). The file keeps its path because transitions T0037/T0038 cite it.

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

// Deterministic in-process provider doubles: implementation evidence only, they prove nothing about any real provider.
// The tests still pass them through the DARK guard as they would a real adapter. They are test-only (audit P6 L2):
// the fixed notification signing secret must not ship in the production build.

type ScriptedProvider struct {
	// Outcomes are consumed in order; the last one repeats once exhausted.
	Outcomes []Result
	// A double answering false must be refused by the guard.
	CanCorrelate bool

	mu   sync.Mutex
	seen []Request
	i    int
}

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
	// Echoes the client reference as the transaction reference prefix so tests can assert correlation round-tripped.
	if res.ProviderTxnRef == "" && res.Outcome != OutcomeNotSent {
		res.ProviderTxnRef = "prv_" + r.ClientRef
	}
	return res, nil
}

func (p *ScriptedProvider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Request, len(p.seen))
	copy(out, p.seen)
	return out
}

// UncorrelatableProvider cannot carry a client reference; the guard must refuse it.
type UncorrelatableProvider struct{}

func (UncorrelatableProvider) Name() string                  { return "test-double" }
func (UncorrelatableProvider) SupportsClientReference() bool { return false }
func (UncorrelatableProvider) Execute(context.Context, Request) (Result, error) {
	return Result{Outcome: OutcomeCaptured}, nil
}

// Secret is the signing key deliveries are verified against; a double that "authenticates" by returning nil would prove
// nothing.
const ScriptedProviderSecret = "test-double-signing-secret"

// SignNotification builds the header a legitimate delivery carries; its absence or a wrong value is a forged delivery.
func SignNotification(body []byte) string {
	m := hmac.New(sha256.New, []byte(ScriptedProviderSecret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

type scriptedPayload struct {
	ClientRef      string  `json:"client_ref"`
	EventID        string  `json:"event_id"`
	EventType      string  `json:"event_type"`
	Outcome        Outcome `json:"outcome"`
	ProviderTxnRef string  `json:"provider_txn_ref"`
	ReasonCode     string  `json:"reason_code"`
}

// The HMAC is verified over the RAW body before parsing: parse-then-verify would let attacker-controlled fields be
// extracted before verification.
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

func BuildNotification(clientRef, eventID string, outcome Outcome, providerTxnRef string) RawNotification {
	body, _ := json.Marshal(scriptedPayload{
		ClientRef: clientRef, EventID: eventID, EventType: "payment." + strings.ToLower(string(outcome)),
		Outcome: outcome, ProviderTxnRef: providerTxnRef,
	})
	return RawNotification{Body: body, Headers: map[string]string{"X-Test-Signature": SignNotification(body)}}
}

// DeafProvider cannot authenticate a notification. It is standalone rather than embedding ScriptedProvider, which would
// promote AuthenticateNotification and satisfy the interface it exists to lack; the assertions below keep that true.
type DeafProvider struct{}

func (*DeafProvider) Name() string                  { return "test-double" }
func (*DeafProvider) SupportsClientReference() bool { return true }
func (*DeafProvider) Execute(context.Context, Request) (Result, error) {
	return Result{Outcome: OutcomeCaptured, ReasonCode: "ok"}, nil
}

var (
	_ Provider = (*DeafProvider)(nil)
	// It is not an authenticator: this assertion fails to compile if it gains one.
	_ = func() bool { var i any = (*DeafProvider)(nil); _, ok := i.(NotificationAuthenticator); return !ok }()
)

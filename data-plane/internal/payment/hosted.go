package payment

import (
	"context"
	"errors"
	"time"
)

// ---------------------------------------------------------------- hosted checkout (Card payment)
//
// Card payment in OneGate is ALWAYS a provider-hosted page. The client enters card data only on the
// provider's page; no card data ever reaches the appliance. The money moves when the client pays there,
// not when OneGate creates the checkout, so creating a checkout is not an Execute: it is a session that
// the client may or may not complete.
//
// Authority: a browser redirect back to the portal proves nothing. The ONLY financial proof is the
// provider's own answer to an authenticated, outbound status query (QueryStatus), applied through the
// outcome authority exactly like any other provider outcome. Status queries are read-only and may be
// repeated safely; checkout creation is never repeated for a settlement (one live charge per settlement
// is a database invariant), and an ambiguous creation is resolved by querying, not by creating again.

// Credentials are one provider account's decrypted secrets, handed to an adapter per call and never
// stored by it. Keys are adapter-defined (e.g. "secret_key", "public_key", "integration_id").
type Credentials map[string]string

// Mode is the provider account mode. LIVE moves real money and is refused by the deployment ceiling
// unless separately authorised; TEST uses the provider's sandbox.
type Mode string

const (
	ModeTest Mode = "TEST"
	ModeLive Mode = "LIVE"
)

// CheckoutRequest is what an adapter needs to create one hosted checkout.
type CheckoutRequest struct {
	ClientRef       string // OneGate's durable reference; the adapter MUST send it as the provider's idempotency / merchant reference
	MerchantAccount string
	Mode            Mode
	AmountMinor     int64
	Currency        string
	Exponent        int16
	Description     string
	ReturnURL       string
	CancelURL       string
	ExpiresAt       time.Time
	Credentials     Credentials
}

// Checkout is a created hosted checkout.
type Checkout struct {
	ProviderSessionRef string // the provider's id for this checkout (Stripe cs_…, Paymob intention id)
	RedirectURL        string // where the client's browser is sent
	ExpiresAt          time.Time
}

// ErrCheckoutNotCreated is returned (wrapped) only when the adapter can PROVE no checkout exists at the
// provider (e.g. a 4xx validation refusal, or no connection was ever made). Any other error is ambiguous:
// the checkout may exist, and the engine resolves it by QueryStatus on the ClientRef.
var ErrCheckoutNotCreated = errors.New("checkout provably not created")

// CheckoutState is the provider's authoritative view of one checkout.
type CheckoutState string

const (
	CheckoutOpen     CheckoutState = "OPEN"      // not paid yet, still payable
	CheckoutCaptured CheckoutState = "CAPTURED"  // paid and captured
	CheckoutDeclined CheckoutState = "DECLINED"  // a conclusive failure
	CheckoutExpired  CheckoutState = "EXPIRED"   // expired or cancelled without payment
	CheckoutNotFound CheckoutState = "NOT_FOUND" // the provider has no checkout for this reference
)

// ProviderEvent is provider-originated financial evidence on a captured charge that OneGate records but
// never initiates: a refund the merchant made in the provider dashboard, or a chargeback.
type ProviderEvent struct {
	EventID     string // the provider's own id: the deduplication key
	Kind        string // REFUND | CHARGEBACK
	AmountMinor int64
	ProviderRef string
	// Cumulative is true when AmountMinor is the provider's running TOTAL of this kind on the charge rather
	// than one event's amount (Paymob reports only a cumulative refunded amount). The engine then records
	// only the increase over what is already recorded, so a partial refund is never counted twice.
	Cumulative bool
}

// StatusQuery identifies one checkout.
type StatusQuery struct {
	ClientRef          string
	ProviderSessionRef string // may be empty when creation was ambiguous
	MerchantAccount    string
	Mode               Mode
	Credentials        Credentials
}

// StatusResult is the provider's answer to a status query.
type StatusResult struct {
	State          CheckoutState
	ProviderTxnRef string // required when CAPTURED
	AmountMinor    int64  // what the provider says was captured; must equal the pinned amount
	Currency       string
	ReasonCode     string
	Events         []ProviderEvent
}

// HostedCheckoutProvider is the capability a Card payment adapter must have.
type HostedCheckoutProvider interface {
	Name() string
	// CreateCheckout creates one hosted checkout. It is not a charge.
	CreateCheckout(ctx context.Context, r CheckoutRequest) (Checkout, error)
	// QueryStatus asks the provider, outbound and authenticated, what happened. Read-only; safe to repeat.
	QueryStatus(ctx context.Context, q StatusQuery) (StatusResult, error)
	// RequiredHostedDomains are the exact domains the client must reach before sign-in to complete
	// payment on the provider's page. Least privilege: nothing broader.
	RequiredHostedDomains() []string
	// CredentialKeys names the secrets this adapter needs; the Admin Console renders exactly these.
	CredentialKeys() []CredentialKey
	// TestConnection performs a non-financial authenticated call to validate credentials.
	TestConnection(ctx context.Context, mode Mode, merchantAccount string, c Credentials) error
}

// CredentialKey describes one secret an adapter needs.
type CredentialKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"` // write-only; never returned by any API
	Required bool   `json:"required"`
}

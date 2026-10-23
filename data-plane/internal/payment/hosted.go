package payment

import (
	"context"
	"errors"
	"time"
)

// Card payment is always a provider-hosted page; no card data reaches the appliance. A browser redirect proves nothing:
// the only financial proof is the provider's answer to an authenticated outbound QueryStatus, applied through the
// outcome authority.
// Checkout creation is never repeated for a settlement; an ambiguous creation is resolved by querying.

// Credentials are one account's decrypted secrets, passed per call and never stored by the adapter.
type Credentials map[string]string

// LIVE moves real money and is refused by the deployment ceiling unless separately authorised.
type Mode string

const (
	ModeTest Mode = "TEST"
	ModeLive Mode = "LIVE"
)

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

type Checkout struct {
	ProviderSessionRef string
	RedirectURL        string
	ExpiresAt          time.Time
}

// ErrCheckoutNotCreated is returned only when the adapter can PROVE no checkout exists (4xx refusal, no connection
// made); any other error is ambiguous and resolved by QueryStatus.
var ErrCheckoutNotCreated = errors.New("checkout provably not created")

type CheckoutState string

const (
	CheckoutOpen     CheckoutState = "OPEN"
	CheckoutCaptured CheckoutState = "CAPTURED"
	CheckoutDeclined CheckoutState = "DECLINED"
	CheckoutExpired  CheckoutState = "EXPIRED"
	CheckoutNotFound CheckoutState = "NOT_FOUND"
)

// ProviderEvent is provider-originated evidence (a dashboard refund, a chargeback) that OneGate records but never
// initiates.
type ProviderEvent struct {
	EventID     string
	Kind        string // REFUND | CHARGEBACK (money returned) | DISPUTE (opened or decided for the merchant: no money moved; review only)
	AmountMinor int64
	ProviderRef string
	// Cumulative: AmountMinor is the provider's running total of this kind (Paymob reports only a cumulative refund);
	// the engine records only the increase.
	Cumulative bool
}

type StatusQuery struct {
	ClientRef          string
	ProviderSessionRef string
	MerchantAccount    string
	Mode               Mode
	Credentials        Credentials
}

type StatusResult struct {
	State          CheckoutState
	ProviderTxnRef string
	AmountMinor    int64
	Currency       string
	ReasonCode     string
	Events         []ProviderEvent
	// Settlement is the provider's own report for a CAPTURED charge, recorded as stated and never recomputed.
	Settlement *SettlementReport
}

// ExchangeRate is the provider's decimal string exactly as sent, never derived.
type SettlementReport struct {
	Currency     string
	AmountMinor  int64
	ExchangeRate string
	FeeMinor     int64
	NetMinor     int64
	BalanceRef   string
}

// CurrencyConverter: a provider that presents in one currency and settles in the merchant's (Stripe yes, Paymob no);
// without it only packages in the account's own currency are offered.
type CurrencyConverter interface {
	ConvertsPresentment(presentment, settlement string) bool
}

type HostedCheckoutProvider interface {
	Name() string
	CreateCheckout(ctx context.Context, r CheckoutRequest) (Checkout, error)
	// QueryStatus is outbound, authenticated, read-only and safe to repeat.
	QueryStatus(ctx context.Context, q StatusQuery) (StatusResult, error)
	// RequiredHostedDomains: exact domains the client must reach before sign-in; nothing broader.
	RequiredHostedDomains() []string
	CredentialKeys() []CredentialKey
	TestConnection(ctx context.Context, mode Mode, merchantAccount string, c Credentials) error
}

type CredentialKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
}

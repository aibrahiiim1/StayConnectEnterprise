// Package stripe is the Stripe Checkout adapter for OneGate's hosted Card payment
// (payment.HostedCheckoutProvider).
//
// Card data never reaches OneGate: the client pays on checkout.stripe.com. The adapter only creates a
// Checkout Session and later asks Stripe, outbound and authenticated, what happened to it.
//
// Provider-API assumptions (Stripe API reference, docs.stripe.com/api, checked 2026-09-28):
//   - POST /v1/checkout/sessions (form-encoded) with an Idempotency-Key header creates one session;
//     the same key replayed returns the original result instead of a second session.
//   - GET /v1/checkout/sessions/{id} supports expand[]=payment_intent and
//     expand[]=payment_intent.latest_charge.
//   - GET /v1/checkout/sessions (list) is newest-first and supports created[gte] and starting_after.
//   - GET /v1/payment_intents/search supports metadata['key']:'value' queries (eventually consistent,
//     so it is used only as a supplementary lookup).
//   - GET /v1/refunds?charge= and GET /v1/disputes?charge= list a charge's refunds and disputes.
//   - Stripe-Version is pinned (apiVersion) so response shapes do not drift with the account default.
//
// Money never moves as a result of anything this package sends: it creates sessions and reads. There is
// no refund-creating, capture or cancel call anywhere in it.
package stripe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

const (
	defaultBaseURL = "https://api.stripe.com"
	defaultTimeout = 20 * time.Second
	// apiVersion pins the response shapes this adapter parses (latest_charge on PaymentIntent, refunds
	// not auto-expanded on Charge).
	apiVersion = "2024-06-20"

	// Stripe requires expires_at to be between 30 minutes and 24 hours after creation. The adapter
	// clamps into [now+minExpiry, now+maxExpiry]; the margins absorb clock skew and request latency so a
	// requested boundary value is never refused by Stripe.
	minExpiry = 31 * time.Minute
	maxExpiry = 23*time.Hour + 55*time.Minute

	// Ambiguous-create resolution lists sessions newest-first back to this age, at most maxListPages.
	resolveWindow = 30 * 24 * time.Hour
	maxListPages  = 50

	metaKey = "onegate_client_ref"
)

var clientRefRE = regexp.MustCompile(`^sc_[0-9a-f]{32}$`)

// requiredHostedDomains is the least-privilege set the client must reach to finish payment on Stripe's
// hosted page. Source: the Content-Security-Policy header served by checkout.stripe.com itself
// (observed 2026-09-28: connect-src api.stripe.com js.stripe.com; script/style/font-src js.stripe.com;
// frame-src js.stripe.com payments.stripe.com b.stripecdn.com) and Stripe's security guide
// (docs.stripe.com/security/guide: hooks.stripe.com is the 3-D Secure / redirect frame, m.stripe.network
// is Stripe.js's fraud-signal frame). Telemetry, error-reporting, image-CDN and Link domains are
// deliberately excluded: the page completes without them. Issuer 3-D Secure ACS pages are hosted by the
// card issuer and cannot be enumerated here.
var requiredHostedDomains = []string{
	"checkout.stripe.com",
	"js.stripe.com",
	"api.stripe.com",
	"payments.stripe.com",
	"hooks.stripe.com",
	"m.stripe.network",
	"b.stripecdn.com",
}

// Adapter is a Stripe Checkout hosted-payment adapter. It holds no credentials.
type Adapter struct {
	client  *http.Client
	baseURL string
	timeout time.Duration
	now     func() time.Time
}

var _ payment.HostedCheckoutProvider = (*Adapter)(nil)

// Option configures an Adapter.
type Option func(*Adapter)

// WithHTTPClient sets the HTTP client used for every call.
func WithHTTPClient(c *http.Client) Option { return func(a *Adapter) { a.client = c } }

// WithBaseURL overrides https://api.stripe.com (tests use an httptest server).
func WithBaseURL(u string) Option { return func(a *Adapter) { a.baseURL = strings.TrimRight(u, "/") } }

// WithTimeout bounds every individual provider call.
func WithTimeout(d time.Duration) Option { return func(a *Adapter) { a.timeout = d } }

// WithClock overrides the clock (tests).
func WithClock(now func() time.Time) Option { return func(a *Adapter) { a.now = now } }

// New builds an Adapter.
func New(opts ...Option) *Adapter {
	a := &Adapter{client: &http.Client{}, baseURL: defaultBaseURL, timeout: defaultTimeout, now: time.Now}
	for _, o := range opts {
		o(a)
	}
	if a.client == nil {
		a.client = &http.Client{}
	}
	if a.timeout <= 0 {
		a.timeout = defaultTimeout
	}
	return a
}

// Name is stored as the provider name.
func (a *Adapter) Name() string { return "stripe" }

// RequiredHostedDomains returns a copy of the hosted-page domain allowlist.
func (a *Adapter) RequiredHostedDomains() []string {
	return append([]string(nil), requiredHostedDomains...)
}

// CredentialKeys names the one secret this adapter needs.
func (a *Adapter) CredentialKeys() []payment.CredentialKey {
	return []payment.CredentialKey{
		{Key: "secret_key", Label: "Secret key (sk_test_… / sk_live_…, or a restricted rk_ key)", Secret: true, Required: true},
	}
}

// ---------------------------------------------------------------- errors (never carry secrets)

func notCreated(format string, args ...any) error {
	return fmt.Errorf("stripe: "+format+": %w", append(args, payment.ErrCheckoutNotCreated)...)
}

func errf(format string, args ...any) error { return fmt.Errorf("stripe: "+format, args...) }

// ---------------------------------------------------------------- credentials and mode

// keyFor returns the secret key after checking that its mode matches the requested mode. Stripe keys
// carry their mode in the prefix: sk_test_/rk_test_ are sandbox, sk_live_/rk_live_ move real money.
func keyFor(mode payment.Mode, c payment.Credentials) (string, error) {
	key := strings.TrimSpace(c["secret_key"])
	if key == "" {
		return "", errors.New("stripe: secret_key is not configured")
	}
	var keyMode payment.Mode
	switch {
	case strings.HasPrefix(key, "sk_test_"), strings.HasPrefix(key, "rk_test_"):
		keyMode = payment.ModeTest
	case strings.HasPrefix(key, "sk_live_"), strings.HasPrefix(key, "rk_live_"):
		keyMode = payment.ModeLive
	default:
		return "", errors.New("stripe: secret_key is not a Stripe secret or restricted key")
	}
	if mode != payment.ModeTest && mode != payment.ModeLive {
		return "", errors.New("stripe: account mode is neither TEST nor LIVE")
	}
	if keyMode != mode {
		return "", fmt.Errorf("stripe: a %s-mode key is configured for a %s-mode account; refused", keyMode, mode)
	}
	return key, nil
}

// ---------------------------------------------------------------- currency

// Stripe's minor-unit rules (docs.stripe.com/currencies). Every currency not listed is two-decimal.
var zeroDecimal = map[string]bool{
	"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true, "kmf": true, "krw": true, "mga": true,
	"pyg": true, "rwf": true, "vnd": true, "vuv": true, "xaf": true, "xof": true, "xpf": true,
}

// threeDecimal currencies: Stripe requires the last digit of the amount to be 0.
var threeDecimal = map[string]bool{"bhd": true, "jod": true, "kwd": true, "omr": true, "tnd": true}

// hundredsOnly currencies are zero-decimal in practice but Stripe represents them with two decimals
// whose fractional part must be 00 (ISK, UGX special cases).
var hundredsOnly = map[string]bool{"isk": true, "ugx": true}

var currencyRE = regexp.MustCompile(`^[a-z]{3}$`)

// checkAmount verifies that OneGate's exponent equals Stripe's exponent for the currency, so AmountMinor
// is already in Stripe's unit. A mismatch is refused rather than converted: silently rescaling money is
// how a 10.00 charge becomes 1000.00.
func checkAmount(cur string, exp int16, amount int64) error {
	if !currencyRE.MatchString(cur) {
		return notCreated("currency is not a three-letter ISO code")
	}
	if amount <= 0 {
		return notCreated("amount must be positive")
	}
	want := int16(2)
	switch {
	case zeroDecimal[cur]:
		want = 0
	case threeDecimal[cur]:
		want = 3
	}
	if exp != want {
		return notCreated("currency %s has exponent %d at Stripe, pinned exponent is %d; refused", strings.ToUpper(cur), want, exp)
	}
	if threeDecimal[cur] && amount%10 != 0 {
		return notCreated("Stripe requires %s amounts to end in 0", strings.ToUpper(cur))
	}
	if hundredsOnly[cur] && amount%100 != 0 {
		return notCreated("Stripe cannot charge fractions of %s", strings.ToUpper(cur))
	}
	return nil
}

// ---------------------------------------------------------------- transport

type response struct {
	status int
	body   []byte
}

// provablyNotSent reports whether a transport error proves the request never reached Stripe: name
// resolution failed, the TCP dial failed, or the TLS handshake was rejected. A failure after the
// connection is up (write, read, timeout mid-request) is ambiguous.
func provablyNotSent(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var (
		certErr     *tls.CertificateVerificationError
		unknownAuth x509.UnknownAuthorityError
		hostErr     x509.HostnameError
		invalidErr  x509.CertificateInvalidError
		recErr      tls.RecordHeaderError
	)
	return errors.As(err, &certErr) || errors.As(err, &unknownAuth) || errors.As(err, &hostErr) ||
		errors.As(err, &invalidErr) || errors.As(err, &recErr)
}

// errKind is a secret-free classification of a transport error.
func errKind(err error) string {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.As(err, &opErr):
		return "network " + opErr.Op
	case provablyNotSent(err):
		return "tls"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "transport"
}

// transportError wraps a failed call. notSent is true only when the request provably never left.
type transportError struct {
	kind    string
	notSent bool
}

func (e *transportError) Error() string { return "stripe: transport failure (" + e.kind + ")" }

func (a *Adapter) call(ctx context.Context, method, path string, form url.Values, key, idemKey string) (response, error) {
	if err := ctx.Err(); err != nil {
		return response{}, &transportError{kind: errKind(err), notSent: true}
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	var body io.Reader
	u := a.baseURL + path
	if method == http.MethodPost {
		body = strings.NewReader(form.Encode())
	} else if len(form) > 0 {
		u += "?" + form.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return response{}, &transportError{kind: "request", notSent: true}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Stripe-Version", apiVersion)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return response{}, &transportError{kind: errKind(err), notSent: provablyNotSent(err)}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return response{status: resp.StatusCode}, &transportError{kind: "read " + errKind(err)}
	}
	return response{status: resp.StatusCode, body: b}, nil
}

var safeTokenRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// providerCode extracts Stripe's error type/code identifiers only. Stripe's human message can quote a
// masked key, so it is never copied into an error.
func providerCode(b []byte) string {
	var e struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) != nil {
		return "unparsed"
	}
	parts := []string{}
	for _, s := range []string{e.Error.Type, e.Error.Code} {
		if safeTokenRE.MatchString(s) {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "unspecified"
	}
	return strings.Join(parts, "/")
}

// refusedWithoutObject lists the HTTP statuses at which Stripe refuses a create before any object exists:
// malformed request, authentication, permission, missing resource, rate limit. 409 (idempotency
// conflict: another request with this key) and every 5xx are ambiguous.
func refusedWithoutObject(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusNotFound, http.StatusTooManyRequests:
		return true
	}
	return false
}

// ---------------------------------------------------------------- create

type sessionCreated struct {
	ID                string `json:"id"`
	Object            string `json:"object"`
	URL               string `json:"url"`
	ExpiresAt         int64  `json:"expires_at"`
	ClientReferenceID string `json:"client_reference_id"`
}

// CreateCheckout creates one Stripe Checkout Session in payment mode for a single line item. The
// ClientRef is the Idempotency-Key, client_reference_id and metadata on both the session and its
// PaymentIntent, so an ambiguous create is resolved by QueryStatus without creating a second session.
func (a *Adapter) CreateCheckout(ctx context.Context, r payment.CheckoutRequest) (payment.Checkout, error) {
	if !clientRefRE.MatchString(r.ClientRef) {
		return payment.Checkout{}, notCreated("client reference is not a OneGate reference")
	}
	key, err := keyFor(r.Mode, r.Credentials)
	if err != nil {
		return payment.Checkout{}, fmt.Errorf("%w: %w", err, payment.ErrCheckoutNotCreated)
	}
	cur := strings.ToLower(strings.TrimSpace(r.Currency))
	if err := checkAmount(cur, r.Exponent, r.AmountMinor); err != nil {
		return payment.Checkout{}, err
	}
	if !isHTTPURL(r.ReturnURL) {
		return payment.Checkout{}, notCreated("return URL is not an absolute http(s) URL")
	}
	if r.CancelURL != "" && !isHTTPURL(r.CancelURL) {
		return payment.Checkout{}, notCreated("cancel URL is not an absolute http(s) URL")
	}
	name := strings.TrimSpace(r.Description)
	if name == "" {
		name = "Internet access"
	}

	now := a.now()
	exp := r.ExpiresAt
	if exp.IsZero() || exp.Before(now.Add(minExpiry)) {
		exp = now.Add(minExpiry)
	}
	if exp.After(now.Add(maxExpiry)) {
		exp = now.Add(maxExpiry)
	}

	f := url.Values{}
	f.Set("mode", "payment")
	f.Set("payment_method_types[0]", "card")
	f.Set("line_items[0][price_data][currency]", cur)
	f.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(r.AmountMinor, 10))
	f.Set("line_items[0][price_data][product_data][name]", name)
	f.Set("line_items[0][quantity]", "1")
	f.Set("client_reference_id", r.ClientRef)
	f.Set("metadata["+metaKey+"]", r.ClientRef)
	f.Set("payment_intent_data[metadata]["+metaKey+"]", r.ClientRef)
	f.Set("payment_intent_data[capture_method]", "automatic")
	f.Set("success_url", r.ReturnURL)
	if r.CancelURL != "" {
		f.Set("cancel_url", r.CancelURL)
	}
	f.Set("expires_at", strconv.FormatInt(exp.Unix(), 10))

	resp, err := a.call(ctx, http.MethodPost, "/v1/checkout/sessions", f, key, r.ClientRef)
	if err != nil {
		var te *transportError
		if errors.As(err, &te) && te.notSent {
			return payment.Checkout{}, notCreated("request never reached Stripe (%s)", te.kind)
		}
		return payment.Checkout{}, errf("create checkout outcome unknown: %v", err)
	}
	if resp.status < 200 || resp.status > 299 {
		if refusedWithoutObject(resp.status) {
			return payment.Checkout{}, notCreated("create checkout refused (HTTP %d, %s)", resp.status, providerCode(resp.body))
		}
		return payment.Checkout{}, errf("create checkout outcome unknown (HTTP %d, %s)", resp.status, providerCode(resp.body))
	}
	var s sessionCreated
	if err := json.Unmarshal(resp.body, &s); err != nil || s.ID == "" || s.URL == "" {
		return payment.Checkout{}, errf("create checkout outcome unknown: malformed response")
	}
	if s.ClientReferenceID != "" && s.ClientReferenceID != r.ClientRef {
		return payment.Checkout{}, errf("create checkout returned a session for a different reference")
	}
	out := payment.Checkout{ProviderSessionRef: s.ID, RedirectURL: s.URL, ExpiresAt: exp}
	if s.ExpiresAt > 0 {
		out.ExpiresAt = time.Unix(s.ExpiresAt, 0).UTC()
	}
	return out, nil
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// ---------------------------------------------------------------- status

type charge struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Paid           bool   `json:"paid"`
	Captured       bool   `json:"captured"`
	AmountCaptured int64  `json:"amount_captured"`
	AmountRefunded int64  `json:"amount_refunded"`
	Disputed       bool   `json:"disputed"`
}

type paymentIntent struct {
	ID               string            `json:"id"`
	Status           string            `json:"status"`
	Amount           int64             `json:"amount"`
	AmountReceived   int64             `json:"amount_received"`
	Currency         string            `json:"currency"`
	Metadata         map[string]string `json:"metadata"`
	LatestChargeRaw  json.RawMessage   `json:"latest_charge"`
	LastPaymentError *struct {
		Code        string `json:"code"`
		DeclineCode string `json:"decline_code"`
	} `json:"last_payment_error"`
}

type session struct {
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	PaymentStatus     string            `json:"payment_status"`
	ClientReferenceID string            `json:"client_reference_id"`
	Metadata          map[string]string `json:"metadata"`
	Created           int64             `json:"created"`
	PaymentIntentRaw  json.RawMessage   `json:"payment_intent"`
}

// expandable decodes a Stripe expandable field that is an object when expanded. It returns false for
// null, an unexpanded id string, or an absent field.
func expandable(raw json.RawMessage, dst any) (bool, error) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" || strings.HasPrefix(t, `"`) {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

// QueryStatus asks Stripe what happened to the checkout. It only issues GET requests and is safe to
// repeat.
func (a *Adapter) QueryStatus(ctx context.Context, q payment.StatusQuery) (payment.StatusResult, error) {
	if !clientRefRE.MatchString(q.ClientRef) {
		return payment.StatusResult{}, errf("client reference is not a OneGate reference")
	}
	key, err := keyFor(q.Mode, q.Credentials)
	if err != nil {
		return payment.StatusResult{}, err
	}
	sid := strings.TrimSpace(q.ProviderSessionRef)
	if sid == "" {
		found, id, err := a.resolveByClientRef(ctx, key, q.ClientRef)
		if err != nil {
			return payment.StatusResult{}, err
		}
		if !found {
			return payment.StatusResult{State: payment.CheckoutNotFound}, nil
		}
		sid = id
	}
	if strings.ContainsAny(sid, "/?#") {
		return payment.StatusResult{}, errf("provider session reference is malformed")
	}

	f := url.Values{}
	f.Add("expand[]", "payment_intent")
	f.Add("expand[]", "payment_intent.latest_charge")
	resp, err := a.call(ctx, http.MethodGet, "/v1/checkout/sessions/"+url.PathEscape(sid), f, key, "")
	if err != nil {
		return payment.StatusResult{}, errf("status query failed: %v", err)
	}
	if resp.status == http.StatusNotFound {
		return payment.StatusResult{State: payment.CheckoutNotFound}, nil
	}
	if resp.status != http.StatusOK {
		return payment.StatusResult{}, errf("status query failed (HTTP %d, %s)", resp.status, providerCode(resp.body))
	}
	var s session
	if err := json.Unmarshal(resp.body, &s); err != nil || s.ID == "" {
		return payment.StatusResult{}, errf("status query returned a malformed session")
	}
	if s.ClientReferenceID != q.ClientRef {
		return payment.StatusResult{}, errf("session does not belong to this client reference")
	}
	var pi paymentIntent
	hasPI, err := expandable(s.PaymentIntentRaw, &pi)
	if err != nil {
		return payment.StatusResult{}, errf("status query returned a malformed payment intent")
	}
	if hasPI && pi.Metadata[metaKey] != "" && pi.Metadata[metaKey] != q.ClientRef {
		return payment.StatusResult{}, errf("payment intent does not belong to this client reference")
	}
	return a.mapSession(ctx, key, s, hasPI, pi)
}

func (a *Adapter) mapSession(ctx context.Context, key string, s session, hasPI bool, pi paymentIntent) (payment.StatusResult, error) {
	switch s.Status {
	case "complete":
		if s.PaymentStatus != "paid" {
			// Only asynchronous payment methods complete unpaid; card-only sessions should not. Not a
			// conclusive outcome either way.
			return payment.StatusResult{State: payment.CheckoutOpen, ReasonCode: "payment_" + safeCode(s.PaymentStatus)}, nil
		}
		if !hasPI {
			return payment.StatusResult{}, errf("completed session has no payment intent")
		}
		if pi.Status != "succeeded" {
			return payment.StatusResult{State: payment.CheckoutOpen, ReasonCode: "intent_" + safeCode(pi.Status)}, nil
		}
		var ch charge
		hasCh, err := expandable(pi.LatestChargeRaw, &ch)
		if err != nil || !hasCh {
			return payment.StatusResult{}, errf("succeeded payment intent has no readable charge")
		}
		if !ch.Captured || ch.Status != "succeeded" {
			return payment.StatusResult{State: payment.CheckoutOpen, ReasonCode: "charge_not_captured"}, nil
		}
		res := payment.StatusResult{
			State:          payment.CheckoutCaptured,
			ProviderTxnRef: pi.ID,
			AmountMinor:    pi.AmountReceived,
			Currency:       strings.ToUpper(pi.Currency),
		}
		evs, err := a.events(ctx, key, ch)
		if err != nil {
			return payment.StatusResult{}, err
		}
		res.Events = evs
		return res, nil
	case "expired":
		return payment.StatusResult{State: payment.CheckoutExpired, ReasonCode: "session_expired"}, nil
	case "open":
		if hasPI {
			switch pi.Status {
			case "canceled":
				return payment.StatusResult{State: payment.CheckoutExpired, ReasonCode: "intent_canceled"}, nil
			case "succeeded":
				// The charge succeeded but the session has not flipped to complete yet: not conclusive
				// until Stripe reports the session complete.
				return payment.StatusResult{State: payment.CheckoutOpen, ReasonCode: "session_completing"}, nil
			case "requires_payment_method":
				// A failed attempt; the client may retry on the hosted page until it expires.
				rc := "requires_payment_method"
				if pi.LastPaymentError != nil {
					if c := safeCode(pi.LastPaymentError.DeclineCode); c != "unknown" {
						rc = c
					} else if c := safeCode(pi.LastPaymentError.Code); c != "unknown" {
						rc = c
					}
				}
				return payment.StatusResult{State: payment.CheckoutOpen, ReasonCode: rc}, nil
			}
		}
		return payment.StatusResult{State: payment.CheckoutOpen}, nil
	}
	return payment.StatusResult{}, errf("session has an unrecognised status")
}

func safeCode(s string) string {
	if safeTokenRE.MatchString(s) {
		return s
	}
	return "unknown"
}

type listResp struct {
	Data    []json.RawMessage `json:"data"`
	HasMore bool              `json:"has_more"`
}

// events reports provider-originated refunds and chargebacks on a captured charge. Only succeeded
// refunds are reported (a pending refund can still fail and will be reported when it succeeds).
// Disputes in a warning_* status are inquiries that withdraw no funds and are not reported.
func (a *Adapter) events(ctx context.Context, key string, ch charge) ([]payment.ProviderEvent, error) {
	var out []payment.ProviderEvent
	if ch.AmountRefunded > 0 {
		err := a.listAll(ctx, key, "/v1/refunds", url.Values{"charge": {ch.ID}}, func(raw json.RawMessage) error {
			var r struct {
				ID     string `json:"id"`
				Amount int64  `json:"amount"`
				Status string `json:"status"`
				Charge string `json:"charge"`
			}
			if err := json.Unmarshal(raw, &r); err != nil || r.ID == "" {
				return errf("refund list is malformed")
			}
			if r.Status == "succeeded" {
				out = append(out, payment.ProviderEvent{EventID: r.ID, Kind: "REFUND", AmountMinor: r.Amount, ProviderRef: ch.ID})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if ch.Disputed {
		err := a.listAll(ctx, key, "/v1/disputes", url.Values{"charge": {ch.ID}}, func(raw json.RawMessage) error {
			var d struct {
				ID     string `json:"id"`
				Amount int64  `json:"amount"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
				return errf("dispute list is malformed")
			}
			if !strings.HasPrefix(d.Status, "warning_") {
				out = append(out, payment.ProviderEvent{EventID: d.ID, Kind: "CHARGEBACK", AmountMinor: d.Amount, ProviderRef: ch.ID})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// listAll pages a Stripe list endpoint to its end (bounded).
func (a *Adapter) listAll(ctx context.Context, key, path string, f url.Values, each func(json.RawMessage) error) error {
	f.Set("limit", "100")
	for page := 0; page < maxListPages; page++ {
		resp, err := a.call(ctx, http.MethodGet, path, f, key, "")
		if err != nil {
			return errf("list failed: %v", err)
		}
		if resp.status != http.StatusOK {
			return errf("list failed (HTTP %d, %s)", resp.status, providerCode(resp.body))
		}
		var l listResp
		if err := json.Unmarshal(resp.body, &l); err != nil {
			return errf("list response is malformed")
		}
		last := ""
		for _, raw := range l.Data {
			if err := each(raw); err != nil {
				return err
			}
			var idOnly struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &idOnly)
			last = idOnly.ID
		}
		if !l.HasMore || last == "" {
			return nil
		}
		f.Set("starting_after", last)
	}
	return errf("list exceeded the page bound")
}

// resolveByClientRef finds the session created for clientRef after an ambiguous create. Listing is
// strongly consistent and covers sessions whose PaymentIntent was never created (Stripe creates it lazily
// on the first payment attempt); the PaymentIntent metadata search is a supplementary lookup. found=false
// is returned only when the list was read to its end within the window and the search found nothing.
func (a *Adapter) resolveByClientRef(ctx context.Context, key, clientRef string) (bool, string, error) {
	var id string
	stop := errors.New("found")
	f := url.Values{}
	f.Set("created[gte]", strconv.FormatInt(a.now().Add(-resolveWindow).Unix(), 10))
	err := a.listAll(ctx, key, "/v1/checkout/sessions", f, func(raw json.RawMessage) error {
		var s session
		if err := json.Unmarshal(raw, &s); err != nil {
			return errf("session list is malformed")
		}
		if s.ClientReferenceID == clientRef {
			id = s.ID
			return stop
		}
		return nil
	})
	if errors.Is(err, stop) {
		return true, id, nil
	}
	if err != nil {
		return false, "", err
	}

	sq := url.Values{}
	sq.Set("query", "metadata['"+metaKey+"']:'"+clientRef+"'")
	resp, err := a.call(ctx, http.MethodGet, "/v1/payment_intents/search", sq, key, "")
	if err != nil {
		return false, "", errf("payment intent search failed: %v", err)
	}
	if resp.status != http.StatusOK {
		return false, "", errf("payment intent search failed (HTTP %d, %s)", resp.status, providerCode(resp.body))
	}
	var l listResp
	if err := json.Unmarshal(resp.body, &l); err != nil {
		return false, "", errf("payment intent search response is malformed")
	}
	if len(l.Data) == 0 {
		return false, "", nil
	}
	if len(l.Data) > 1 {
		return false, "", errf("more than one payment intent carries this client reference")
	}
	var pi paymentIntent
	if err := json.Unmarshal(l.Data[0], &pi); err != nil || pi.ID == "" {
		return false, "", errf("payment intent search response is malformed")
	}
	resp, err = a.call(ctx, http.MethodGet, "/v1/checkout/sessions", url.Values{"payment_intent": {pi.ID}}, key, "")
	if err != nil {
		return false, "", errf("session lookup failed: %v", err)
	}
	if resp.status != http.StatusOK {
		return false, "", errf("session lookup failed (HTTP %d, %s)", resp.status, providerCode(resp.body))
	}
	if err := json.Unmarshal(resp.body, &l); err != nil {
		return false, "", errf("session lookup response is malformed")
	}
	for _, raw := range l.Data {
		var s session
		if json.Unmarshal(raw, &s) == nil && s.ClientReferenceID == clientRef {
			return true, s.ID, nil
		}
	}
	return false, "", errf("a payment intent carries this client reference but no matching session was found")
}

// ---------------------------------------------------------------- test connection

// TestConnection checks that the key's mode matches the account mode and that Stripe accepts the key,
// using the non-financial balance read.
func (a *Adapter) TestConnection(ctx context.Context, mode payment.Mode, merchantAccount string, c payment.Credentials) error {
	key, err := keyFor(mode, c)
	if err != nil {
		return err
	}
	resp, err := a.call(ctx, http.MethodGet, "/v1/balance", nil, key, "")
	if err != nil {
		return errf("connection test failed: %v", err)
	}
	if resp.status != http.StatusOK {
		return errf("connection test refused (HTTP %d, %s)", resp.status, providerCode(resp.body))
	}
	var b struct {
		Object   string `json:"object"`
		Livemode bool   `json:"livemode"`
	}
	if err := json.Unmarshal(resp.body, &b); err != nil || b.Object != "balance" {
		return errf("connection test returned a malformed response")
	}
	if b.Livemode != (mode == payment.ModeLive) {
		return errf("Stripe reports a different mode than the configured account mode")
	}
	return nil
}

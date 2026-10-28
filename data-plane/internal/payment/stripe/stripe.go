// Package stripe is the Stripe Checkout adapter (payment.HostedCheckoutProvider). Card data never reaches OneGate and
// no refund, capture or cancel call is made.
// Assumptions (docs.stripe.com/api): POST /v1/checkout/sessions with an Idempotency-Key returns the original result on
// replay; the session GET supports expand[]=payment_intent.latest_charge; the session list is newest-first with
// created[gte] and starting_after; payment_intents/search is eventually consistent so it is only a supplementary
// lookup; Stripe-Version is pinned so response shapes do not drift.
package stripe

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
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
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

const (
	defaultBaseURL = "https://api.stripe.com"
	defaultTimeout = 20 * time.Second
	// apiVersion pins the response shapes parsed (latest_charge on PaymentIntent, refunds not auto-expanded on Charge).
	apiVersion = "2024-06-20"

	// Stripe requires expires_at 30 minutes to 24 hours after creation; the adapter clamps with margins for clock skew
	// and latency.
	minExpiry = 31 * time.Minute
	maxExpiry = 23*time.Hour + 55*time.Minute

	// Ambiguous-create resolution lists sessions newest-first back to this age, at most maxListPages.
	resolveWindow = 30 * 24 * time.Hour
	maxListPages  = 50
	// resolveSkew allows generously for the appliance's database clock and Stripe's disagreeing when the window
	// starts at the checkout's creation time; a clock wrong by more than a day breaks TLS long before this.
	resolveSkew = 24 * time.Hour

	metaKey = "onegate_client_ref"
)

var clientRefRE = regexp.MustCompile(`^sc_[0-9a-f]{32}$`)

// requiredHostedDomains is the least-privilege set to finish payment on Stripe's hosted page (from
// checkout.stripe.com's CSP and Stripe's security guide). Telemetry, error-reporting, image-CDN and Link domains are
// excluded; issuer 3-D Secure pages cannot be enumerated.
var requiredHostedDomains = []string{
	"checkout.stripe.com",
	"js.stripe.com",
	"api.stripe.com",
	"payments.stripe.com",
	"hooks.stripe.com",
	"m.stripe.network",
	"b.stripecdn.com",
}

type Adapter struct {
	client   *http.Client
	baseURL  string
	timeout  time.Duration
	now      func() time.Time
	variants sync.Map
}

var _ payment.HostedCheckoutProvider = (*Adapter)(nil)

type Option func(*Adapter)

func WithHTTPClient(c *http.Client) Option { return func(a *Adapter) { a.client = c } }

func WithBaseURL(u string) Option { return func(a *Adapter) { a.baseURL = strings.TrimRight(u, "/") } }

func WithTimeout(d time.Duration) Option { return func(a *Adapter) { a.timeout = d } }

func WithClock(now func() time.Time) Option { return func(a *Adapter) { a.now = now } }

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

func (a *Adapter) Name() string { return "stripe" }

func (a *Adapter) RequiredHostedDomains() []string {
	return append([]string(nil), requiredHostedDomains...)
}

func (a *Adapter) CredentialKeys() []payment.CredentialKey {
	return []payment.CredentialKey{
		{Key: "secret_key", Label: "Secret key (sk_test_… / sk_live_…, or a restricted rk_ key)", Secret: true, Required: true},
	}
}

func notCreated(format string, args ...any) error {
	return fmt.Errorf("stripe: "+format+": %w", append(args, payment.ErrCheckoutNotCreated)...)
}

func errf(format string, args ...any) error { return fmt.Errorf("stripe: "+format, args...) }

// keyFor checks the key's mode against the requested mode: sk_test_/rk_test_ are sandbox, sk_live_/rk_live_ move real
// money.
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

// Stripe's minor-unit rules; unlisted currencies are two-decimal.
var zeroDecimal = map[string]bool{
	"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true, "kmf": true, "krw": true, "mga": true,
	"pyg": true, "rwf": true, "vnd": true, "vuv": true, "xaf": true, "xof": true, "xpf": true,
}

// threeDecimal: the last digit of the amount must be 0.
var threeDecimal = map[string]bool{"bhd": true, "jod": true, "kwd": true, "omr": true, "tnd": true}

// hundredsOnly: zero-decimal in practice but sent with two decimals whose fraction must be 00 (ISK, UGX).
var hundredsOnly = map[string]bool{"isk": true, "ugx": true}

var currencyRE = regexp.MustCompile(`^[a-z]{3}$`)

// checkAmount refuses an exponent mismatch rather than rescaling: silent rescaling turns 10.00 into 1000.00.
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

type response struct {
	status int
	body   []byte
}

// provablyNotSent: name resolution, TCP dial or TLS handshake failed; a failure after the connection is up is
// ambiguous.
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
var safeParamRE = regexp.MustCompile(`^[a-z0-9_\[\]]{1,96}$`)

// providerCode extracts only error type/code: Stripe's message can quote a masked key.
func providerCode(b []byte) string {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   string `json:"param"`
			Message string `json:"message"`
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
	// The name (never the value) of the field Stripe objected to, e.g. "line_items[0][price_data][currency]", for
	// refusals with no code.
	if safeParamRE.MatchString(e.Error.Param) {
		parts = append(parts, "param="+e.Error.Param)
	}
	// Stripe's own sentence when no code names the refusal; key-like tokens and long digit runs are removed first.
	if e.Error.Code == "" && e.Error.Param == "" {
		if m := redactProviderMessage(e.Error.Message); m != "" {
			parts = append(parts, "message="+m)
		}
	}
	if len(parts) == 0 {
		return "unspecified"
	}
	return strings.Join(parts, "/")
}

var (
	keyLikeRE   = regexp.MustCompile(`(?i)\b(sk|pk|rk|whsec|acct|cus|pi|ch|cs)_[A-Za-z0-9_*]+`)
	longDigitRE = regexp.MustCompile(`[0-9][0-9 -]{6,}[0-9]`)
)

// redactProviderMessage removes anything that could identify a credential, account or card, and bounds length.
func redactProviderMessage(m string) string {
	m = strings.TrimSpace(keyLikeRE.ReplaceAllString(m, "[redacted]"))
	m = longDigitRE.ReplaceAllString(m, "[redacted]")
	if len(m) > 200 {
		m = m[:200]
	}
	return m
}

// Statuses at which Stripe refuses before any object exists; 409 (idempotency conflict) and 5xx are ambiguous.
func refusedWithoutObject(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusNotFound, http.StatusTooManyRequests:
		return true
	}
	return false
}

type sessionCreated struct {
	ID                string `json:"id"`
	Object            string `json:"object"`
	URL               string `json:"url"`
	ExpiresAt         int64  `json:"expires_at"`
	ClientReferenceID string `json:"client_reference_id"`
	// PaymentMethodTypes must be exactly the approved set.
	PaymentMethodTypes []string `json:"payment_method_types"`
}

// approvedMethodTypes: Apple Pay and Google Pay are card wallets reported as type "card" and pass; Link, bank debits,
// vouchers and asynchronous methods do not.
var approvedMethodTypes = map[string]bool{"card": true}

// An empty or absent list is NOT approved: what the page offers must be known, not assumed.
func onlyApproved(types []string) bool {
	if len(types) == 0 {
		return false
	}
	for _, t := range types {
		if !approvedMethodTypes[strings.ToLower(strings.TrimSpace(t))] {
			return false
		}
	}
	return true
}

// ClientRef is the Idempotency-Key, client_reference_id and metadata on session and PaymentIntent, so an ambiguous
// create is resolved by QueryStatus.
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
	// Link is its own stored-credential method type and is not approved; Apple Pay and Google Pay are type "card" and
	// stay.
	f.Set("wallet_options[link][display]", "never")

	// Managed Payments accounts refuse payment_method_types with a 400 before any object exists. Variants are tried in
	// order (card-only, then card-only with Managed Payments off) and the accepted one is remembered per account. No
	// variant lets Stripe choose the methods: an account refusing both is "not created". Each variant has its own
	// idempotency key because Stripe records a refused request against its key; that is safe since a refused create
	// made nothing and the engine never creates again.
	acct := keyFingerprint(key)
	start := 0
	if v, ok := a.variants.Load(acct); ok {
		start = v.(int)
	}
	var resp response

	for v := start; v < len(checkoutVariants); v++ {
		checkoutVariants[v](f)
		idem := r.ClientRef
		if v > 0 {
			idem = fmt.Sprintf("%s-v%d", r.ClientRef, v)
		}
		resp, err = a.call(ctx, http.MethodPost, "/v1/checkout/sessions", f, key, idem)
		if err == nil && resp.status == http.StatusBadRequest && managedPaymentsRefusal(resp.body) && v+1 < len(checkoutVariants) {
			continue
		}
		if err == nil && resp.status >= 200 && resp.status <= 299 {
			a.variants.Store(acct, v)
		}
		break
	}
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
	// A session offering anything but approved methods is expired before any client sees it, so it is truthfully "not
	// created".
	if !onlyApproved(s.PaymentMethodTypes) {
		er, eerr := a.call(ctx, http.MethodPost, "/v1/checkout/sessions/"+url.PathEscape(s.ID)+"/expire", url.Values{}, key, r.ClientRef+"-expire")
		if eerr == nil && er.status >= 200 && er.status <= 299 {
			return payment.Checkout{}, notCreated("provider offered unapproved payment methods; the session was expired unused")
		}
		return payment.Checkout{}, errf("provider offered unapproved payment methods and the session could not be expired")
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

type balanceTxn struct {
	ID           string          `json:"id"`
	Amount       int64           `json:"amount"`
	Currency     string          `json:"currency"`
	ExchangeRate json.RawMessage `json:"exchange_rate"`
	Fee          int64           `json:"fee"`
	Net          int64           `json:"net"`
}

type charge struct {
	BalanceTxnRaw        json.RawMessage `json:"balance_transaction"`
	PaymentMethodDetails *struct {
		Type string `json:"type"`
	} `json:"payment_method_details"`
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
		found, id, err := a.resolveByClientRef(ctx, key, q.ClientRef, q.CreatedAt)
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
	f.Add("expand[]", "payment_intent.latest_charge.balance_transaction")
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
			// Only asynchronous methods complete unpaid; not conclusive either way.
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
		// Money taken by an unapproved method never grants by itself: it stays unresolved, then goes to financial
		// review.
		if ch.PaymentMethodDetails == nil || !approvedMethodTypes[strings.ToLower(ch.PaymentMethodDetails.Type)] {
			return payment.StatusResult{State: payment.CheckoutOpen, ReasonCode: "payment_method_not_approved"}, nil
		}
		res := payment.StatusResult{
			State:          payment.CheckoutCaptured,
			ProviderTxnRef: pi.ID,
			AmountMinor:    pi.AmountReceived,
			Currency:       strings.ToUpper(pi.Currency),
		}
		var bt balanceTxn
		if ok, err := expandable(ch.BalanceTxnRaw, &bt); err == nil && ok && bt.ID != "" && bt.Currency != "" {
			res.Settlement = &payment.SettlementReport{Currency: strings.ToUpper(bt.Currency), AmountMinor: bt.Amount,
				ExchangeRate: stripeRate(bt.ExchangeRate), FeeMinor: bt.Fee, NetMinor: bt.Net, BalanceRef: bt.ID}
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
				// The charge succeeded but the session has not flipped to complete: not conclusive.
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

// events reports provider-originated refunds and chargebacks. Only succeeded refunds count; warning_* disputes are
// inquiries that withdraw no funds. A dispute is a CHARGEBACK only once LOST; open or won it is a DISPUTE case that
// records no money.
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
			switch {
			case strings.HasPrefix(d.Status, "warning_"):
			case d.Status == "lost":
				out = append(out, payment.ProviderEvent{EventID: d.ID, Kind: "CHARGEBACK", AmountMinor: d.Amount, ProviderRef: ch.ID})
			default:
				out = append(out, payment.ProviderEvent{EventID: "dispute:" + d.ID, Kind: "DISPUTE", AmountMinor: d.Amount, ProviderRef: d.ID})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

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

// resolveByClientRef: listing is strongly consistent and covers sessions with no PaymentIntent (created lazily); the
// metadata search is supplementary. found=false only when the list was read to its end within the window and the search
// found nothing.
//
// The window starts at the checkout's creation (less resolveSkew) when it is known (audit P6 L3): the client
// reference did not exist before then, and a 30-day window cannot be listed within the page bound on a busy account.
func (a *Adapter) resolveByClientRef(ctx context.Context, key, clientRef string, createdAt time.Time) (bool, string, error) {
	var id string
	stop := errors.New("found")
	from := a.now().Add(-resolveWindow)
	if !createdAt.IsZero() && createdAt.Add(-resolveSkew).After(from) {
		from = createdAt.Add(-resolveSkew)
	}
	f := url.Values{}
	f.Set("created[gte]", strconv.FormatInt(from.Unix(), 10))
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

// stripeRate is exchange_rate exactly as sent (null if no conversion), kept as text so float rounding never touches a
// financial record.
func stripeRate(raw json.RawMessage) string {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" || !rateRE.MatchString(t) {
		return ""
	}
	return t
}

var rateRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)

// stripePresentment: currencies documented for card payments and priceable in the package editor. Stripe stays the
// authority, but a package outside this list is simply not offered by card rather than offered and refused.
var stripePresentment = map[string]bool{
	"USD": true, "EUR": true, "GBP": true, "AED": true, "SAR": true, "QAR": true, "EGP": true, "KWD": true,
	"BHD": true, "OMR": true, "JOD": true, "CHF": true, "CAD": true, "AUD": true, "NZD": true, "JPY": true,
	"CNY": true, "HKD": true, "SGD": true, "INR": true, "TRY": true, "MAD": true, "ZAR": true, "SEK": true,
	"NOK": true, "DKK": true, "PLN": true, "CZK": true, "HUF": true, "RON": true,
}

func (a *Adapter) ConvertsPresentment(presentment, settlement string) bool {
	p := strings.ToUpper(strings.TrimSpace(presentment))
	s := strings.ToUpper(strings.TrimSpace(settlement))
	return p == s || (stripePresentment[p] && stripePresentment[s])
}

func managedPaymentsRefusal(body []byte) bool {
	b := strings.ToLower(string(body))
	return strings.Contains(b, "managed payments") || strings.Contains(b, "managed_payments")
}

var checkoutVariants = []func(url.Values){
	func(f url.Values) { f.Set("payment_method_types[0]", "card"); f.Del("managed_payments[enabled]") },
	func(f url.Values) {
		f.Set("payment_method_types[0]", "card")
		f.Set("managed_payments[enabled]", "false")
	},
}

func keyFingerprint(key string) string {
	h := sha256.Sum256([]byte("stripe-variant:" + key))
	return hex.EncodeToString(h[:8])
}

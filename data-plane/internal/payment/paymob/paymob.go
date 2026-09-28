// Package paymob is the Paymob Intention API + Unified Checkout adapter for OneGate's hosted Card
// payment (payment.HostedCheckoutProvider).
//
// Card data never reaches OneGate: the client pays on Paymob's hosted Unified Checkout page. The adapter
// creates one payment intention and later asks Paymob, outbound and authenticated, what happened.
//
// Provider-API assumptions and their sources (checked 2026-09-28):
//   - Create: POST {base}/v1/intention/ with "Authorization: Token <secret_key>" and a JSON body of amount
//     (minor units), currency, payment_methods (integration ids), items, billing_data, special_reference,
//     redirection_url, expiration, extras; the 201 response carries id, client_secret and
//     intention_order_id. Source: developers.paymob.com "Create Intention" and the PaymobAccept
//     API-Postman-Collections repository.
//   - billing_data: Paymob requires the object; fields that do not apply are sent as "NA" (Paymob's own
//     integration guidance for anonymous checkout). OneGate sends no guest data to the provider.
//   - Redirect: {base}/unifiedcheckout/?publicKey=<public_key>&clientSecret=<client_secret>.
//   - Status: POST {base}/api/auth/tokens {"api_key"} returns a short-lived token; POST
//     {base}/api/ecommerce/orders/transaction_inquiry with "Authorization: Bearer <token>" and
//     {"order_id"} or {"merchant_order_id"} returns the order's transaction (PaymobAccept Transaction
//     Inquiry collection). The intention's special_reference is assumed to be the created order's
//     merchant_order_id; the adapter prefers the order id returned at creation and uses
//     merchant_order_id only to resolve an ambiguous creation.
//   - Key mode: Paymob keys are "<country>_<sk|pk>_<test|live>_…" with country egy, sau, are, omn, pak. The
//     prefix is what Paymob's own checkout page uses to route a key to its region backend
//     (eg.checkout.paymob.com page source), so it is treated as reliable and enforced.
//
// Paymob has no documented Idempotency-Key header. Duplicate protection for an ambiguous create rests on
// the engine never re-creating (it resolves by QueryStatus on the ClientRef) and on special_reference,
// which Paymob documents as a unique reference.
//
// Money never moves as a result of anything this package sends. There is no refund, void or capture call.
package paymob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
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
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

const (
	defaultTimeout = 20 * time.Second
	// Intention lifetime bounds (seconds sent as "expiration"). The lower bound keeps a checkout
	// usable; the upper bound matches Stripe's 24h ceiling so both providers behave alike.
	minExpiry = 5 * time.Minute
	maxExpiry = 24 * time.Hour
	// expiryGrace absorbs clock skew before a paymentless checkout is declared EXPIRED.
	expiryGrace = 5 * time.Minute
	placeholder = "NA"
)

var clientRefRE = regexp.MustCompile(`^sc_[0-9a-f]{32}$`)

// Region is one Paymob regional deployment. Test and live share the base URL; the keys decide the mode.
type Region struct {
	Name    string
	BaseURL string
	Prefix  string // key country prefix
	// Domains are what the client must reach to pay: the regional API host (the unifiedcheckout redirect
	// and the page's intention/payment calls) and the regional checkout page host it redirects to.
	// Source: the 302 from {base}/unifiedcheckout/ and the connect-src of the checkout page's
	// Content-Security-Policy (observed 2026-09-28). Google Fonts, analytics, Sentry and wallet domains
	// are excluded: the card form completes without them. Issuer 3-D Secure ACS pages are hosted by the
	// card issuer (the page's CSP is frame-src https:) and cannot be enumerated here.
	Domains []string
}

// Regions Paymob documents. Pakistan (pakistan.paymob.com) is not included: its checkout host could not
// be verified, and a region whose hosted-page domains are unknown cannot be given a least-privilege
// allowlist.
var Regions = map[string]Region{
	"egypt": {Name: "egypt", BaseURL: "https://accept.paymob.com", Prefix: "egy", Domains: []string{"accept.paymob.com", "eg.checkout.paymob.com"}},
	"ksa":   {Name: "ksa", BaseURL: "https://ksa.paymob.com", Prefix: "sau", Domains: []string{"ksa.paymob.com", "ksa.checkout.paymob.com"}},
	"uae":   {Name: "uae", BaseURL: "https://uae.paymob.com", Prefix: "are", Domains: []string{"uae.paymob.com", "uae.checkout.paymob.com"}},
	"oman":  {Name: "oman", BaseURL: "https://oman.paymob.com", Prefix: "omn", Domains: []string{"oman.paymob.com", "om.checkout.paymob.com"}},
}

// currencyExponent lists the currencies this adapter will send and the minor-unit exponent Paymob's
// "amount in cents" means for them. OMR is deliberately absent: whether Paymob's amount is in baisa
// (exponent 3) or hundredths is not verified, and guessing would mis-scale money by 10x.
var currencyExponent = map[string]int16{"EGP": 2, "SAR": 2, "AED": 2, "USD": 2, "EUR": 2, "GBP": 2}

// Adapter is a Paymob hosted-checkout adapter for one region. It holds no credentials.
type Adapter struct {
	client  *http.Client
	region  Region
	baseURL string
	timeout time.Duration
	now     func() time.Time
}

var _ payment.HostedCheckoutProvider = (*Adapter)(nil)

// Option configures an Adapter.
type Option func(*Adapter)

// WithHTTPClient sets the HTTP client used for every call.
func WithHTTPClient(c *http.Client) Option { return func(a *Adapter) { a.client = c } }

// WithRegion selects the regional deployment ("egypt" default, "ksa", "uae", "oman"). An unknown name
// makes New return an error.
func WithRegion(name string) Option {
	return func(a *Adapter) { a.region = Region{Name: strings.ToLower(strings.TrimSpace(name))} }
}

// WithBaseURL overrides the region's API base URL (tests use an httptest server). Hosted domains stay
// the region's.
func WithBaseURL(u string) Option { return func(a *Adapter) { a.baseURL = strings.TrimRight(u, "/") } }

// WithTimeout bounds every individual provider call.
func WithTimeout(d time.Duration) Option { return func(a *Adapter) { a.timeout = d } }

// WithClock overrides the clock (tests).
func WithClock(now func() time.Time) Option { return func(a *Adapter) { a.now = now } }

// New builds an Adapter. RequiredHostedDomains takes no credentials, so the region (and therefore the
// allowlist) is fixed per adapter instance; a credential "region" that disagrees is refused per call.
func New(opts ...Option) (*Adapter, error) {
	a := &Adapter{client: &http.Client{}, region: Regions["egypt"], timeout: defaultTimeout, now: time.Now}
	for _, o := range opts {
		o(a)
	}
	r, ok := Regions[a.region.Name]
	if !ok {
		return nil, errors.New("paymob: unknown region")
	}
	a.region = r
	if a.baseURL == "" {
		a.baseURL = r.BaseURL
	}
	if a.client == nil {
		a.client = &http.Client{}
	}
	if a.timeout <= 0 {
		a.timeout = defaultTimeout
	}
	return a, nil
}

// Name is stored as the provider name.
func (a *Adapter) Name() string { return "paymob" }

// RequiredHostedDomains returns the region's hosted-page domains.
func (a *Adapter) RequiredHostedDomains() []string { return append([]string(nil), a.region.Domains...) }

// CredentialKeys names what the Admin Console collects.
func (a *Adapter) CredentialKeys() []payment.CredentialKey {
	return []payment.CredentialKey{
		{Key: "secret_key", Label: "Secret key (creates payment intentions)", Secret: true, Required: true},
		{Key: "public_key", Label: "Public key (opens Unified Checkout)", Secret: false, Required: true},
		{Key: "api_key", Label: "API key (transaction inquiry)", Secret: true, Required: true},
		{Key: "integration_ids", Label: "Card integration IDs (comma-separated)", Secret: false, Required: true},
		{Key: "hmac_secret", Label: "HMAC secret (return-redirect sanity check only)", Secret: true, Required: false},
		{Key: "region", Label: "Region (egypt, ksa, uae, oman)", Secret: false, Required: false},
	}
}

// ---------------------------------------------------------------- errors (never carry secrets)

func notCreated(format string, args ...any) error {
	return fmt.Errorf("paymob: "+format+": %w", append(args, payment.ErrCheckoutNotCreated)...)
}

func errf(format string, args ...any) error { return fmt.Errorf("paymob: "+format, args...) }

// ---------------------------------------------------------------- credentials and mode

var keyRE = regexp.MustCompile(`^([a-z]{3})_(sk|pk)_(test|live)_[A-Za-z0-9]+$`)

// checkKey verifies a secret/public key's kind, region and mode from its prefix. An unrecognised format is
// refused: the mode cannot be proven.
func (a *Adapter) checkKey(name, kind string, mode payment.Mode, v string) error {
	m := keyRE.FindStringSubmatch(v)
	if m == nil || m[2] != kind {
		return fmt.Errorf("paymob: %s is not a Paymob %s key", name, map[string]string{"sk": "secret", "pk": "public"}[kind])
	}
	if m[1] != a.region.Prefix {
		return fmt.Errorf("paymob: %s belongs to a different Paymob region than this adapter (%s)", name, a.region.Name)
	}
	keyMode := payment.ModeTest
	if m[3] == "live" {
		keyMode = payment.ModeLive
	}
	if keyMode != mode {
		return fmt.Errorf("paymob: %s is a %s-mode key configured for a %s-mode account; refused", name, keyMode, mode)
	}
	return nil
}

type creds struct {
	secretKey, publicKey, apiKey, hmacSecret string
	integrationIDs                           []int64
}

func (a *Adapter) creds(mode payment.Mode, c payment.Credentials, needCreate, needInquiry bool) (creds, error) {
	if mode != payment.ModeTest && mode != payment.ModeLive {
		return creds{}, errors.New("paymob: account mode is neither TEST nor LIVE")
	}
	if r := strings.ToLower(strings.TrimSpace(c["region"])); r != "" && r != a.region.Name {
		return creds{}, errors.New("paymob: the account's region does not match this adapter's region")
	}
	out := creds{
		secretKey:  strings.TrimSpace(c["secret_key"]),
		publicKey:  strings.TrimSpace(c["public_key"]),
		apiKey:     strings.TrimSpace(c["api_key"]),
		hmacSecret: c["hmac_secret"],
	}
	// Both keys are checked whenever present so a TEST account can never hold a LIVE key unnoticed.
	if needCreate || out.secretKey != "" {
		if err := a.checkKey("secret_key", "sk", mode, out.secretKey); err != nil {
			return creds{}, err
		}
	}
	if needCreate || out.publicKey != "" {
		if err := a.checkKey("public_key", "pk", mode, out.publicKey); err != nil {
			return creds{}, err
		}
	}
	if needInquiry && out.apiKey == "" {
		return creds{}, errors.New("paymob: api_key is not configured")
	}
	if needCreate {
		for _, s := range strings.Split(c["integration_ids"], ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil || id <= 0 {
				return creds{}, errors.New("paymob: integration_ids must be positive integers")
			}
			out.integrationIDs = append(out.integrationIDs, id)
		}
		if len(out.integrationIDs) == 0 {
			return creds{}, errors.New("paymob: integration_ids is not configured")
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- transport

type transportError struct {
	kind    string
	notSent bool
}

func (e *transportError) Error() string { return "paymob: transport failure (" + e.kind + ")" }

// provablyNotSent reports whether a transport error proves the request never reached Paymob (name
// resolution, TCP dial or TLS handshake failed).
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

func (a *Adapter) post(ctx context.Context, path, authHeader string, body any) (int, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, &transportError{kind: errKind(err), notSent: true}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return 0, nil, &transportError{kind: "encode", notSent: true}
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, &transportError{kind: "request", notSent: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, &transportError{kind: errKind(err), notSent: provablyNotSent(err)}
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, &transportError{kind: "read " + errKind(err)}
	}
	return resp.StatusCode, rb, nil
}

// refusedWithoutObject: statuses at which Paymob refuses before creating anything (validation,
// authentication, permission, missing route, unsupported media, rate limit). Everything else,
// including 409 and 5xx, is ambiguous.
func refusedWithoutObject(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusMethodNotAllowed, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity,
		http.StatusTooManyRequests:
		return true
	}
	return false
}

// ---------------------------------------------------------------- provider session reference

// sessionRef is the ProviderSessionRef: "<intention id>|<order id>|<expiry unix seconds>". The order id
// is what the status query asks about; the expiry is what lets a paymentless checkout become EXPIRED
// (Paymob's inquiry answers about transactions, not about the intention's lifetime).
type sessionRef struct {
	intentionID string
	orderID     int64
	expiresAt   time.Time
}

func (s sessionRef) String() string {
	return s.intentionID + "|" + strconv.FormatInt(s.orderID, 10) + "|" + strconv.FormatInt(s.expiresAt.Unix(), 10)
}

func parseSessionRef(v string) (sessionRef, error) {
	p := strings.Split(v, "|")
	if len(p) != 3 || p[0] == "" {
		return sessionRef{}, errors.New("paymob: provider session reference is malformed")
	}
	oid, err1 := strconv.ParseInt(p[1], 10, 64)
	exp, err2 := strconv.ParseInt(p[2], 10, 64)
	if err1 != nil || err2 != nil || oid <= 0 || exp <= 0 {
		return sessionRef{}, errors.New("paymob: provider session reference is malformed")
	}
	return sessionRef{intentionID: p[0], orderID: oid, expiresAt: time.Unix(exp, 0).UTC()}, nil
}

// ---------------------------------------------------------------- create

type billingData struct {
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	Email       string `json:"email"`
	PhoneNumber string `json:"phone_number"`
	Apartment   string `json:"apartment"`
	Floor       string `json:"floor"`
	Street      string `json:"street"`
	Building    string `json:"building"`
	City        string `json:"city"`
	State       string `json:"state"`
	Country     string `json:"country"`
	PostalCode  string `json:"postal_code"`
}

type item struct {
	Name     string `json:"name"`
	Amount   int64  `json:"amount"`
	Quantity int    `json:"quantity"`
}

type intentionReq struct {
	Amount           int64             `json:"amount"`
	Currency         string            `json:"currency"`
	PaymentMethods   []int64           `json:"payment_methods"`
	Items            []item            `json:"items"`
	BillingData      billingData       `json:"billing_data"`
	SpecialReference string            `json:"special_reference"`
	RedirectionURL   string            `json:"redirection_url"`
	Expiration       int64             `json:"expiration"`
	Extras           map[string]string `json:"extras"`
}

type intentionResp struct {
	ID               string      `json:"id"`
	ClientSecret     string      `json:"client_secret"`
	IntentionOrderID json.Number `json:"intention_order_id"`
	SpecialReference *string     `json:"special_reference"`
}

// CreateCheckout creates one payment intention and returns the Unified Checkout redirect. No
// notification_url is sent: OneGate is outbound-only and learns the outcome by QueryStatus.
func (a *Adapter) CreateCheckout(ctx context.Context, r payment.CheckoutRequest) (payment.Checkout, error) {
	if !clientRefRE.MatchString(r.ClientRef) {
		return payment.Checkout{}, notCreated("client reference is not a OneGate reference")
	}
	c, err := a.creds(r.Mode, r.Credentials, true, false)
	if err != nil {
		return payment.Checkout{}, fmt.Errorf("%w: %w", err, payment.ErrCheckoutNotCreated)
	}
	cur := strings.ToUpper(strings.TrimSpace(r.Currency))
	exp, ok := currencyExponent[cur]
	if !ok {
		return payment.Checkout{}, notCreated("currency %q is not supported by this adapter", cur)
	}
	if r.Exponent != exp {
		return payment.Checkout{}, notCreated("currency %s has exponent %d at Paymob, pinned exponent is %d; refused", cur, exp, r.Exponent)
	}
	if r.AmountMinor <= 0 {
		return payment.Checkout{}, notCreated("amount must be positive")
	}
	if u, err := url.Parse(r.ReturnURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return payment.Checkout{}, notCreated("return URL is not an absolute http(s) URL")
	}
	now := a.now()
	expiresAt := r.ExpiresAt
	if expiresAt.IsZero() || expiresAt.Before(now.Add(minExpiry)) {
		expiresAt = now.Add(minExpiry)
	}
	if expiresAt.After(now.Add(maxExpiry)) {
		expiresAt = now.Add(maxExpiry)
	}
	expiresAt = expiresAt.Truncate(time.Second)
	name := strings.TrimSpace(r.Description)
	if name == "" {
		name = "Internet access"
	}
	body := intentionReq{
		Amount:         r.AmountMinor,
		Currency:       cur,
		PaymentMethods: c.integrationIDs,
		Items:          []item{{Name: name, Amount: r.AmountMinor, Quantity: 1}},
		BillingData: billingData{
			FirstName: placeholder, LastName: placeholder, Email: placeholder, PhoneNumber: placeholder,
			Apartment: placeholder, Floor: placeholder, Street: placeholder, Building: placeholder,
			City: placeholder, State: placeholder, Country: placeholder, PostalCode: placeholder,
		},
		SpecialReference: r.ClientRef,
		RedirectionURL:   r.ReturnURL,
		Expiration:       int64(expiresAt.Sub(now) / time.Second),
		Extras:           map[string]string{"onegate_client_ref": r.ClientRef},
	}
	status, rb, err := a.post(ctx, "/v1/intention/", "Token "+c.secretKey, body)
	if err != nil {
		var te *transportError
		if errors.As(err, &te) && te.notSent {
			return payment.Checkout{}, notCreated("request never reached Paymob (%s)", te.kind)
		}
		return payment.Checkout{}, errf("create checkout outcome unknown: %v", err)
	}
	if status < 200 || status > 299 {
		if refusedWithoutObject(status) {
			return payment.Checkout{}, notCreated("create checkout refused (HTTP %d)", status)
		}
		return payment.Checkout{}, errf("create checkout outcome unknown (HTTP %d)", status)
	}
	var ir intentionResp
	dec := json.NewDecoder(bytes.NewReader(rb))
	dec.UseNumber()
	if err := dec.Decode(&ir); err != nil || ir.ID == "" || ir.ClientSecret == "" {
		return payment.Checkout{}, errf("create checkout outcome unknown: malformed response")
	}
	if ir.SpecialReference != nil && *ir.SpecialReference != "" && *ir.SpecialReference != r.ClientRef {
		return payment.Checkout{}, errf("create checkout returned an intention for a different reference")
	}
	if strings.Contains(ir.ID, "|") {
		return payment.Checkout{}, errf("create checkout returned an unusable intention id")
	}
	oid, err := ir.IntentionOrderID.Int64()
	if err != nil || oid <= 0 {
		return payment.Checkout{}, errf("create checkout outcome unknown: response has no order id")
	}
	ref := sessionRef{intentionID: ir.ID, orderID: oid, expiresAt: expiresAt}
	redirect := a.baseURL + "/unifiedcheckout/?publicKey=" + url.QueryEscape(c.publicKey) + "&clientSecret=" + url.QueryEscape(ir.ClientSecret)
	return payment.Checkout{ProviderSessionRef: ref.String(), RedirectURL: redirect, ExpiresAt: expiresAt.UTC()}, nil
}

// ---------------------------------------------------------------- status

type transaction struct {
	ID                  int64  `json:"id"`
	Pending             bool   `json:"pending"`
	Success             bool   `json:"success"`
	IsAuth              bool   `json:"is_auth"`
	IsVoided            bool   `json:"is_voided"`
	IsRefunded          bool   `json:"is_refunded"`
	AmountCents         int64  `json:"amount_cents"`
	RefundedAmountCents int64  `json:"refunded_amount_cents"`
	Currency            string `json:"currency"`
	Data                struct {
		TxnResponseCode json.RawMessage `json:"txn_response_code"`
	} `json:"data"`
	Order struct {
		ID              int64  `json:"id"`
		MerchantOrderID string `json:"merchant_order_id"`
		PaidAmountCents int64  `json:"paid_amount_cents"`
	} `json:"order"`
}

func (a *Adapter) authToken(ctx context.Context, apiKey string) (string, error) {
	status, rb, err := a.post(ctx, "/api/auth/tokens", "", map[string]string{"api_key": apiKey})
	if err != nil {
		return "", errf("authentication failed: %v", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", errf("authentication refused (HTTP %d)", status)
	}
	var t struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rb, &t); err != nil || t.Token == "" {
		return "", errf("authentication returned a malformed response")
	}
	return t.Token, nil
}

var codeRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

// QueryStatus authenticates and asks Paymob's transaction inquiry about the order. It never creates,
// voids, captures or refunds anything and is safe to repeat.
//
// Mapping (conservative):
//   - pending → OPEN.
//   - success, not pending, not voided, not auth-only → CAPTURED (ProviderTxnRef = transaction id), with
//     refunds reported as events. A later refund does not un-capture: the capture happened.
//   - success=false, not pending → DECLINED only once the checkout has expired; before that the client can
//     retry on the same Unified Checkout, so it stays OPEN (reason "declined_retryable").
//   - no transaction → OPEN until expiry, then EXPIRED; NOT_FOUND when the reference is unknown and no
//     session reference exists (an ambiguous create whose client_secret never reached the client).
//   - voided, auth-only, or an order that records a paid amount the transaction does not explain → error
//     (manual review), never a guess.
func (a *Adapter) QueryStatus(ctx context.Context, q payment.StatusQuery) (payment.StatusResult, error) {
	if !clientRefRE.MatchString(q.ClientRef) {
		return payment.StatusResult{}, errf("client reference is not a OneGate reference")
	}
	c, err := a.creds(q.Mode, q.Credentials, false, true)
	if err != nil {
		return payment.StatusResult{}, err
	}
	var sr *sessionRef
	if strings.TrimSpace(q.ProviderSessionRef) != "" {
		s, err := parseSessionRef(strings.TrimSpace(q.ProviderSessionRef))
		if err != nil {
			return payment.StatusResult{}, err
		}
		sr = &s
	}
	token, err := a.authToken(ctx, c.apiKey)
	if err != nil {
		return payment.StatusResult{}, err
	}
	var body map[string]any
	if sr != nil {
		body = map[string]any{"order_id": sr.orderID}
	} else {
		body = map[string]any{"merchant_order_id": q.ClientRef}
	}
	status, rb, err := a.post(ctx, "/api/ecommerce/orders/transaction_inquiry", "Bearer "+token, body)
	if err != nil {
		return payment.StatusResult{}, errf("status query failed: %v", err)
	}
	now := a.now()
	noTxn := func() (payment.StatusResult, error) {
		if sr == nil {
			return payment.StatusResult{State: payment.CheckoutNotFound}, nil
		}
		if now.After(sr.expiresAt.Add(expiryGrace)) {
			return payment.StatusResult{State: payment.CheckoutExpired, ReasonCode: "intention_expired"}, nil
		}
		return payment.StatusResult{State: payment.CheckoutOpen}, nil
	}
	if status == http.StatusNotFound {
		return noTxn()
	}
	if status != http.StatusOK {
		return payment.StatusResult{}, errf("status query failed (HTTP %d)", status)
	}
	var t transaction
	if err := json.Unmarshal(rb, &t); err != nil {
		return payment.StatusResult{}, errf("status query returned a malformed response")
	}
	if t.ID == 0 {
		if t.Order.PaidAmountCents > 0 {
			return payment.StatusResult{}, errf("order records a paid amount but no transaction; manual review required")
		}
		return noTxn()
	}
	if t.Order.MerchantOrderID != "" && t.Order.MerchantOrderID != q.ClientRef {
		return payment.StatusResult{}, errf("transaction does not belong to this client reference")
	}
	if sr != nil && t.Order.ID != 0 && t.Order.ID != sr.orderID {
		return payment.StatusResult{}, errf("transaction does not belong to this checkout's order")
	}
	txnRef := strconv.FormatInt(t.ID, 10)
	switch {
	case t.IsVoided:
		return payment.StatusResult{}, errf("transaction %s was voided at the provider; manual review required", txnRef)
	case t.Pending:
		return payment.StatusResult{State: payment.CheckoutOpen, ReasonCode: "pending"}, nil
	case t.Success && t.IsAuth:
		return payment.StatusResult{}, errf("transaction %s is authorised but not captured; manual review required", txnRef)
	case t.Success:
		res := payment.StatusResult{
			State:          payment.CheckoutCaptured,
			ProviderTxnRef: txnRef,
			AmountMinor:    t.AmountCents,
			Currency:       strings.ToUpper(t.Currency),
		}
		if t.RefundedAmountCents > 0 {
			// Paymob's inquiry exposes only the cumulative refunded amount, not individual refund ids. The
			// event id is therefore stable per cumulative total, and AmountMinor is that CUMULATIVE total
			// as of this observation: a later partial refund yields a new event whose amount supersedes
			// (does not add to) the earlier one.
			res.Events = []payment.ProviderEvent{{
				EventID:     "refund:" + txnRef + ":" + strconv.FormatInt(t.RefundedAmountCents, 10),
				Kind:        "REFUND",
				AmountMinor: t.RefundedAmountCents,
				ProviderRef: txnRef,
				Cumulative:  true,
			}}
		}
		return res, nil
	default:
		if t.Order.PaidAmountCents > 0 {
			return payment.StatusResult{}, errf("order records a paid amount but the latest transaction failed; manual review required")
		}
		rc := responseCode(t.Data.TxnResponseCode)
		if sr == nil || now.After(sr.expiresAt.Add(expiryGrace)) {
			return payment.StatusResult{State: payment.CheckoutDeclined, ReasonCode: rc}, nil
		}
		return payment.StatusResult{State: payment.CheckoutOpen, ReasonCode: "declined_retryable"}, nil
	}
}

func responseCode(raw json.RawMessage) string {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if codeRE.MatchString(s) {
		return s
	}
	return "declined"
}

// ---------------------------------------------------------------- test connection

// TestConnection checks every configured key's region and mode, the integration ids, and that Paymob
// issues an auth token for the api_key. Paymob exposes no non-financial call authenticated by the secret
// key, so the secret key is validated by format only.
func (a *Adapter) TestConnection(ctx context.Context, mode payment.Mode, merchantAccount string, c payment.Credentials) error {
	cr, err := a.creds(mode, c, true, true)
	if err != nil {
		return err
	}
	_, err = a.authToken(ctx, cr.apiKey)
	return err
}

// ---------------------------------------------------------------- redirect HMAC (sanity only)

// hmacFields is Paymob's documented concatenation order for the transaction HMAC.
var hmacFields = []string{
	"amount_cents", "created_at", "currency", "error_occured", "has_parent_transaction", "id",
	"integration_id", "is_3d_secure", "is_auth", "is_capture", "is_refunded", "is_standalone_payment",
	"is_voided", "order", "owner", "pending", "source_data.pan", "source_data.sub_type", "source_data.type",
	"success",
}

// VerifyRedirectHMAC checks the hmac parameter Paymob appends to the return redirect. It is a SANITY
// check for the portal's display only and is never financial proof: the only proof is QueryStatus.
// It returns false when hmacSecret is empty or the parameter is absent.
func VerifyRedirectHMAC(q url.Values, hmacSecret string) bool {
	got := strings.ToLower(q.Get("hmac"))
	if hmacSecret == "" || got == "" {
		return false
	}
	var sb strings.Builder
	for _, f := range hmacFields {
		sb.WriteString(q.Get(f))
	}
	m := hmac.New(sha512.New, []byte(hmacSecret))
	m.Write([]byte(sb.String()))
	want := hex.EncodeToString(m.Sum(nil))
	return hmac.Equal([]byte(want), []byte(got))
}

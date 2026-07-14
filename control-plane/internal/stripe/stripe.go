// Package stripe is a minimal Stripe client tailored to our one use case:
// create Checkout sessions for guest wifi purchases, and verify webhook
// signatures on their completion events.
//
// We hand-roll the HTTP layer instead of pulling in stripe-go — the SDK
// is a large surface and we only touch two endpoints:
//
//	POST https://api.stripe.com/v1/checkout/sessions
//	(webhook signature verify — pure HMAC-SHA256, no network)
//
// Keep it this way until we need Customers, Subscriptions, Refunds, etc.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBase         = "https://api.stripe.com"
	signatureToleranceSkew = 5 * time.Minute
)

// Client holds the per-request secret key and an HTTP transport. One
// client per Stripe account; safe for concurrent callers.
type Client struct {
	SecretKey  string
	APIBase    string       // override for tests; empty = production
	HTTPClient *http.Client // optional; defaults to a 10s-timeout client
}

func New(secretKey string) *Client {
	return &Client{
		SecretKey:  secretKey,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// CheckoutLineItem is a single "price × quantity" line. For our wifi
// products the price is set inline (Stripe's "price_data" shape) so we
// don't have to pre-register every ticket_template as a Price object.
type CheckoutLineItem struct {
	Name        string
	Description string
	AmountCents int64
	Currency    string
	Quantity    int
}

// CheckoutRequest is the subset of Stripe's API we use.
type CheckoutRequest struct {
	Mode       string // "payment" | "subscription" — we use "payment"
	SuccessURL string
	CancelURL  string
	LineItems  []CheckoutLineItem
	// Any key we pass here lands on the resulting Session.metadata map
	// AND on the PaymentIntent.metadata — the webhook handler reads it
	// to thread our payment-row UUID through.
	Metadata map[string]string
}

// CheckoutResponse is what we pluck from Stripe's reply.
type CheckoutResponse struct {
	ID          string `json:"id"`     // cs_live_… / cs_test_…
	URL         string `json:"url"`    // hosted checkout page
	Status      string `json:"status"` // open | complete | expired
	AmountTotal int64  `json:"amount_total"`
}

// CreateCheckoutSession POSTs form-encoded parameters to
// /v1/checkout/sessions. Stripe's API is form-encoded even for writes —
// that's legacy but documented and stable.
func (c *Client) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (*CheckoutResponse, error) {
	form := url.Values{}
	mode := req.Mode
	if mode == "" {
		mode = "payment"
	}
	form.Set("mode", mode)
	form.Set("success_url", req.SuccessURL)
	form.Set("cancel_url", req.CancelURL)
	for i, li := range req.LineItems {
		q := li.Quantity
		if q <= 0 {
			q = 1
		}
		prefix := fmt.Sprintf("line_items[%d]", i)
		form.Set(prefix+"[quantity]", strconv.Itoa(q))
		form.Set(prefix+"[price_data][currency]", strings.ToLower(li.Currency))
		form.Set(prefix+"[price_data][unit_amount]", strconv.FormatInt(li.AmountCents, 10))
		form.Set(prefix+"[price_data][product_data][name]", li.Name)
		if li.Description != "" {
			form.Set(prefix+"[price_data][product_data][description]", li.Description)
		}
	}
	for k, v := range req.Metadata {
		form.Set("metadata["+k+"]", v)
		// Mirror into payment_intent_data so the subsequent
		// payment_intent.succeeded event (if we ever consume it) also
		// carries our payment row id.
		form.Set("payment_intent_data[metadata]["+k+"]", v)
	}

	base := c.APIBase
	if base == "" {
		base = defaultAPIBase
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.SecretKey)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stripe checkout: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var e stripeErrorEnvelope
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("stripe checkout: %s — %s", e.Error.Code, e.Error.Message)
		}
		return nil, fmt.Errorf("stripe checkout status=%d body=%s", resp.StatusCode, string(b))
	}
	var out CheckoutResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("stripe checkout decode: %w", err)
	}
	if out.ID == "" {
		return nil, errors.New("stripe: checkout response missing id")
	}
	return &out, nil
}

// ----- Webhook signature verification -----
//
// Spec: https://docs.stripe.com/webhooks/signatures
//
//   Stripe-Signature: t=<unix>,v1=<hexdigest>,v1=<hexdigest>,...
//   signed_payload   = "<t>.<raw body>"
//   digest           = HMAC_SHA256(webhook_secret, signed_payload)
//
// Tolerance: reject events older than signatureToleranceSkew — the
// industry default is 5 minutes; it's both lenient enough for clock
// skew and tight enough to throw out replayed events after an incident.

var (
	ErrSignatureMissing = errors.New("stripe: missing Stripe-Signature header")
	ErrSignatureInvalid = errors.New("stripe: invalid signature")
	ErrSignatureStale   = errors.New("stripe: signature too old (tolerance exceeded)")
)

// VerifyWebhook parses + validates a Stripe-Signature header against the
// raw request body and returns the parsed Event on success.
func VerifyWebhook(body []byte, sigHeader, secret string, now time.Time) (*Event, error) {
	if sigHeader == "" {
		return nil, ErrSignatureMissing
	}
	var ts int64
	var sigs []string
	for _, part := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			t, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return nil, ErrSignatureInvalid
			}
			ts = t
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return nil, ErrSignatureInvalid
	}
	eventTime := time.Unix(ts, 0)
	if d := now.Sub(eventTime); d < 0 || d > signatureToleranceSkew {
		return nil, ErrSignatureStale
	}
	signedPayload := strconv.FormatInt(ts, 10) + "." + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expect := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range sigs {
		// Constant-time compare to avoid side-channel leaks on the
		// hex string length (Stripe always sends 64-char hex, so lengths
		// match in practice, but belt-and-braces).
		if hmac.Equal([]byte(sig), []byte(expect)) {
			var e Event
			if err := json.Unmarshal(body, &e); err != nil {
				return nil, fmt.Errorf("stripe: body parse: %w", err)
			}
			return &e, nil
		}
	}
	return nil, ErrSignatureInvalid
}

// SignPayload produces the header value a real Stripe webhook would send
// for the given body at the given time. Useful in tests; not used in prod.
func SignPayload(body []byte, secret string, ts time.Time) string {
	t := ts.Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(t, 10) + "." + string(body)))
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}

// Event mirrors the subset of Stripe's Event object we consume. `Data.Object`
// is kept as raw JSON so the handler can unmarshal into the exact subtype
// per event (Checkout Session, PaymentIntent, etc.).
type Event struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
	Created int64 `json:"created"`
}

// CheckoutSessionObject is the subset of the data.object payload for
// checkout.session.completed events that we care about.
type CheckoutSessionObject struct {
	ID            string            `json:"id"`
	PaymentIntent string            `json:"payment_intent"`
	PaymentStatus string            `json:"payment_status"`
	AmountTotal   int64             `json:"amount_total"`
	Currency      string            `json:"currency"`
	CustomerEmail string            `json:"customer_email"`
	Metadata      map[string]string `json:"metadata"`
}

type stripeErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

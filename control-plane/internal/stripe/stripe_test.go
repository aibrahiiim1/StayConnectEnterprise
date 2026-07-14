package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeStripeAPI imitates https://api.stripe.com for the one endpoint we
// call. It echoes the parsed form fields into `captured` so tests can
// assert on the outgoing shape (line_items, mode, metadata, etc.).
func fakeStripeAPI(t *testing.T, status int, respBody string, captured *url.Values, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("wrong method %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("wrong content-type %q", r.Header.Get("Content-Type"))
		}
		*gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		*captured = form
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

func TestCreateCheckoutSessionShape(t *testing.T) {
	var captured url.Values
	var gotAuth string
	srv := fakeStripeAPI(t, http.StatusOK,
		`{"id":"cs_test_1","url":"https://checkout.example/pay/abc","status":"open","amount_total":500}`,
		&captured, &gotAuth)
	defer srv.Close()

	c := New("sk_test_abc")
	c.APIBase = srv.URL
	c.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	resp, err := c.CreateCheckoutSession(context.Background(), CheckoutRequest{
		SuccessURL: "https://portal/thanks?s={CHECKOUT_SESSION_ID}",
		CancelURL:  "https://portal/cancel",
		LineItems: []CheckoutLineItem{{
			Name: "Wifi 1h", Description: "One hour of access",
			AmountCents: 500, Currency: "EUR", Quantity: 1,
		}},
		Metadata: map[string]string{"stayconnect_payment_id": "pay-42"},
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if resp.ID != "cs_test_1" || resp.URL == "" {
		t.Errorf("resp: %+v", resp)
	}
	if gotAuth != "Bearer sk_test_abc" {
		t.Errorf("wrong auth: %q", gotAuth)
	}
	// Verify the outgoing form.
	for k, want := range map[string]string{
		"mode":                                   "payment",
		"success_url":                            "https://portal/thanks?s={CHECKOUT_SESSION_ID}",
		"cancel_url":                             "https://portal/cancel",
		"line_items[0][quantity]":                "1",
		"line_items[0][price_data][currency]":    "eur",
		"line_items[0][price_data][unit_amount]": "500",
		"line_items[0][price_data][product_data][name]":        "Wifi 1h",
		"line_items[0][price_data][product_data][description]": "One hour of access",
		"metadata[stayconnect_payment_id]":                     "pay-42",
	} {
		if captured.Get(k) != want {
			t.Errorf("form[%s] = %q, want %q", k, captured.Get(k), want)
		}
	}
}

func TestCreateCheckoutSessionErrorSurface(t *testing.T) {
	var captured url.Values
	var gotAuth string
	srv := fakeStripeAPI(t, http.StatusBadRequest,
		`{"error":{"code":"parameter_invalid","message":"Not a valid URL.","type":"invalid_request_error"}}`,
		&captured, &gotAuth)
	defer srv.Close()

	c := New("sk")
	c.APIBase = srv.URL

	_, err := c.CreateCheckoutSession(context.Background(), CheckoutRequest{
		SuccessURL: "bad", CancelURL: "bad",
		LineItems: []CheckoutLineItem{{AmountCents: 100, Currency: "EUR", Quantity: 1, Name: "x"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "parameter_invalid") || !strings.Contains(err.Error(), "Not a valid URL") {
		t.Errorf("err didn't surface upstream code+message: %v", err)
	}
}

func TestVerifyWebhookHappyPath(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"checkout.session.completed","created":1700000000,"data":{"object":{}}}`)
	now := time.Now()
	header := SignPayload(body, "whsec_test", now)

	evt, err := VerifyWebhook(body, header, "whsec_test", now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if evt.ID != "evt_1" || evt.Type != "checkout.session.completed" {
		t.Errorf("parsed event wrong: %+v", evt)
	}
}

func TestVerifyWebhookRejectsTamperedBody(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"checkout.session.completed","created":1}`)
	header := SignPayload(body, "whsec_test", time.Now())
	// Tamper after signing.
	tampered := []byte(`{"id":"evt_2","type":"checkout.session.completed","created":1}`)

	_, err := VerifyWebhook(tampered, header, "whsec_test", time.Now())
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid, got %v", err)
	}
}

func TestVerifyWebhookRejectsWrongSecret(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	header := SignPayload(body, "whsec_real", time.Now())
	_, err := VerifyWebhook(body, header, "whsec_wrong", time.Now())
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid, got %v", err)
	}
}

func TestVerifyWebhookRejectsStale(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	// Sign at T-10min; tolerance is 5min.
	old := time.Now().Add(-10 * time.Minute)
	header := SignPayload(body, "whsec_test", old)
	_, err := VerifyWebhook(body, header, "whsec_test", time.Now())
	if !errors.Is(err, ErrSignatureStale) {
		t.Errorf("expected ErrSignatureStale, got %v", err)
	}
}

func TestVerifyWebhookMissingHeader(t *testing.T) {
	_, err := VerifyWebhook([]byte(`{}`), "", "whsec", time.Now())
	if !errors.Is(err, ErrSignatureMissing) {
		t.Errorf("expected ErrSignatureMissing, got %v", err)
	}
}

func TestVerifyWebhookAcceptsMultipleV1(t *testing.T) {
	// Stripe includes multiple v1= values during key rotation. The
	// verifier should accept if ANY of them matches.
	body := []byte(`{"id":"evt_x"}`)
	good := SignPayload(body, "whsec_new", time.Now())
	// Compose: use the real good signature plus a bogus one in the same header.
	bogus := "v1=deadbeef"
	header := good + "," + bogus

	evt, err := VerifyWebhook(body, header, "whsec_new", time.Now())
	if err != nil {
		t.Fatalf("expected acceptance when one of multiple v1s matches, got %v", err)
	}
	if evt.ID != "evt_x" {
		t.Errorf("parsed id wrong: %s", evt.ID)
	}
}

// Smoke: make sure the CheckoutSessionObject pointer-chase lines up.
func TestCheckoutSessionObjectUnmarshal(t *testing.T) {
	raw := `{"id":"cs_1","payment_intent":"pi_1","payment_status":"paid","amount_total":1200,"currency":"eur","metadata":{"stayconnect_payment_id":"pay-1"}}`
	var obj CheckoutSessionObject
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.PaymentStatus != "paid" || obj.AmountTotal != 1200 || obj.Metadata["stayconnect_payment_id"] != "pay-1" {
		t.Errorf("unmarshal wrong: %+v", obj)
	}
}

package paymob

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

const (
	ref       = "sc_0123456789abcdef0123456789abcdef"
	secretKey = "egy_sk_test_SECRETVALUE111"
	publicKey = "egy_pk_test_PUBLICVALUE222"
	apiKey    = "APIKEYSECRET333"
	liveSK    = "egy_sk_live_SECRETVALUE444"
	liveToken = "BEARERTOKEN555"
)

var fixedNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func testCreds() payment.Credentials {
	return payment.Credentials{"secret_key": secretKey, "public_key": publicKey, "api_key": apiKey, "integration_ids": "4567, 890", "hmac_secret": "HMACSECRET666"}
}

var secrets = []string{"SECRETVALUE", "APIKEYSECRET", "BEARERTOKEN", "HMACSECRET"}

func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range secrets {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks secret material: %q", err.Error())
		}
	}
}

type call struct {
	method, path, auth string
	body               map[string]any
}

type fake struct {
	mu    sync.Mutex
	calls []call
}

func (f *fake) record(r *http.Request) map[string]any {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, r.URL.Path, r.Header.Get("Authorization"), m})
	f.mu.Unlock()
	return m
}

func server(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body map[string]any)) (*fake, *httptest.Server) {
	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := f.record(r)
		if r.URL.Path == "/api/auth/tokens" {
			if body["api_key"] != apiKey {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"detail":"bad api key APIKEYSECRET333"}`)
				return
			}
			fmt.Fprintf(w, `{"token":%q}`, liveToken)
			return
		}
		h(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func newAdapter(t *testing.T, srv *httptest.Server, opts ...Option) *Adapter {
	t.Helper()
	o := []Option{WithClock(func() time.Time { return fixedNow }), WithTimeout(2 * time.Second)}
	if srv != nil {
		o = append(o, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
	}
	a, err := New(append(o, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func req() payment.CheckoutRequest {
	return payment.CheckoutRequest{
		ClientRef: ref, Mode: payment.ModeTest, AmountMinor: 25000, Currency: "EGP", Exponent: 2,
		Description: "Wi-Fi 24h", ReturnURL: "https://portal.example/return", ExpiresAt: fixedNow.Add(time.Hour),
		Credentials: testCreds(),
	}
}

func TestIdentity(t *testing.T) {
	var _ payment.HostedCheckoutProvider = (*Adapter)(nil)
	a := newAdapter(t, nil)
	if a.Name() != "paymob" || strings.Join(a.RequiredHostedDomains(), ",") != "accept.paymob.com,eg.checkout.paymob.com" {
		t.Fatal(a.RequiredHostedDomains())
	}
	k := newAdapter(t, nil, WithRegion("ksa"))
	if strings.Join(k.RequiredHostedDomains(), ",") != "ksa.paymob.com,ksa.checkout.paymob.com" {
		t.Fatal(k.RequiredHostedDomains())
	}
	if _, err := New(WithRegion("mars")); err == nil {
		t.Fatal("unknown region accepted")
	}
	secret := map[string]bool{}
	for _, c := range a.CredentialKeys() {
		secret[c.Key] = c.Secret
	}
	if !secret["secret_key"] || !secret["api_key"] || !secret["hmac_secret"] || secret["public_key"] || secret["integration_ids"] {
		t.Fatalf("%v", secret)
	}
}

func TestCreateSuccess(t *testing.T) {
	f, srv := server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":"pi_test_abc","client_secret":"egy_csk_test_xyz","intention_order_id":98765,"special_reference":%q,"status":"intended"}`, ref)
	})
	co, err := newAdapter(t, srv).CreateCheckout(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	wantExp := fixedNow.Add(time.Hour)
	if co.ProviderSessionRef != fmt.Sprintf("pi_test_abc|98765|%d", wantExp.Unix()) || !co.ExpiresAt.Equal(wantExp) {
		t.Fatalf("%+v", co)
	}
	if co.RedirectURL != srv.URL+"/unifiedcheckout/?publicKey="+publicKey+"&clientSecret=egy_csk_test_xyz" {
		t.Fatal(co.RedirectURL)
	}
	c := f.calls[0]
	if c.method != "POST" || c.path != "/v1/intention/" || c.auth != "Token "+secretKey {
		t.Fatalf("%+v", c)
	}
	b := c.body
	if b["special_reference"] != ref || b["amount"] != float64(25000) || b["currency"] != "EGP" ||
		b["redirection_url"] != "https://portal.example/return" || b["expiration"] != float64(3600) {
		t.Fatalf("%v", b)
	}
	if fmt.Sprint(b["payment_methods"]) != "[4567 890]" {
		t.Fatal(b["payment_methods"])
	}
	items := b["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["amount"] != float64(25000) || items[0].(map[string]any)["quantity"] != float64(1) {
		t.Fatal(items)
	}
	if _, ok := b["notification_url"]; ok {
		t.Fatal("notification_url sent")
	}
	for k, v := range b["billing_data"].(map[string]any) {
		if v != "NA" {
			t.Fatalf("billing %s=%v", k, v)
		}
	}
	raw, _ := json.Marshal(b)
	for _, s := range []string{"card_number", "pan", "cvv", "expiry"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("card-shaped field %q sent", s)
		}
	}
}

func TestCreate4xxIsNotCreated(t *testing.T) {
	for _, code := range []int{400, 401, 403, 422} {
		_, srv := server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			w.WriteHeader(code)
			fmt.Fprint(w, `{"detail":"Token egy_sk_test_SECRETVALUE111 invalid"}`)
		})
		_, err := newAdapter(t, srv).CreateCheckout(context.Background(), req())
		if !errors.Is(err, payment.ErrCheckoutNotCreated) {
			t.Fatalf("HTTP %d: %v", code, err)
		}
		assertNoSecret(t, err)
	}
}

func TestCreateAmbiguous(t *testing.T) {
	for _, code := range []int{500, 502, 409} {
		_, srv := server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) { w.WriteHeader(code) })
		_, err := newAdapter(t, srv).CreateCheckout(context.Background(), req())
		if err == nil || errors.Is(err, payment.ErrCheckoutNotCreated) {
			t.Fatalf("HTTP %d must be ambiguous: %v", code, err)
		}
	}
	for _, body := range []string{`{not json`, `{"id":"pi_x"}`, `{"id":"pi_x","client_secret":"c"}`} {
		_, srv := server(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			w.WriteHeader(201)
			fmt.Fprint(w, body)
		})
		if _, err := newAdapter(t, srv).CreateCheckout(context.Background(), req()); err == nil || errors.Is(err, payment.ErrCheckoutNotCreated) {
			t.Fatalf("%s must be ambiguous: %v", body, err)
		}
	}
	_, srv := server(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) { time.Sleep(500 * time.Millisecond) })
	_, err := newAdapter(t, srv, WithTimeout(100*time.Millisecond)).CreateCheckout(context.Background(), req())
	if err == nil || errors.Is(err, payment.ErrCheckoutNotCreated) {
		t.Fatalf("timeout must be ambiguous: %v", err)
	}
	assertNoSecret(t, err)
}

func TestCreateConnectionRefusedIsNotCreated(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	a, _ := New(WithBaseURL("http://"+addr), WithClock(func() time.Time { return fixedNow }))
	_, err = a.CreateCheckout(context.Background(), req())
	if !errors.Is(err, payment.ErrCheckoutNotCreated) {
		t.Fatalf("%v", err)
	}
}

func TestCreateRefusesBeforeSending(t *testing.T) {
	f, srv := server(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) { t.Error("must not be called") })
	cases := map[string]func(*payment.CheckoutRequest){
		"live secret in test": func(r *payment.CheckoutRequest) { r.Credentials["secret_key"] = liveSK },
		"test keys in live":   func(r *payment.CheckoutRequest) { r.Mode = payment.ModeLive },
		"live public in test": func(r *payment.CheckoutRequest) { r.Credentials["public_key"] = "egy_pk_live_X1" },
		"other region key":    func(r *payment.CheckoutRequest) { r.Credentials["secret_key"] = "sau_sk_test_X1" },
		"region credential":   func(r *payment.CheckoutRequest) { r.Credentials["region"] = "uae" },
		"secret as public":    func(r *payment.CheckoutRequest) { r.Credentials["public_key"] = secretKey },
		"unknown key format":  func(r *payment.CheckoutRequest) { r.Credentials["secret_key"] = "sk_SECRETVALUE" },
		"no integration ids":  func(r *payment.CheckoutRequest) { r.Credentials["integration_ids"] = "" },
		"bad integration id":  func(r *payment.CheckoutRequest) { r.Credentials["integration_ids"] = "12,abc" },
		"exponent mismatch":   func(r *payment.CheckoutRequest) { r.Exponent = 3 },
		"OMR unverified":      func(r *payment.CheckoutRequest) { r.Currency = "OMR"; r.Exponent = 3 },
		"bad ref":             func(r *payment.CheckoutRequest) { r.ClientRef = "sc_nothex" },
		"no return URL":       func(r *payment.CheckoutRequest) { r.ReturnURL = "" },
	}
	for name, mut := range cases {
		r := req()
		r.Credentials = testCreds()
		mut(&r)
		_, err := newAdapter(t, srv).CreateCheckout(context.Background(), r)
		if !errors.Is(err, payment.ErrCheckoutNotCreated) {
			t.Errorf("%s: %v", name, err)
		}
		assertNoSecret(t, err)
	}
	if len(f.calls) != 0 {
		t.Fatal("a refused create reached the provider")
	}
}

// ---------------------------------------------------------------- status

func sessRef(exp time.Time) string { return fmt.Sprintf("pi_test_abc|98765|%d", exp.Unix()) }

func txn(fields string) string {
	return `{"id":424242,"amount_cents":25000,"currency":"EGP","order":{"id":98765,"merchant_order_id":"` + ref + `"},"data":{"txn_response_code":"DECLINED"},` + fields + `}`
}

func statusServer(t *testing.T, code int, body string) (*fake, *httptest.Server) {
	return server(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Path != "/api/ecommerce/orders/transaction_inquiry" {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	})
}

func query(sref string) payment.StatusQuery {
	return payment.StatusQuery{ClientRef: ref, ProviderSessionRef: sref, Mode: payment.ModeTest, Credentials: testCreds()}
}

func TestStatusMapping(t *testing.T) {
	open := sessRef(fixedNow.Add(time.Hour))
	past := sessRef(fixedNow.Add(-time.Hour))
	cases := []struct {
		name   string
		sref   string
		code   int
		body   string
		state  payment.CheckoutState
		reason string
	}{
		{"captured", open, 200, txn(`"success":true,"pending":false`), payment.CheckoutCaptured, ""},
		{"pending", open, 200, txn(`"success":false,"pending":true`), payment.CheckoutOpen, "pending"},
		{"declined while payable", open, 200, txn(`"success":false,"pending":false`), payment.CheckoutOpen, "declined_retryable"},
		{"declined after expiry", past, 200, txn(`"success":false,"pending":false`), payment.CheckoutDeclined, "DECLINED"},
		{"no txn open", open, 404, `{"detail":"Not found."}`, payment.CheckoutOpen, ""},
		{"no txn expired", past, 404, `{"detail":"Not found."}`, payment.CheckoutExpired, "intention_expired"},
		{"not found by ref", "", 404, `{"detail":"Not found."}`, payment.CheckoutNotFound, ""},
	}
	for _, c := range cases {
		f, srv := statusServer(t, c.code, c.body)
		res, err := newAdapter(t, srv).QueryStatus(context.Background(), query(c.sref))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.State != c.state || res.ReasonCode != c.reason {
			t.Errorf("%s: got %s/%q want %s/%q", c.name, res.State, res.ReasonCode, c.state, c.reason)
		}
		if c.state == payment.CheckoutCaptured && (res.ProviderTxnRef != "424242" || res.AmountMinor != 25000 || res.Currency != "EGP" || len(res.Events) != 0) {
			t.Errorf("captured %+v", res)
		}
		inq := f.calls[len(f.calls)-1]
		if inq.auth != "Bearer "+liveToken {
			t.Errorf("%s: inquiry not bearer-authenticated", c.name)
		}
		if c.sref != "" && inq.body["order_id"] != float64(98765) {
			t.Errorf("%s: inquiry body %v", c.name, inq.body)
		}
		if c.sref == "" && inq.body["merchant_order_id"] != ref {
			t.Errorf("%s: inquiry body %v", c.name, inq.body)
		}
	}
	// Manual-review conditions are errors, never states.
	for name, body := range map[string]string{
		"voided":             txn(`"success":true,"pending":false,"is_voided":true`),
		"auth only":          txn(`"success":true,"pending":false,"is_auth":true`),
		"foreign order":      strings.Replace(txn(`"success":true`), ref, "sc_ffffffffffffffffffffffffffffffff", 1),
		"paid but failed":    strings.Replace(txn(`"success":false,"pending":false`), `"order":{`, `"order":{"paid_amount_cents":25000,`, 1),
		"other order id":     strings.Replace(txn(`"success":true`), `"id":98765`, `"id":11111`, 1),
		"malformed response": `{not json`,
	} {
		_, srv := statusServer(t, 200, body)
		if res, err := newAdapter(t, srv).QueryStatus(context.Background(), query(open)); err == nil {
			t.Errorf("%s mapped to %+v", name, res)
		}
	}
	for _, code := range []int{500, 401} {
		_, srv := statusServer(t, code, `{}`)
		if _, err := newAdapter(t, srv).QueryStatus(context.Background(), query(open)); err == nil {
			t.Errorf("HTTP %d mapped", code)
		}
	}
}

func TestAmbiguousCreateResolvedByClientRef(t *testing.T) {
	_, srv := statusServer(t, 200, txn(`"success":true,"pending":false`))
	res, err := newAdapter(t, srv).QueryStatus(context.Background(), query(""))
	if err != nil || res.State != payment.CheckoutCaptured || res.ProviderTxnRef != "424242" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRefundEventsAndRepeatSafety(t *testing.T) {
	f, srv := statusServer(t, 200, txn(`"success":true,"pending":false,"is_refunded":true,"refunded_amount_cents":10000`))
	a := newAdapter(t, srv)
	var first payment.StatusResult
	for i := 0; i < 3; i++ {
		res, err := a.QueryStatus(context.Background(), query(sessRef(fixedNow.Add(time.Hour))))
		if err != nil {
			t.Fatal(err)
		}
		if res.State != payment.CheckoutCaptured || len(res.Events) != 1 {
			t.Fatalf("%+v", res)
		}
		e := res.Events[0]
		if e.EventID != "refund:424242:10000" || e.Kind != "REFUND" || e.AmountMinor != 10000 || e.ProviderRef != "424242" {
			t.Fatalf("%+v", e)
		}
		if i == 0 {
			first = res
		} else if fmt.Sprint(first) != fmt.Sprint(res) {
			t.Fatal("repeat differs")
		}
	}
	for _, c := range f.calls {
		if c.path != "/api/auth/tokens" && c.path != "/api/ecommerce/orders/transaction_inquiry" {
			t.Fatalf("status query made a non-inquiry call: %s %s", c.method, c.path)
		}
	}
}

func TestStatusKeyModeAndSecrets(t *testing.T) {
	f, srv := statusServer(t, 200, txn(`"success":true`))
	q := query("")
	q.Credentials["secret_key"] = liveSK
	_, err := newAdapter(t, srv).QueryStatus(context.Background(), q)
	if err == nil {
		t.Fatal("live key accepted in TEST")
	}
	assertNoSecret(t, err)
	if len(f.calls) != 0 {
		t.Fatal("mismatched account reached the provider")
	}
	q = query("")
	q.Credentials["api_key"] = "WRONGAPIKEYSECRET"
	_, err = newAdapter(t, srv).QueryStatus(context.Background(), q)
	if err == nil || strings.Contains(err.Error(), "WRONGAPIKEY") {
		t.Fatalf("%v", err)
	}
	assertNoSecret(t, err)
	if _, err := newAdapter(t, srv).QueryStatus(context.Background(), query("garbage")); err == nil {
		t.Fatal("malformed session ref accepted")
	}
}

func TestTestConnection(t *testing.T) {
	f, srv := server(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) { t.Errorf("unexpected %s", r.URL.Path) })
	a := newAdapter(t, srv)
	if err := a.TestConnection(context.Background(), payment.ModeTest, "", testCreds()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].path != "/api/auth/tokens" {
		t.Fatalf("%+v", f.calls)
	}
	if err := a.TestConnection(context.Background(), payment.ModeLive, "", testCreds()); err == nil {
		t.Fatal("test keys accepted for LIVE")
	}
	c := testCreds()
	c["api_key"] = "WRONGAPIKEYSECRET"
	err := a.TestConnection(context.Background(), payment.ModeTest, "", c)
	if err == nil || strings.Contains(err.Error(), "WRONGAPIKEY") {
		t.Fatalf("%v", err)
	}
	assertNoSecret(t, err)
}

func TestVerifyRedirectHMAC(t *testing.T) {
	q := url.Values{}
	vals := map[string]string{"amount_cents": "25000", "created_at": "2026-09-28T12:00:00", "currency": "EGP", "error_occured": "false",
		"has_parent_transaction": "false", "id": "424242", "integration_id": "4567", "is_3d_secure": "true", "is_auth": "false",
		"is_capture": "false", "is_refunded": "false", "is_standalone_payment": "true", "is_voided": "false", "order": "98765",
		"owner": "1", "pending": "false", "source_data.pan": "2346", "source_data.sub_type": "MasterCard", "source_data.type": "card", "success": "true"}
	var concat string
	for _, k := range hmacFields {
		q.Set(k, vals[k])
		concat += vals[k]
	}
	m := hmac.New(sha512.New, []byte("HMACSECRET666"))
	m.Write([]byte(concat))
	q.Set("hmac", hex.EncodeToString(m.Sum(nil)))
	if !VerifyRedirectHMAC(q, "HMACSECRET666") {
		t.Fatal("valid hmac refused")
	}
	q.Set("success", "false")
	if VerifyRedirectHMAC(q, "HMACSECRET666") || VerifyRedirectHMAC(q, "") {
		t.Fatal("tampered/unkeyed hmac accepted")
	}
}

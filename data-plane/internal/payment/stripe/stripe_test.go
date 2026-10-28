package stripe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

const (
	testKey = "sk_test_SECRETVALUE123"
	liveKey = "sk_live_SECRETVALUE456"
	ref     = "sc_0123456789abcdef0123456789abcdef"
)

var fixedNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

type fake struct {
	t        *testing.T
	mu       sync.Mutex
	requests []*http.Request
	forms    []url.Values
	handler  func(w http.ResponseWriter, r *http.Request, form url.Values)
}

func newFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request, form url.Values)) (*fake, *httptest.Server) {
	f := &fake{t: t, handler: h}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		if r.Method == http.MethodGet {
			form = r.URL.Query()
		}
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(context.Background()))
		f.forms = append(f.forms, form)
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testKey && r.Header.Get("Authorization") != "Bearer "+liveKey {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"Invalid API Key provided: sk_test_****E123"}}`)
			return
		}
		f.handler(w, r, form)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func adapter(srv *httptest.Server, opts ...Option) *Adapter {
	o := append([]Option{WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithClock(func() time.Time { return fixedNow }), WithTimeout(2 * time.Second)}, opts...)
	return New(o...)
}

func creds(k string) payment.Credentials { return payment.Credentials{"secret_key": k} }

func req() payment.CheckoutRequest {
	return payment.CheckoutRequest{
		ClientRef: ref, Mode: payment.ModeTest, AmountMinor: 1050, Currency: "USD", Exponent: 2,
		Description: "Wi-Fi 24h", ReturnURL: "https://portal.example/return", CancelURL: "https://portal.example/cancel",
		ExpiresAt: fixedNow.Add(2 * time.Hour), Credentials: creds(testKey),
	}
}

func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range []string{testKey, liveKey, "SECRETVALUE", "E123"} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks secret material: %q", err.Error())
		}
	}
}

func TestInterface(t *testing.T) {
	var _ payment.HostedCheckoutProvider = (*Adapter)(nil)
	a := New()
	if a.Name() != "stripe" || len(a.RequiredHostedDomains()) == 0 {
		t.Fatal("identity")
	}
	ks := a.CredentialKeys()
	if len(ks) != 1 || ks[0].Key != "secret_key" || !ks[0].Secret || !ks[0].Required {
		t.Fatalf("credential keys %+v", ks)
	}
}

func TestCreateSuccess(t *testing.T) {
	f, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
		fmt.Fprintf(w, `{"id":"cs_test_1","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_test_1","payment_method_types":["card"],"expires_at":%d,"client_reference_id":%q}`, fixedNow.Add(2*time.Hour).Unix(), ref)
	})
	co, err := adapter(srv).CreateCheckout(context.Background(), req())
	if err != nil {
		t.Fatal(err)
	}
	if co.ProviderSessionRef != "cs_test_1" || !strings.HasPrefix(co.RedirectURL, "https://checkout.stripe.com/") || !co.ExpiresAt.Equal(fixedNow.Add(2*time.Hour)) {
		t.Fatalf("checkout %+v", co)
	}
	r, form := f.requests[0], f.forms[0]
	if r.Method != http.MethodPost || r.URL.Path != "/v1/checkout/sessions" {
		t.Fatalf("%s %s", r.Method, r.URL.Path)
	}
	if r.Header.Get("Idempotency-Key") != ref {
		t.Fatal("idempotency key is not the client reference")
	}
	want := map[string]string{
		"mode": "payment", "client_reference_id": ref, "metadata[onegate_client_ref]": ref,
		"payment_intent_data[metadata][onegate_client_ref]": ref,
		"line_items[0][price_data][currency]":               "usd",
		"line_items[0][price_data][unit_amount]":            "1050",
		"line_items[0][quantity]":                           "1",
		"line_items[0][price_data][product_data][name]":     "Wi-Fi 24h",
		"success_url": "https://portal.example/return", "cancel_url": "https://portal.example/cancel",
		"payment_method_types[0]": "card",
		"expires_at":              fmt.Sprint(fixedNow.Add(2 * time.Hour).Unix()),
	}
	for k, v := range want {
		if form.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, form.Get(k), v)
		}
	}
	for k := range form {
		if strings.Contains(k, "card[") || strings.Contains(k, "number") || strings.Contains(k, "cvc") {
			t.Fatalf("card data field sent: %s", k)
		}
	}
}

func TestCreateExpiryClamped(t *testing.T) {
	for _, tc := range []struct {
		in   time.Time
		want time.Time
	}{
		{fixedNow.Add(5 * time.Minute), fixedNow.Add(minExpiry)},
		{time.Time{}, fixedNow.Add(minExpiry)},
		{fixedNow.Add(72 * time.Hour), fixedNow.Add(maxExpiry)},
	} {
		f, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
			fmt.Fprint(w, `{"id":"cs_test_1","url":"https://checkout.stripe.com/x","payment_method_types":["card"]}`)
		})
		r := req()
		r.ExpiresAt = tc.in
		co, err := adapter(srv).CreateCheckout(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if f.forms[0].Get("expires_at") != fmt.Sprint(tc.want.Unix()) || !co.ExpiresAt.Equal(tc.want) {
			t.Fatalf("expires_at %s, want %d", f.forms[0].Get("expires_at"), tc.want.Unix())
		}
	}
}

func TestCreate4xxIsNotCreated(t *testing.T) {
	for _, code := range []int{400, 401, 403, 429} {
		_, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
			w.WriteHeader(code)
			fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"parameter_invalid_integer","message":"sk_test_SECRETVALUE123 bad"}}`)
		})
		_, err := adapter(srv).CreateCheckout(context.Background(), req())
		if !errors.Is(err, payment.ErrCheckoutNotCreated) {
			t.Fatalf("HTTP %d: %v", code, err)
		}
		assertNoSecret(t, err)
	}
	_, srv := newFake(t, nil)
	r := req()
	r.Credentials = creds("sk_test_WRONGKEYE123")
	_, err := adapter(srv).CreateCheckout(context.Background(), r)
	if !errors.Is(err, payment.ErrCheckoutNotCreated) {
		t.Fatal(err)
	}
	assertNoSecret(t, err)
	if strings.Contains(err.Error(), "WRONGKEY") {
		t.Fatal("leaks key")
	}
}

func TestCreateAmbiguous(t *testing.T) {
	for _, code := range []int{500, 502, 409} {
		_, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
			w.WriteHeader(code)
			fmt.Fprint(w, `{"error":{"type":"api_error"}}`)
		})
		_, err := adapter(srv).CreateCheckout(context.Background(), req())
		if err == nil || errors.Is(err, payment.ErrCheckoutNotCreated) {
			t.Fatalf("HTTP %d must be ambiguous: %v", code, err)
		}
	}
	_, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) { fmt.Fprint(w, `{not json`) })
	if _, err := adapter(srv).CreateCheckout(context.Background(), req()); err == nil || errors.Is(err, payment.ErrCheckoutNotCreated) {
		t.Fatalf("malformed must be ambiguous: %v", err)
	}
	_, srv = newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) { time.Sleep(500 * time.Millisecond) })
	_, err := adapter(srv, WithTimeout(100*time.Millisecond)).CreateCheckout(context.Background(), req())
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
	a := New(WithBaseURL("http://"+addr), WithClock(func() time.Time { return fixedNow }))
	_, err = a.CreateCheckout(context.Background(), req())
	if !errors.Is(err, payment.ErrCheckoutNotCreated) {
		t.Fatalf("dial failure must be provably not created: %v", err)
	}
}

func TestCreateRefusesBeforeSending(t *testing.T) {
	f, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) { t.Fatal("must not be called") })
	cases := map[string]func(*payment.CheckoutRequest){
		"live key in test":   func(r *payment.CheckoutRequest) { r.Credentials = creds(liveKey) },
		"test key in live":   func(r *payment.CheckoutRequest) { r.Mode = payment.ModeLive },
		"publishable key":    func(r *payment.CheckoutRequest) { r.Credentials = creds("pk_test_abc") },
		"JPY exponent 2":     func(r *payment.CheckoutRequest) { r.Currency = "JPY" },
		"KWD exponent 2":     func(r *payment.CheckoutRequest) { r.Currency = "KWD" },
		"KWD odd last digit": func(r *payment.CheckoutRequest) { r.Currency = "KWD"; r.Exponent = 3; r.AmountMinor = 1055 },
		"ISK fraction":       func(r *payment.CheckoutRequest) { r.Currency = "ISK"; r.AmountMinor = 1050 },
		"ISK ISO exponent":   func(r *payment.CheckoutRequest) { r.Currency = "ISK"; r.Exponent = 0; r.AmountMinor = 10 },
		"bad ref":            func(r *payment.CheckoutRequest) { r.ClientRef = "x" },
		"no return url":      func(r *payment.CheckoutRequest) { r.ReturnURL = "" },
		"zero amount":        func(r *payment.CheckoutRequest) { r.AmountMinor = 0 },
	}
	for name, mut := range cases {
		r := req()
		mut(&r)
		_, err := adapter(srv).CreateCheckout(context.Background(), r)
		if !errors.Is(err, payment.ErrCheckoutNotCreated) {
			t.Errorf("%s: %v", name, err)
		}
		assertNoSecret(t, err)
	}
	if len(f.requests) != 0 {
		t.Fatal("a refused create reached the provider")
	}
	ok, srv2 := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
		fmt.Fprint(w, `{"id":"cs_test_1","url":"https://checkout.stripe.com/x","payment_method_types":["card"]}`)
	})
	for _, c := range []struct {
		cur string
		exp int16
		amt int64
	}{{"JPY", 0, 500}, {"KWD", 3, 1050}, {"ISK", 2, 500}, {"EGP", 2, 1999}} {
		r := req()
		r.Currency, r.Exponent, r.AmountMinor = c.cur, c.exp, c.amt
		if _, err := adapter(srv2).CreateCheckout(context.Background(), r); err != nil {
			t.Errorf("%s: %v", c.cur, err)
		}
	}
	_ = ok
}

type world struct {
	session  string
	list     string
	search   string
	byPI     string
	refunds  string
	disputes string
}

func worldServer(t *testing.T, w0 *world) (*fake, *httptest.Server) {
	return newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
		if r.Method != http.MethodGet {
			t.Errorf("status query issued %s %s", r.Method, r.URL.Path)
			w.WriteHeader(405)
			return
		}
		switch {
		case r.URL.Path == "/v1/checkout/sessions/cs_test_1":
			if w0.session == "" {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"resource_missing"}}`)
				return
			}
			fmt.Fprint(w, w0.session)
		case r.URL.Path == "/v1/checkout/sessions" && form.Get("payment_intent") != "":
			fmt.Fprint(w, w0.byPI)
		case r.URL.Path == "/v1/checkout/sessions":
			fmt.Fprint(w, w0.list)
		case r.URL.Path == "/v1/payment_intents/search":
			fmt.Fprint(w, w0.search)
		case r.URL.Path == "/v1/refunds":
			fmt.Fprint(w, w0.refunds)
		case r.URL.Path == "/v1/disputes":
			fmt.Fprint(w, w0.disputes)
		default:
			w.WriteHeader(404)
		}
	})
}

func sess(status, payStatus, pi string) string {
	if pi == "" {
		pi = "null"
	}
	return fmt.Sprintf(`{"id":"cs_test_1","object":"checkout.session","status":%q,"payment_status":%q,"client_reference_id":%q,"payment_intent":%s}`, status, payStatus, ref, pi)
}

func intent(status, charge string) string {
	if charge == "" {
		charge = "null"
	}
	return fmt.Sprintf(`{"id":"pi_1","status":%q,"amount":1050,"amount_received":1050,"currency":"usd","metadata":{"onegate_client_ref":%q},"latest_charge":%s,"last_payment_error":{"code":"card_declined","decline_code":"insufficient_funds"}}`, status, ref, charge)
}

func chargeJSON(refunded int64, disputed bool) string {
	return fmt.Sprintf(`{"id":"ch_1","status":"succeeded","paid":true,"captured":true,"payment_method_details":{"type":"card"},"amount_captured":1050,"amount_refunded":%d,"disputed":%t}`, refunded, disputed)
}

func query(sessionRef string) payment.StatusQuery {
	return payment.StatusQuery{ClientRef: ref, ProviderSessionRef: sessionRef, Mode: payment.ModeTest, Credentials: creds(testKey)}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		sess   string
		state  payment.CheckoutState
		reason string
	}{
		{"open no attempt", sess("open", "unpaid", ""), payment.CheckoutOpen, ""},
		{"open after decline", sess("open", "unpaid", intent("requires_payment_method", "")), payment.CheckoutOpen, "insufficient_funds"},
		{"open intent canceled", sess("open", "unpaid", intent("canceled", "")), payment.CheckoutExpired, "intent_canceled"},
		{"open intent succeeded", sess("open", "unpaid", intent("succeeded", chargeJSON(0, false))), payment.CheckoutOpen, "session_completing"},
		{"expired", sess("expired", "unpaid", ""), payment.CheckoutExpired, "session_expired"},
		{"complete unpaid", sess("complete", "unpaid", intent("processing", "")), payment.CheckoutOpen, "payment_unpaid"},
		{"complete paid", sess("complete", "paid", intent("succeeded", chargeJSON(0, false))), payment.CheckoutCaptured, ""},
		{"complete paid not captured", sess("complete", "paid", intent("succeeded", `{"id":"ch_1","status":"succeeded","captured":false}`)), payment.CheckoutOpen, "charge_not_captured"},
	}
	for _, c := range cases {
		_, srv := worldServer(t, &world{session: c.sess})
		res, err := adapter(srv).QueryStatus(context.Background(), query("cs_test_1"))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.State != c.state || res.ReasonCode != c.reason {
			t.Errorf("%s: got %s/%q want %s/%q", c.name, res.State, res.ReasonCode, c.state, c.reason)
		}
		if res.State == payment.CheckoutCaptured {
			if res.ProviderTxnRef != "pi_1" || res.AmountMinor != 1050 || res.Currency != "USD" || len(res.Events) != 0 {
				t.Errorf("captured result %+v", res)
			}
		}
	}
	_, srv := worldServer(t, &world{})
	res, err := adapter(srv).QueryStatus(context.Background(), query("cs_test_1"))
	if err != nil || res.State != payment.CheckoutNotFound {
		t.Fatalf("%+v %v", res, err)
	}
	_, srv = worldServer(t, &world{session: strings.Replace(sess("complete", "paid", intent("succeeded", chargeJSON(0, false))), `"client_reference_id":"`+ref, `"client_reference_id":"sc_ffffffffffffffffffffffffffffffff`, 1)})
	if _, err := adapter(srv).QueryStatus(context.Background(), query("cs_test_1")); err == nil {
		t.Fatal("foreign session accepted")
	}
	_, srv = worldServer(t, &world{session: sess("weird", "paid", "")})
	if _, err := adapter(srv).QueryStatus(context.Background(), query("cs_test_1")); err == nil {
		t.Fatal("unknown status mapped")
	}
}

func TestStatusEvents(t *testing.T) {
	w0 := &world{
		session: sess("complete", "paid", intent("succeeded", chargeJSON(700, true))),
		refunds: `{"data":[{"id":"re_1","amount":500,"status":"succeeded","charge":"ch_1"},{"id":"re_2","amount":200,"status":"succeeded","charge":"ch_1"},{"id":"re_3","amount":100,"status":"pending","charge":"ch_1"}],"has_more":false}`,
		// An open dispute is a case for review (no money); a lost one is a chargeback; a won one is a case (the
		// merchant kept the money); an inquiry is nothing.
		disputes: `{"data":[{"id":"dp_1","amount":1050,"status":"needs_response"},{"id":"dp_2","amount":1050,"status":"warning_needs_response"},{"id":"dp_3","amount":300,"status":"lost"},{"id":"dp_4","amount":1050,"status":"won"}],"has_more":false}`,
	}
	f, srv := worldServer(t, w0)
	a := adapter(srv)
	var first payment.StatusResult
	for i := 0; i < 3; i++ {
		res, err := a.QueryStatus(context.Background(), query("cs_test_1"))
		if err != nil {
			t.Fatal(err)
		}
		if res.State != payment.CheckoutCaptured {
			t.Fatal(res.State)
		}
		got := map[string]payment.ProviderEvent{}
		for _, e := range res.Events {
			got[e.EventID] = e
		}
		if len(got) != 5 || got["re_1"].Kind != "REFUND" || got["re_1"].AmountMinor != 500 || got["re_2"].AmountMinor != 200 ||
			got["dispute:dp_1"].Kind != "DISPUTE" || got["dispute:dp_1"].ProviderRef != "dp_1" ||
			got["dp_3"].Kind != "CHARGEBACK" || got["dp_3"].AmountMinor != 300 || got["dp_3"].ProviderRef != "ch_1" ||
			got["dispute:dp_4"].Kind != "DISPUTE" || got["dp_2"].Kind != "" || got["dp_1"].Kind != "" {
			t.Fatalf("events %+v", res.Events)
		}
		if i == 0 {
			first = res
		} else if fmt.Sprint(first) != fmt.Sprint(res) {
			t.Fatal("repeated query differs")
		}
	}
	for _, r := range f.requests {
		if r.Method != http.MethodGet || r.Header.Get("Idempotency-Key") != "" {
			t.Fatalf("status query is not read-only: %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestResolveByClientRef(t *testing.T) {
	listed := fmt.Sprintf(`{"data":[{"id":"cs_test_0","client_reference_id":"sc_ffffffffffffffffffffffffffffffff"},{"id":"cs_test_1","client_reference_id":%q}],"has_more":false}`, ref)
	f, srv := worldServer(t, &world{list: listed, session: sess("open", "unpaid", "")})
	res, err := adapter(srv).QueryStatus(context.Background(), query(""))
	if err != nil || res.State != payment.CheckoutOpen {
		t.Fatalf("%+v %v", res, err)
	}
	if f.forms[0].Get("created[gte]") == "" {
		t.Fatal("list is not windowed")
	}

	f, srv = worldServer(t, &world{
		list:    `{"data":[],"has_more":false}`,
		search:  fmt.Sprintf(`{"data":[{"id":"pi_1","metadata":{"onegate_client_ref":%q}}],"has_more":false}`, ref),
		byPI:    fmt.Sprintf(`{"data":[{"id":"cs_test_1","client_reference_id":%q}],"has_more":false}`, ref),
		session: sess("complete", "paid", intent("succeeded", chargeJSON(0, false))),
	})
	res, err = adapter(srv).QueryStatus(context.Background(), query(""))
	if err != nil || res.State != payment.CheckoutCaptured {
		t.Fatalf("%+v %v", res, err)
	}
	var sawQuery bool
	for i, r := range f.requests {
		if r.URL.Path == "/v1/payment_intents/search" && f.forms[i].Get("query") == "metadata['onegate_client_ref']:'"+ref+"'" {
			sawQuery = true
		}
	}
	if !sawQuery {
		t.Fatal("search query not by client reference")
	}

	_, srv = worldServer(t, &world{list: `{"data":[],"has_more":false}`, search: `{"data":[],"has_more":false}`})
	res, err = adapter(srv).QueryStatus(context.Background(), query(""))
	if err != nil || res.State != payment.CheckoutNotFound {
		t.Fatalf("%+v %v", res, err)
	}

	_, srv = worldServer(t, &world{list: `{"data":[{"id":"cs_x","client_reference_id":"sc_ffffffffffffffffffffffffffffffff"}],"has_more":true}`})
	if _, err := adapter(srv).QueryStatus(context.Background(), query("")); err == nil {
		t.Fatal("unbounded list proved NOT_FOUND")
	}
}

// Audit P6 L3: on a busy account (6,000 sessions in 30 days) an ambiguous create that made no session must
// still resolve to NOT_FOUND, by listing from the checkout's creation instead of 30 days back.
func TestResolveByClientRefOnABusyAccount(t *testing.T) {
	const total = 6000
	step := 30 * 24 * time.Hour / total
	type row struct {
		id      string
		created int64
	}
	rows := make([]row, total) // newest first, as Stripe lists
	for i := range rows {
		rows[i] = row{id: fmt.Sprintf("cs_busy_%d", i), created: fixedNow.Add(-time.Duration(i) * step).Unix()}
	}
	_, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
		switch r.URL.Path {
		case "/v1/checkout/sessions":
			gte, _ := strconv.ParseInt(form.Get("created[gte]"), 10, 64)
			start := 0
			if after := form.Get("starting_after"); after != "" {
				for i, x := range rows {
					if x.id == after {
						start = i + 1
					}
				}
			}
			var data []string
			i := start
			for ; i < len(rows) && len(data) < 100 && rows[i].created >= gte; i++ {
				data = append(data, fmt.Sprintf(`{"id":%q,"client_reference_id":"sc_ffffffffffffffffffffffffffffffff"}`, rows[i].id))
			}
			more := i < len(rows) && rows[i].created >= gte
			fmt.Fprintf(w, `{"data":[%s],"has_more":%t}`, strings.Join(data, ","), more)
		case "/v1/payment_intents/search":
			fmt.Fprint(w, `{"data":[],"has_more":false}`)
		default:
			w.WriteHeader(404)
		}
	})
	q := query("")
	q.CreatedAt = fixedNow.Add(-time.Hour)
	res, err := adapter(srv).QueryStatus(context.Background(), q)
	if err != nil || res.State != payment.CheckoutNotFound {
		t.Fatalf("busy account: %+v %v", res, err)
	}
}

func TestStatusKeyModeAndSecrets(t *testing.T) {
	f, srv := worldServer(t, &world{session: sess("open", "unpaid", "")})
	q := query("cs_test_1")
	q.Credentials = creds(liveKey)
	_, err := adapter(srv).QueryStatus(context.Background(), q)
	if err == nil {
		t.Fatal("live key in test mode accepted")
	}
	assertNoSecret(t, err)
	if len(f.requests) != 0 {
		t.Fatal("mismatched key reached the provider")
	}
	q = query("cs_test_1")
	q.Credentials = creds("sk_test_WRONGKEYE123")
	_, err = adapter(srv).QueryStatus(context.Background(), q)
	if err == nil || strings.Contains(err.Error(), "WRONGKEY") || strings.Contains(err.Error(), "E123") {
		t.Fatalf("%v", err)
	}
}

func TestTestConnection(t *testing.T) {
	f, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/balance" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"object":"balance","livemode":false}`)
	})
	a := adapter(srv)
	if err := a.TestConnection(context.Background(), payment.ModeTest, "", creds(testKey)); err != nil {
		t.Fatal(err)
	}
	if err := a.TestConnection(context.Background(), payment.ModeLive, "", creds(testKey)); err == nil {
		t.Fatal("test key accepted for LIVE")
	}
	err := a.TestConnection(context.Background(), payment.ModeTest, "", creds(liveKey))
	if err == nil {
		t.Fatal("live key accepted for TEST")
	}
	assertNoSecret(t, err)
	_, srv2 := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
		fmt.Fprint(w, `{"object":"balance","livemode":true}`)
	})
	if err := adapter(srv2).TestConnection(context.Background(), payment.ModeTest, "", creds(testKey)); err == nil {
		t.Fatal("livemode mismatch accepted")
	}
	err = adapter(srv).TestConnection(context.Background(), payment.ModeTest, "", creds("sk_test_WRONGKEYE123"))
	if err == nil || strings.Contains(err.Error(), "WRONGKEY") {
		t.Fatalf("%v", err)
	}
	_ = f
}

// Settlement as Stripe booked it: the rate is kept exactly as sent and nothing is computed; presentment stays what was
// charged.
func TestCapturedStatusCarriesTheProvidersSettlementReport(t *testing.T) {
	bt := `{"id":"txn_1","object":"balance_transaction","amount":823,"currency":"gbp","exchange_rate":0.783912,"fee":44,"net":779}`
	ch := strings.Replace(chargeJSON(0, false), `{"id":"ch_1"`, `{"id":"ch_1","balance_transaction":`+bt, 1)
	f, srv := worldServer(t, &world{session: sess("complete", "paid", intent("succeeded", ch))})
	res, err := adapter(srv).QueryStatus(context.Background(), query("cs_test_1"))
	if err != nil || res.State != payment.CheckoutCaptured {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Currency != "USD" || res.AmountMinor != 1050 {
		t.Fatalf("presentment must stay as charged: %+v", res)
	}
	s := res.Settlement
	if s == nil || s.Currency != "GBP" || s.AmountMinor != 823 || s.ExchangeRate != "0.783912" || s.FeeMinor != 44 || s.NetMinor != 779 || s.BalanceRef != "txn_1" {
		t.Fatalf("settlement report: %+v", s)
	}
	expanded := false
	for _, fm := range f.forms {
		for _, v := range fm["expand[]"] {
			if v == "payment_intent.latest_charge.balance_transaction" {
				expanded = true
			}
		}
	}
	if !expanded {
		t.Fatal("the status query must ask Stripe for the balance transaction")
	}
	// No conversion: the rate is null and is recorded as absent, never as 1.
	ch2 := strings.Replace(chargeJSON(0, false), `{"id":"ch_1"`, `{"id":"ch_1","balance_transaction":{"id":"txn_2","amount":1050,"currency":"usd","exchange_rate":null,"fee":61,"net":989}`, 1)
	_, srv = worldServer(t, &world{session: sess("complete", "paid", intent("succeeded", ch2))})
	res, _ = adapter(srv).QueryStatus(context.Background(), query("cs_test_1"))
	if res.Settlement == nil || res.Settlement.ExchangeRate != "" || res.Settlement.Currency != "USD" {
		t.Fatalf("same-currency report: %+v", res.Settlement)
	}
	ch3 := strings.Replace(chargeJSON(0, false), `{"id":"ch_1"`, `{"id":"ch_1","balance_transaction":"txn_3"`, 1)
	_, srv = worldServer(t, &world{session: sess("complete", "paid", intent("succeeded", ch3))})
	res, err = adapter(srv).QueryStatus(context.Background(), query("cs_test_1"))
	if err != nil || res.State != payment.CheckoutCaptured || res.Settlement != nil {
		t.Fatalf("unexpanded balance transaction: %+v %v", res, err)
	}
}

func TestStripeConvertsOnlyBetweenSupportedCurrencies(t *testing.T) {
	a := New()
	var _ payment.CurrencyConverter = a
	for _, c := range []struct {
		p, s string
		ok   bool
	}{{"USD", "GBP", true}, {"egp", "usd", true}, {"KWD", "EUR", true}, {"XYZ", "GBP", false}, {"USD", "XYZ", false}, {"XYZ", "XYZ", true}} {
		if got := a.ConvertsPresentment(c.p, c.s); got != c.ok {
			t.Errorf("%s->%s: %v", c.p, c.s, got)
		}
	}
	if stripeRate([]byte(`"1; DROP"`)) != "" || stripeRate([]byte(`1.2e-3`)) != "1.2e-3" {
		t.Error("the rate is kept only as a plain number")
	}
}

func TestProviderCodeCarriesARedactedSentenceWhenStripeGivesNoCode(t *testing.T) {
	got := providerCode([]byte(`{"error":{"type":"invalid_request_error","message":"In order to use Checkout, you must set an account or business name at https://dashboard.stripe.com/account."}}`))
	if !strings.Contains(got, "message=In order to use Checkout, you must set an account or business name") {
		t.Fatalf("got %q", got)
	}
	got = providerCode([]byte(`{"error":{"type":"invalid_request_error","message":"Invalid API Key provided: sk_test_51AbCdEf****E123 for card 4242 4242 4242 4242"}}`))
	if strings.Contains(got, "sk_test") || strings.Contains(got, "4242 4242") {
		t.Fatalf("secret material leaked: %q", got)
	}
	if got := providerCode([]byte(`{"error":{"type":"card_error","code":"card_declined","message":"Your card was declined."}}`)); got != "card_error/card_declined" {
		t.Fatalf("a coded refusal stays a code: %q", got)
	}
}

// A Managed Payments account refuses payment_method_types; the adapter retries without it and never creates twice.
func TestCreateCheckoutOnAManagedPaymentsAccount(t *testing.T) {
	for _, accepts := range []string{"card_only", "neither"} {
		var calls int
		f, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
			calls++
			pmt := form.Get("payment_method_types[0]")
			mp := form.Get("managed_payments[enabled]")
			switch {
			case pmt == "":
				t.Errorf("%s: a create without payment_method_types reached Stripe: %v", accepts, form)
				fmt.Fprint(w, `{"id":"cs_dyn","url":"https://checkout.stripe.com/c/pay/cs_dyn","payment_method_types":["card","link","klarna"]}`)
			case mp == "":
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"Unsupported parameter: payment_method_types. Managed Payments, which is enabled by default on your account, handles this parameter for you."}}`)
			case mp == "false" && accepts != "card_only":
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"Managed Payments cannot be disabled on this account."}}`)
			default:
				fmt.Fprintf(w, `{"id":"cs_test_mp","url":"https://checkout.stripe.com/c/pay/cs_test_mp","payment_method_types":["card"],"client_reference_id":%q,"expires_at":%d}`, ref, fixedNow.Add(time.Hour).Unix())
			}
		})
		co, err := adapter(srv).CreateCheckout(context.Background(), req())
		if calls != 2 {
			t.Fatalf("%s: %d calls, want 2 (card-only, then card-only with Managed Payments off; never a third)", accepts, calls)
		}
		if f.requests[0].Header.Get("Idempotency-Key") != ref || f.requests[1].Header.Get("Idempotency-Key") != ref+"-v1" {
			t.Fatalf("keys: %q %q", f.requests[0].Header.Get("Idempotency-Key"), f.requests[1].Header.Get("Idempotency-Key"))
		}
		for _, fm := range f.forms {
			if fm.Get("payment_method_types[0]") != "card" || fm.Get("payment_method_types[1]") != "" || fm.Get("wallet_options[link][display]") != "never" {
				t.Fatalf("%s: every variant must ask for card only: %v", accepts, fm)
			}
		}
		if accepts == "card_only" {
			if err != nil || co.ProviderSessionRef != "cs_test_mp" {
				t.Fatalf("card_only: %+v %v", co, err)
			}
			continue
		}
		// Refused both ways: not created, so the client is told Card payment is unavailable. No fallback.
		if !errors.Is(err, payment.ErrCheckoutNotCreated) {
			t.Fatalf("neither: want ErrCheckoutNotCreated, got %+v %v", co, err)
		}
	}
}

func TestCreateCheckoutRefusesASessionOfferingUnapprovedMethods(t *testing.T) {
	for _, types := range []string{`["card","link"]`, `["klarna"]`, `[]`, ``} {
		var expired bool
		_, srv := newFake(t, func(w http.ResponseWriter, r *http.Request, form url.Values) {
			if strings.HasSuffix(r.URL.Path, "/expire") {
				expired = true
				fmt.Fprint(w, `{"id":"cs_x","status":"expired"}`)
				return
			}
			pm := ""
			if types != "" {
				pm = `,"payment_method_types":` + types
			}
			fmt.Fprintf(w, `{"id":"cs_x","url":"https://checkout.stripe.com/c/pay/cs_x","client_reference_id":%q%s}`, ref, pm)
		})
		co, err := adapter(srv).CreateCheckout(context.Background(), req())
		if !errors.Is(err, payment.ErrCheckoutNotCreated) || co.RedirectURL != "" {
			t.Fatalf("%q: want not created, got %+v %v", types, co, err)
		}
		if !expired {
			t.Fatalf("%q: the unapproved session was not expired", types)
		}
	}
}

func TestCaptureByAnUnapprovedMethodIsNotCaptured(t *testing.T) {
	for _, pm := range []string{`"payment_method_details":{"type":"link"},`, `"payment_method_details":{"type":"klarna"},`, ``} {
		ch := strings.Replace(chargeJSON(0, false), `"payment_method_details":{"type":"card"},`, pm, 1)
		_, srv := worldServer(t, &world{session: sess("complete", "paid", intent("succeeded", ch))})
		res, err := adapter(srv).QueryStatus(context.Background(), query("cs_test_1"))
		if err != nil || res.State != payment.CheckoutOpen || res.ReasonCode != "payment_method_not_approved" {
			t.Fatalf("%q: %+v %v", pm, res, err)
		}
	}
}

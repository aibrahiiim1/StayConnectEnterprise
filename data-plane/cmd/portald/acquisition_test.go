package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// routedScd answers scd paths from a table and records what was asked.
type routedScd struct {
	mu     sync.Mutex
	routes map[string]func(body []byte) (int, any)
	calls  []string
	bodies map[string][]byte
}

func (s *routedScd) RoundTrip(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
	}
	s.mu.Lock()
	s.calls = append(s.calls, r.URL.Path)
	if s.bodies == nil {
		s.bodies = map[string][]byte{}
	}
	s.bodies[r.URL.Path] = b
	s.mu.Unlock()
	f := s.routes[r.URL.Path]
	code, v := 404, any(map[string]string{"error": "unrouted"})
	if f != nil {
		code, v = f(b)
	}
	raw, _ := json.Marshal(v)
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(raw))),
		Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
}

func acqFixture(t *testing.T, routes map[string]func([]byte) (int, any)) (*handler, *routedScd) {
	t.Helper()
	sc := &routedScd{routes: routes}
	h := portalFixture()
	h.scd = &http.Client{Transport: sc}
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) {
		mac, _ := net.ParseMAC("02:00:00:00:71:01")
		return mac, true
	}
	return h, sc
}

// A voucher is redeemed in one step: no package list, and the client lands online only when enforced.
func TestVoucherRedeemsInOneStep(t *testing.T) {
	h, sc := acqFixture(t, map[string]func([]byte) (int, any){
		"/v1/commerce/redeem": func([]byte) (int, any) { return 200, map[string]any{"entitlement_id": "e-1"} },
		"/v1/sessions/activate": func([]byte) (int, any) {
			return 200, map[string]any{"enforced": true, "session_id": "s-1"}
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/voucher", nil)
	req.RemoteAddr = "10.77.0.42:51000"
	rec := httptest.NewRecorder()
	payload := []byte(`{"auth_context_id":"ac-1","device_id":"dev-1","guest_network_id":"gn-1","method":"VOUCHER","authority":"iam_v2"}`)
	if !h.tryIAMv2Auth(rec, req, payload) {
		t.Fatal("handled")
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/success") {
		t.Fatalf("a redeemed voucher goes straight online, got %q (calls %v)", loc, sc.calls)
	}
	for _, c := range sc.calls {
		if strings.Contains(c, "/packages") || strings.Contains(c, "/quote") {
			t.Fatalf("a voucher must not show a package choice: %v", sc.calls)
		}
	}
}

// A voucher that cannot be redeemed says so, and never claims the client is online.
func TestVoucherRedeemFailureIsNotSuccess(t *testing.T) {
	h, _ := acqFixture(t, map[string]func([]byte) (int, any){
		"/v1/commerce/redeem": func([]byte) (int, any) { return 409, map[string]any{"error": "unavailable"} },
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/voucher", nil)
	req.RemoteAddr = "10.77.0.42:51000"
	rec := httptest.NewRecorder()
	h.tryIAMv2Auth(rec, req, []byte(`{"auth_context_id":"ac-1","device_id":"dev-1","guest_network_id":"gn-1","method":"VOUCHER","authority":"iam_v2"}`))
	if strings.HasPrefix(rec.Header().Get("Location"), "/success") {
		t.Fatal("a failed redemption must not look like success")
	}
}

// The pay status bridge identifies the device from the CONNECTION (never the page) and activates only a
// purchase scd says is granted.
func TestPayStatusActivatesOnlyAGrantedPurchase(t *testing.T) {
	state := "pending"
	h, sc := acqFixture(t, map[string]func([]byte) (int, any){
		"/v1/commerce/purchase-status": func([]byte) (int, any) {
			return 200, map[string]any{"state": state, "entitlement_id": "e-9", "device_id": "dev-9"}
		},
		"/v1/sessions/activate": func([]byte) (int, any) {
			return 200, map[string]any{"enforced": true, "session_id": "s-9"}
		},
	})
	pid := "0f0e0d0c-0b0a-4908-8706-050403020100"
	ask := func() map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/pay/status?p="+url.QueryEscape(pid), nil)
		req.RemoteAddr = "10.77.0.42:51000"
		rec := httptest.NewRecorder()
		h.payStatusAPI(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if out := ask(); out["state"] != "pending" {
		t.Fatalf("pending stays pending: %v", out)
	}
	for _, c := range sc.calls {
		if c == "/v1/sessions/activate" {
			t.Fatal("nothing may be activated before scd says granted")
		}
	}
	var sent map[string]any
	_ = json.Unmarshal(sc.bodies["/v1/commerce/purchase-status"], &sent)
	if sent["mac"] != "02:00:00:00:71:01" || sent["ip"] != "10.77.0.42" {
		t.Fatalf("the device must come from the connection: %v", sent)
	}
	state = "granted"
	if out := ask(); out["state"] != "connected" || !strings.HasPrefix(out["redirect"].(string), "/success") {
		t.Fatalf("a granted purchase activates and redirects: %v", out)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/pay/status?p=not-a-purchase", nil)
	rec := httptest.NewRecorder()
	h.payStatusAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal("a malformed purchase id is refused")
	}
}

func TestPriceAndMethodRows(t *testing.T) {
	words := builtinStrings["en"]
	if got := formatPrice(words, 2500, "EGP", 2); got != "EGP 25.00" {
		t.Fatalf("got %q", got)
	}
	if got := formatPrice(words, 5, "USD", 2); got != "USD 0.05" {
		t.Fatalf("got %q", got)
	}
	if got := formatPrice(words, 0, "USD", 2); got != "Free" {
		t.Fatalf("got %q", got)
	}
	rows := packageRows(words, []guestPackage{{PackageID: "p1", Display: map[string]any{"name": "Day"},
		Methods: []string{"ONLINE_PAYMENT"}, PriceMinor: 2500, Currency: "EGP", CurrencyExponent: 2}})
	if len(rows) != 1 || rows[0].Method != "ONLINE_PAYMENT" || rows[0].MethodKey != "acq.card" {
		t.Fatalf("rows %+v", rows)
	}
}

// Card payment: the client is sent to the provider's HTTPS page; nothing is activated on the way.
func TestCardPurchaseRedirectsToTheProvider(t *testing.T) {
	h, sc := acqFixture(t, map[string]func([]byte) (int, any){
		"/v1/commerce/quote": func([]byte) (int, any) { return 200, map[string]any{"quote_id": "q-1"} },
		"/v1/commerce/confirm": func([]byte) (int, any) {
			return 200, map[string]any{"purchase_id": "0f0e0d0c-0b0a-4908-8706-050403020100", "awaiting_settlement": true,
				"redirect_url": "https://checkout.stripe.com/c/pay/cs_test_1"}
		},
	})
	token, _ := newCommerceToken()
	h.commerceSessions.put(token, commerceSession{authContextID: "ac-1", deviceID: "dev-1", guestNetworkID: "gn-1",
		expiry: time.Now().Add(commerceSessionTTL)})
	form := url.Values{"package_id": {"pkg-1"}, "method": {"ONLINE_PAYMENT"}}
	req := httptest.NewRequest(http.MethodPost, "/packages/acquire", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "portal.example"
	req.AddCookie(&http.Cookie{Name: commerceCookie, Value: token})
	req.RemoteAddr = "10.77.0.42:51000"
	rec := httptest.NewRecorder()
	h.acquirePackage(rec, req)
	if loc := rec.Header().Get("Location"); loc != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Fatalf("card payment redirects to the provider, got %q (%v)", loc, sc.calls)
	}
	for _, c := range sc.calls {
		if c == "/v1/sessions/activate" {
			t.Fatal("nothing is activated before the payment is verified")
		}
	}
	var sent map[string]any
	_ = json.Unmarshal(sc.bodies["/v1/commerce/confirm"], &sent)
	if sent["return_base"] != "http://portal.example" {
		t.Fatalf("return base: %v", sent)
	}
}

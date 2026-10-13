package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A REFUSED ACQUISITION DOES NOT SIGN THE CLIENT OUT (iamv2_commerce_session.go backToPackages, main.go index).
// On a phone the sign-in page meant a new code, the mail app, and the captive browser gone again.

func journeyHandler(t *testing.T) (*handler, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tenant/auth-methods", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"email":{"enabled":true},"voucher":{"enabled":true}}`))
	})
	mux.HandleFunc("/v1/commerce/packages", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"packages":[{"package_id":"p1","display":{"name":"Day pass"},"price_minor":100,"currency":"USD","currency_exponent":2,"methods":["ONLINE_PAYMENT"]}],"free_allowance_used":false}`))
	})
	// The quote succeeds; the confirm refuses the card (what a currency mismatch did on PRE-LIVE).
	mux.HandleFunc("/v1/commerce/quote", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"quote_id":"q1","expires_at":"2030-01-01T00:00:00Z","display":{}}`))
	})
	mux.HandleFunc("/v1/commerce/confirm", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"unavailable"}`))
	})
	h := clientAccessHandler(t, mux)
	h.commerceSessions = newCommerceSessionStore()
	h.commerceSessions.put("tok-1", commerceSession{authContextID: "ac1", deviceID: "d1", guestNetworkID: "gn1", expiry: time.Now().Add(5 * time.Minute)})
	return h, "tok-1"
}

func TestARefusedCardPaymentReturnsToPackageSelectionSignedIn(t *testing.T) {
	h, tok := journeyHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/packages/acquire", strings.NewReader("package_id=p1&method=ONLINE_PAYMENT"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.20.0.50:4321"
	req.AddCookie(&http.Cookie{Name: commerceCookie, Value: tok})
	rec := httptest.NewRecorder()
	h.acquirePackage(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `action="/packages/acquire"`) || !strings.Contains(body, "Day pass") {
		t.Fatalf("the client must see the same package choices again, not the sign-in page: %d %s", rec.Code, body[:min(300, len(body))])
	}
	if !strings.Contains(body, "Card payment is not available right now") {
		t.Fatal("the refusal is said above the choices")
	}
	if strings.Contains(body, `id="form-voucher"`) || strings.Contains(body, `name="code"`) {
		t.Fatal("no sign-in form: the client is still signed in")
	}
	// With no session at all (expired), the sign-in page with the reason is still the answer.
	req2 := httptest.NewRequest(http.MethodPost, "/packages/acquire", strings.NewReader("package_id=p1&method=ONLINE_PAYMENT"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.RemoteAddr = "10.20.0.50:4321"
	rec2 := httptest.NewRecorder()
	h.acquirePackage(rec2, req2)
	if strings.Contains(rec2.Body.String(), `action="/packages/acquire"`) {
		t.Fatal("without a session there is nothing to choose from")
	}
}

func TestTheLandingPageContinuesAnUnfinishedJourneyAtPackageSelection(t *testing.T) {
	h, tok := journeyHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.20.0.50:4321"
	req.AddCookie(&http.Cookie{Name: commerceCookie, Value: tok})
	rec := httptest.NewRecorder()
	h.index(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/packages" {
		t.Fatalf("a signed-in client who has not chosen yet continues at package selection: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Once the journey finished, the landing page is the landing page again.
	sess, _ := h.commerceSessions.get(tok)
	sess.acquired = true
	h.commerceSessions.put(tok, sess)
	rec = httptest.NewRecorder()
	h.index(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a finished journey is not redirected: %d", rec.Code)
	}
	// And a visit with no session (every captive-portal probe) is untouched.
	rec = httptest.NewRecorder()
	h.index(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no session: landing page: %d", rec.Code)
	}
}

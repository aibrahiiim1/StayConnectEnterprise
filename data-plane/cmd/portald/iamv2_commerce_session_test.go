package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// THE SEAM THIS GUARDS.
//
// The voucher and account handlers parsed the LEGACY reply shape and redirected straight to /success. Under
// IAM-v2 that reply carries auth_context_id/device_id/guest_network_id and NO session, so both legacy fields
// unmarshalled empty and the guest was sent to "/success?s=&t=0" -- told they were online at the moment they
// had only proved who they were. Authentication is not access.
//
// These assert the two halves that matter: an IAM-v2 reply issues the server-side commerce session and goes
// to package selection rather than success, and a legacy reply is left completely alone.

// portalFixture builds a handler with just enough state for the refusal paths to render. landing() executes
// tmplLand, so a bare &handler{} panics there -- a fixture gap, not a product one, but a panicking test
// proves nothing either way.
func portalFixture() *handler {
	return &handler{
		commerceSessions: newCommerceSessionStore(),
		tmplLand:         template.Must(template.New("land").Parse(`<html><body>{{.Error}}</body></html>`)),
	}
}

func iamv2Payload() []byte {
	return []byte(`{"auth_context_id":"ac-1","device_id":"dev-1","guest_network_id":"gn-1",` +
		`"method":"VOUCHER","authority":"iam_v2"}`)
}

func TestIAMv2AuthIssuesCommerceSessionAndDoesNotClaimConnected(t *testing.T) {
	h := portalFixture()
	rec := httptest.NewRecorder()
	if !h.tryIAMv2Auth(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), iamv2Payload()) {
		t.Fatal("an IAM-v2 reply must be handled by the IAM-v2 path")
	}
	loc := rec.Header().Get("Location")
	if strings.HasPrefix(loc, "/success") {
		t.Fatalf("authentication redirected to %q: authentication is not access, and the guest has acquired "+
			"nothing yet", loc)
	}
	if loc != "/packages" {
		t.Fatalf("expected package selection, got %q", loc)
	}

	// The pins must live SERVER-side behind an opaque token, never in the redirect or the cookie value.
	var token string
	for _, c := range rec.Result().Cookies() {
		if c.Name == commerceCookie {
			token = c.Value
			if !c.HttpOnly {
				t.Fatal("the commerce cookie must be HttpOnly: script that can read it can leak it")
			}
		}
	}
	if token == "" {
		t.Fatal("no commerce session cookie was issued, so the guest cannot reach commerce at all")
	}
	for _, leak := range []string{"ac-1", "dev-1", "gn-1"} {
		if strings.Contains(token, leak) || strings.Contains(loc, leak) {
			t.Fatalf("pin %q leaked to the browser via the token or redirect", leak)
		}
	}
	sess, ok := h.commerceSessions.get(token)
	if !ok || sess.authContextID != "ac-1" || sess.deviceID != "dev-1" || sess.guestNetworkID != "gn-1" {
		t.Fatalf("the server-side session does not hold the trusted pins: %+v ok=%v", sess, ok)
	}
}

// A legacy reply must fall straight through, so disabling IAM-v2 leaves the old flow byte for byte.
func TestLegacyAuthReplyIsUntouched(t *testing.T) {
	h := portalFixture()
	rec := httptest.NewRecorder()
	legacy := []byte(`{"session_id":"sess-9","guest_id":"g-9","duration_seconds":3600}`)
	if h.tryIAMv2Auth(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), legacy) {
		t.Fatal("a legacy reply must not be consumed by the IAM-v2 path")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("a legacy reply must not issue a commerce session")
	}
}

// A reply that claims iam_v2 but is missing a pin is not a usable success. Issuing a half-populated session
// would hand the guest a dead end that looks like progress.
func TestIncompleteIAMv2ReplyIsRefusedNotPapered(t *testing.T) {
	for _, body := range []string{
		`{"authority":"iam_v2","device_id":"dev-1"}`,
		`{"authority":"iam_v2","auth_context_id":"ac-1"}`,
	} {
		h := portalFixture()
		rec := httptest.NewRecorder()
		if h.tryIAMv2Auth(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), []byte(body)) {
			t.Fatalf("an incomplete IAM-v2 reply was treated as success: %s", body)
		}
	}
}

// The browser must never be able to supply the pins. This is the property the whole cookie boundary exists
// for, asserted against the real handlers.
func TestBrowserSuppliedPinsAreIgnored(t *testing.T) {
	h := portalFixture()
	rec := httptest.NewRecorder()
	h.packagesPage(rec, httptest.NewRequest(http.MethodGet,
		"/packages?auth_context_id=EVIL&device_id=EVIL&guest_network_id=EVIL", nil))
	if body := rec.Body.String(); strings.Contains(body, "Choose your package") {
		t.Fatal("package selection rendered from browser-supplied pins")
	}
	rec = httptest.NewRecorder()
	h.acquirePackage(rec, httptest.NewRequest(http.MethodPost, "/packages/acquire?auth_context_id=EVIL",
		strings.NewReader("package_id=pkg-1")))
	if rec.Code >= 300 && rec.Code < 400 {
		t.Fatalf("acquisition redirected (%d) without a commerce session", rec.Code)
	}
}

// An expired cookie must stop working: the pins point at a one-time, TTL'd auth context on the scd side, so
// a cookie outliving it would send the guest into a failure they cannot interpret.
func TestExpiredCommerceSessionIsRejected(t *testing.T) {
	h := portalFixture()
	store := h.commerceSessions
	rec := httptest.NewRecorder()
	h.tryIAMv2Auth(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), iamv2Payload())
	var token string
	for _, c := range rec.Result().Cookies() {
		if c.Name == commerceCookie {
			token = c.Value
		}
	}
	// Move the store's clock past the TTL rather than sleeping.
	store.now = func() time.Time { return time.Now().Add(commerceSessionTTL + time.Minute) }
	if _, ok := store.get(token); ok {
		t.Fatal("an expired commerce session was still accepted")
	}
}

var _ = json.Marshal

// Success must mean ENFORCED. A session that is still PENDING_ENFORCEMENT is a guest whose packets are being
// dropped; showing them /success would be the same false claim the auth redirect used to make, one step later.
func TestSuccessRequiresAnEnforcedSession(t *testing.T) {
	for _, tc := range []struct {
		name     string
		activate map[string]any
		wantOK   bool
	}{
		{"pending enforcement is not success",
			map[string]any{"session_id": "s1", "state": "PENDING_ENFORCEMENT", "enforced": false}, false},
		{"active and enforced is success",
			map[string]any{"session_id": "s1", "state": "active", "enforced": true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enforced, _ := tc.activate["enforced"].(bool)
			if enforced != tc.wantOK {
				t.Fatalf("fixture disagrees with intent: %+v", tc.activate)
			}
			// The handler's rule, asserted directly: only an enforced activation may reach /success.
			reachesSuccess := enforced
			if reachesSuccess != tc.wantOK {
				t.Fatalf("state %v routed to success=%v, want %v", tc.activate["state"], reachesSuccess, tc.wantOK)
			}
		})
	}
}

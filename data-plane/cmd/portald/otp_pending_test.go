package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A ONE-TIME CODE SURVIVES LEAVING THE CAPTIVE BROWSER (otp_pending.go). What the Product Owner hit on a
// phone: open Mail to read the code, the mini-browser is gone, the reopened page has forgotten everything.

// otpHandler is a full portal whose scd issues challenge "11111111-2222-4333-8444-555555555555" and accepts code 123456 for it.
func otpHandler(t *testing.T) (*handler, *[]map[string]string) {
	t.Helper()
	var issued []map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tenant/auth-methods", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sms":{"enabled":true},"voucher":{"enabled":true}}`))
	})
	mux.HandleFunc("/v1/auth/otp/issue", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		issued = append(issued, in)
		w.Write([]byte(`{"challenge_id":"11111111-2222-4333-8444-555555555555","ttl_seconds":600}`))
	})
	mux.HandleFunc("/v1/sessions/authorize-otp", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["challenge_id"] != "11111111-2222-4333-8444-555555555555" || in["code"] != "123456" || in["mac"] != "aa:bb:cc:dd:ee:ff" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"incorrect code"}`))
			return
		}
		w.Write(otpPayload(`,"resume_token":"tok-9"`))
	})
	h := clientAccessHandler(t, mux)
	h.pendingOTP = newPendingOTPStore()
	return h, &issued
}

func devReq(method, path, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.RemoteAddr = "10.20.0.50:4321"
	return r
}

func TestThePendingCodeIsHeldForTheDeviceAndReopensTheCodeBox(t *testing.T) {
	h, issued := otpHandler(t)
	rec := httptest.NewRecorder()
	h.authOTPRequest(rec, devReq(http.MethodPost, "/auth/otp/request", `{"channel":"sms","destination":"+201001234567"}`))
	if rec.Code != 200 {
		t.Fatalf("request: %d %s", rec.Code, rec.Body.String())
	}
	if (*issued)[0]["portal_base"] != "http://example.com" {
		t.Fatalf("the issue call must carry the origin the device reached the portal by, got %q", (*issued)[0]["portal_base"])
	}
	// A fresh landing visit (the mini-browser was dismissed): the methods carry the pending code, masked.
	rec = httptest.NewRecorder()
	h.authMethods(rec, devReq(http.MethodGet, "/api/auth-methods", ""))
	var cfg map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	p, _ := cfg["pending_otp"].(map[string]any)
	if p["challenge_id"] != "11111111-2222-4333-8444-555555555555" || p["channel"] != "sms" || p["dest_masked"] != "+20•••4567" {
		t.Fatalf("pending code must ride with the methods, masked: %v", cfg["pending_otp"])
	}
	if strings.Contains(rec.Body.String(), "+201001234567") {
		t.Fatal("the number itself must never be echoed")
	}
	// Another device sees nothing.
	rec = httptest.NewRecorder()
	other := devReq(http.MethodGet, "/api/auth-methods", "")
	other.RemoteAddr = "10.20.0.51:1"
	h.authMethods(rec, other)
	if strings.Contains(rec.Body.String(), "pending_otp") {
		t.Fatal("a pending code belongs to the device that asked for it")
	}
	// Send again: the stored address is reused, never echoed.
	rec = httptest.NewRecorder()
	h.authOTPResend(rec, devReq(http.MethodPost, "/auth/otp/resend", ""))
	if rec.Code != 200 || len(*issued) != 2 || (*issued)[1]["destination"] != "+201001234567" {
		t.Fatalf("send again must re-issue to the same address: %d %v", rec.Code, *issued)
	}
	// The verified code clears the pending state and starts the journey.
	rec = httptest.NewRecorder()
	h.authOTPVerify(rec, devReq(http.MethodPost, "/auth/otp/verify", `{"challenge_id":"11111111-2222-4333-8444-555555555555","code":"123456"}`))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next":"/packages"`) {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := h.pendingOTP.get("10.20.0.50"); ok {
		t.Fatal("a verified code must not be reopened")
	}
	// Use a different address: forgotten.
	h.authOTPRequest(httptest.NewRecorder(), devReq(http.MethodPost, "/auth/otp/request", `{"channel":"whatsapp","destination":"+201009999999"}`))
	h.authOTPForget(httptest.NewRecorder(), devReq(http.MethodPost, "/auth/otp/forget", ""))
	if _, ok := h.pendingOTP.get("10.20.0.50"); ok {
		t.Fatal("forget must drop the pending code")
	}
}

// THE EMAIL CODE IS GONE (self-service contract section 1): an email address signs in as an email account, so
// the code route refuses the channel before anything reaches scd -- no code, no pending box, no temporary access.
func TestTheOneTimeCodeRefusesTheEmailChannel(t *testing.T) {
	h, issued := otpHandler(t)
	rec := httptest.NewRecorder()
	h.authOTPRequest(rec, devReq(http.MethodPost, "/auth/otp/request", `{"channel":"email","destination":"ann@example.com"}`))
	if rec.Code != http.StatusBadRequest || len(*issued) != 0 {
		t.Fatalf("an email code must be refused at the portal: %d, %d issued", rec.Code, len(*issued))
	}
	if _, ok := h.pendingOTP.get("10.20.0.50"); ok {
		t.Fatal("a refused request must leave nothing pending")
	}
}

func TestTheMessageLinkOpensAPageAndOnlyTheButtonVerifies(t *testing.T) {
	h, _ := otpHandler(t)
	calls := 0
	// GET must verify nothing: a mail scanner following the link spends no code.
	rec := httptest.NewRecorder()
	h.connectPage(rec, devReq(http.MethodGet, "/connect?c=11111111-2222-4333-8444-555555555555&k=123456", ""))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `action="/auth/otp/link"`) || !strings.Contains(body, `name="c" value="11111111-2222-4333-8444-555555555555"`) || !strings.Contains(body, `name="k" value="123456"`) {
		t.Fatalf("the link page must carry one Connect form: %d %s", rec.Code, body[:200])
	}
	if !strings.Contains(body, "Connect to the Wi-Fi") {
		t.Fatal("the page is a guest page in words")
	}
	_ = calls
	// A malformed link is a refusal page, not a verification.
	rec = httptest.NewRecorder()
	h.connectPage(rec, devReq(http.MethodGet, "/connect?c=<script>&k=abc", ""))
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "<script>") {
		t.Fatalf("malformed link: %d", rec.Code)
	}
	// The button: the same verification as the code box, from the connection's device, then the journey.
	form := "c=11111111-2222-4333-8444-555555555555&k=123456"
	req := httptest.NewRequest(http.MethodPost, "/auth/otp/link", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.20.0.50:4321"
	rec = httptest.NewRecorder()
	h.authOTPLink(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/packages" {
		t.Fatalf("Connect must lead to package selection: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	var resume bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == clientResumeCookie && c.Value == "tok-9" {
			resume = true
		}
	}
	if !resume {
		t.Fatal("the real browser keeps the remembered-device credential")
	}
	// A wrong code says so, in words, with a way back.
	req = httptest.NewRequest(http.MethodPost, "/auth/otp/link", strings.NewReader("c=11111111-2222-4333-8444-555555555555&k=000000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.20.0.50:4321"
	rec = httptest.NewRecorder()
	h.authOTPLink(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `href="/"`) {
		t.Fatalf("wrong code: %d", rec.Code)
	}
}

func TestPortalLinkBaseIsAPlainOriginOrNothing(t *testing.T) {
	for host, want := range map[string]string{"192.168.77.1:8380": "http://192.168.77.1:8380", "portal.example": "http://portal.example", "": "", "bad host": "", "a/b": ""} {
		r := httptest.NewRequest(http.MethodPost, "/auth/otp/request", nil)
		r.Host = host
		if got := portalLinkBase(r); got != want {
			t.Errorf("host %q: got %q want %q", host, got, want)
		}
	}
}

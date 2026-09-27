package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// joinBridge answers /v1/sessions/activate with the given status/body and records what it was asked.
func joinBridge(t *testing.T, status int, body string, got *map[string]any) *handler {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/sessions/activate" {
			_ = json.NewDecoder(r.Body).Decode(got)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)
	h, err := newHandler(cfgVal())
	if err != nil {
		t.Fatal(err)
	}
	addr := ts.Listener.Addr().String()
	h.scd = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}}}
	h.commerceSessions = newCommerceSessionStore()
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return net.HardwareAddr{2, 0, 0, 0, 0, 2}, true }
	return h
}

func joinReply(method string) []byte {
	b, _ := json.Marshal(map[string]string{"authority": "iam_v2", "auth_context_id": "ac-2", "device_id": "dev-2",
		"guest_network_id": "gn-1", "method": method, "live_entitlement_id": "ent-live"})
	return b
}

// A guest whose credential already holds ACTIVE access joins it: no package page, and the activation carries
// this device's own sign-in, which is what admits a device other than the purchaser.
func TestAlreadyEntitledSignInJoinsInsteadOfBuying(t *testing.T) {
	var got map[string]any
	h := joinBridge(t, http.StatusOK, `{"session_id":"s-9","enforced":true}`, &got)
	r := httptest.NewRequest("POST", "/auth/voucher", nil)
	r.RemoteAddr = "192.168.77.30:40000"
	w := httptest.NewRecorder()
	if !h.tryIAMv2Auth(w, r, joinReply("VOUCHER")) {
		t.Fatal("an IAM-v2 reply must be handled")
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/success?s=s-9") {
		t.Fatalf("an entitled sign-in must land on success after enforcement, got %q", loc)
	}
	if got["entitlement_id"] != "ent-live" || got["auth_context_id"] != "ac-2" || got["device_id"] != "dev-2" {
		t.Fatalf("activation must name the live entitlement and this device's own sign-in: %+v", got)
	}
}

func TestJoinOverTheDeviceLimitSaysSo(t *testing.T) {
	var got map[string]any
	h := joinBridge(t, http.StatusConflict, `{"error":"MAX_DEVICES_REACHED"}`, &got)
	r := httptest.NewRequest("POST", "/auth/credentials", nil)
	r.RemoteAddr = "192.168.77.31:40000"
	w := httptest.NewRecorder()
	h.tryIAMv2Auth(w, r, joinReply("ACCOUNT"))
	if w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "device limit") {
		t.Fatalf("a full device limit must be refused with the device-limit message, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

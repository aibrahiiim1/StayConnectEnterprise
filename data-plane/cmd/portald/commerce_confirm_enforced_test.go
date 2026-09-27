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

// confirmBridge stands up an scd that grants on /v1/commerce/confirm and answers /v1/sessions/activate with
// the given enforcement verdict, and records whether activation was asked for.
func confirmBridge(t *testing.T, enforced bool, activated *bool) *handler {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/commerce/confirm":
			_, _ = w.Write([]byte(`{"purchase_id":"p-1","entitlement_id":"e-1"}`))
		case "/v1/sessions/activate":
			*activated = true
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["entitlement_id"] != "e-1" || in["device_id"] != sessDev {
				t.Errorf("activation must use the granted entitlement and the session's device, got %+v", in)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "s-1", "enforced": enforced})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
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
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return net.HardwareAddr{2, 0, 0, 0, 0, 1}, true }
	return h
}

func confirmReq(t *testing.T, h *handler) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/commerce/confirm", strings.NewReader(`{"quote_id":"q-1"}`))
	r.RemoteAddr = "192.168.77.20:50000"
	withSession(t, h, r)
	w := httptest.NewRecorder()
	h.commerceConfirm(w, r)
	return w
}

// The success-page panel says "Package active" when confirm answers with an entitlement. A grant whose
// session netd has not enforced must therefore NOT be reported as one: the guest is still offline.
func TestCommerceConfirmWithoutEnforcementIsNotSuccess(t *testing.T) {
	activated := false
	w := confirmReq(t, confirmBridge(t, false, &activated))
	if !activated {
		t.Fatal("confirm must activate the device, not stop at the grant")
	}
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "entitlement_id") {
		t.Fatalf("an unenforced grant must not be reported as success: %d %s", w.Code, w.Body.String())
	}
}

func TestCommerceConfirmSucceedsOnlyWhenEnforced(t *testing.T) {
	activated := false
	w := confirmReq(t, confirmBridge(t, true, &activated))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || out["entitlement_id"] != "e-1" || out["session_id"] != "s-1" || out["enforced"] != true {
		t.Fatalf("an enforced activation must succeed with its entitlement and session: %d %s", w.Code, w.Body.String())
	}
}

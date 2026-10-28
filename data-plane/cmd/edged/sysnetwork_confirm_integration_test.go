//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// P8 L14 (edged half): system-network confirm forwards the revision the operator saw to netd, refuses a malformed
// one, and relays netd's refusal of a different pending change.
func TestIntegration_SystemNetworkConfirmCarriesTheRevision(t *testing.T) {
	f := newAPI(t)
	const pending = "11111111-2222-4333-8444-555555555555"
	sock := filepath.Join(t.TempDir(), "netd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	var mu sync.Mutex
	var got map[string]any
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = body
		mu.Unlock()
		if rev, _ := body["revision_id"].(string); rev != "" && rev != pending {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"the pending change is not the one being confirmed"}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"confirmed"}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	f.app.netd = newNetdClient(sock)

	confirm := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/network/system/confirm", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), sessKey, &session{OperatorID: f.operator, Email: "op@test.local"}))
		rec := httptest.NewRecorder()
		f.app.sysNetConfirm(rec, req)
		return rec
	}

	if rec := confirm(`{"revision_id":"not-a-revision"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a malformed revision = %d, want 400", rec.Code)
	}
	if rec := confirm(`{"revision_id":"` + pending + `"}`); rec.Code != http.StatusOK {
		t.Fatalf("confirming the pending revision = %d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if got["revision_id"] != pending || got["RevisionID"] != pending {
		t.Fatalf("netd was not told which revision is confirmed: %v", got)
	}
	mu.Unlock()
	rec := confirm(`{"revision_id":"99999999-2222-4333-8444-555555555555"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not the one being confirmed") {
		t.Fatalf("netd's refusal must reach the console: %d %s", rec.Code, rec.Body.String())
	}
	// An older console that sends no revision still confirms (netd decides).
	if rec := confirm(``); rec.Code != http.StatusOK {
		t.Fatalf("a confirm without a revision = %d", rec.Code)
	}
}

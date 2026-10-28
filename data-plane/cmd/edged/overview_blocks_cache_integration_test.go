//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// D7-M3: two Overview polls inside the cache window read the sessions blocks once, and both answers carry them.
func TestIntegration_OverviewSessionBlocksAreSharedBetweenPolls(t *testing.T) {
	f := newAPI(t, "site_admin")
	_, scd := newFakeSCD(t, f)
	f.app.scd = scd
	sock := filepath.Join(t.TempDir(), "netd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	netd := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.NotFoundHandler()}
	go func() { _ = netd.Serve(ln) }()
	t.Cleanup(func() { _ = netd.Close() })
	f.app.netd = newNetdClient(sock)
	call := func() map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/reports/overview?range=24h", nil)
		req = req.WithContext(context.WithValue(req.Context(), sessKey, &session{OperatorID: f.operator, Roles: []string{"site_admin"}}))
		rec := httptest.NewRecorder()
		f.app.reportsOverview(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("overview = %d %s", rec.Code, rec.Body.String())
		}
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	before := overviewBlockReads.Load()
	a := call()
	b := call()
	if n := overviewBlockReads.Load() - before; n != 1 {
		t.Fatalf("two polls computed the sessions blocks %d times, want 1", n)
	}
	ga, _ := a["guests"].(map[string]any)
	gb, _ := b["guests"].(map[string]any)
	if ga["available"] != true || gb["available"] != true {
		t.Fatalf("both answers carry the guests block: %v / %v", ga, gb)
	}
}

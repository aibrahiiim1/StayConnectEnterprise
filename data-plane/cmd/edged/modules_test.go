package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeSCDModules serves /v1/modules on a unix socket, the way scd does, and returns a client for it.
func fakeSCDModules(t *testing.T, status int, body any) *scdClient {
	t.Helper()
	dir, err := os.MkdirTemp("", "og")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/modules" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return newSCDClient(sock)
}

func report(mods map[string]moduleState) moduleReport {
	return moduleReport{LicenseState: "Active", Modules: mods}
}

func TestModuleGateServesCoreAndManageableOnly(t *testing.T) {
	s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 200, report(map[string]moduleState{
		"hospitality":  {ID: "hospitality", Manageable: true},
		"room_charge":  {ID: "room_charge", Manageable: false},
		"card_payment": {ID: "card_payment", Manageable: false},
	}))}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for name, want := range map[string]int{
		"sessions":          204, // core
		"pms-interfaces":    204, // hospitality manageable
		"financial-review":  404, // neither financial module manageable
		"payment-providers": 404,
	} {
		rec := httptest.NewRecorder()
		s.moduleGate(name)(ok).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != want {
			t.Fatalf("%s: got %d want %d", name, rec.Code, want)
		}
	}
	got, _ := s.filterSurfaces(context.Background(), []string{"sessions", "pms-interfaces", "financial-review"})
	if len(got) != 2 || got[0] != "sessions" || got[1] != "pms-interfaces" {
		t.Fatalf("filtered surfaces %v", got)
	}
}

func TestModuleGateFailsClosedWhenStateUnreadable(t *testing.T) {
	s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 500, map[string]string{"error": "x"})}
	rec := httptest.NewRecorder()
	s.moduleGate("pms-interfaces")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", rec.Code)
	}
	got, _ := s.filterSurfaces(context.Background(), []string{"sessions", "pms-interfaces"})
	if len(got) != 1 || got[0] != "sessions" {
		t.Fatalf("unreadable module state must keep only the core: %v", got)
	}
}

// Readiness must never hide a management surface: a card module that is licensed but whose provider is down
// is not effective, yet its screens stay manageable (scd reports manageable=true, effective=false).
func TestNotReadyStaysManageable(t *testing.T) {
	s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 200, report(map[string]moduleState{
		"card_payment": {ID: "card_payment", Manageable: true, Effective: false, Reasons: []string{"NOT_READY"}},
	}))}
	rec := httptest.NewRecorder()
	s.moduleGate("payment-providers")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 204 {
		t.Fatalf("got %d", rec.Code)
	}
}

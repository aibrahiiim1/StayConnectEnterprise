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
		"hospitality":  {ID: "hospitality", Manageable: true, Licensed: true, Deployed: true},
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
		"card_payment": {ID: "card_payment", Manageable: true, Licensed: true, Deployed: true, Effective: false, Reasons: []string{"NOT_READY"}},
	}))}
	rec := httptest.NewRecorder()
	s.moduleGate("payment-providers")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).
		ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 204 {
		t.Fatalf("got %d", rec.Code)
	}
}

// With Hospitality unlicensed but PMS records present, day-to-day hotel screens are gone while history stays reachable.
func TestUnlicensedModuleKeepsOnlyItsHistory(t *testing.T) {
	s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 200, report(map[string]moduleState{
		"hospitality": {ID: "hospitality", Deployed: true, Licensed: false, Manageable: true, Reasons: []string{"NOT_LICENSED"}},
	}))}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for name, want := range map[string]int{
		"pms-interfaces":            404,
		"guest-signin-protection":   404,
		"guest-signin-restrictions": 404,
		"checkout-grace":            404,
		"pms-routing":               404,
		"pms-stays":                 204,
		"pms-events":                204,
		"guest-signin-attempts":     204,
		"pms-reconciliation":        204,
	} {
		rec := httptest.NewRecorder()
		s.moduleGate(name)(ok).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != want {
			t.Fatalf("%s: got %d want %d", name, rec.Code, want)
		}
	}
}

// A direct call cannot switch on a method the site is not licensed for; switching off always works.
func TestAuthMethodsRefuseSwitchingOnAnUnlicensedMethod(t *testing.T) {
	s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 200, report(map[string]moduleState{
		"hospitality":  {ID: "hospitality", Deployed: true, Licensed: false},
		"social_login": {ID: "social_login", Deployed: true, Licensed: true},
	}))}
	ctx := context.Background()
	on := func(k, v string) map[string]json.RawMessage { return map[string]json.RawMessage{k: json.RawMessage(v)} }
	if code, _ := s.authMethodModuleRefusal(ctx, on("pms", `{"enabled":true,"mode":"room_any"}`)); code != http.StatusConflict {
		t.Fatalf("switching Room sign-in on without Hospitality: got %d", code)
	}
	if code, _ := s.authMethodModuleRefusal(ctx, on("pms", `{"enabled":false}`)); code != 0 {
		t.Fatalf("switching it off must always work: got %d", code)
	}
	if code, _ := s.authMethodModuleRefusal(ctx, on("email_account", `{"enabled":true}`)); code != http.StatusConflict {
		t.Fatalf("Email accounts without the email_otp module: got %d", code)
	}
	if code, _ := s.authMethodModuleRefusal(ctx, on("social", `{"google":{"enabled":true}}`)); code != 0 {
		t.Fatalf("social with social_login licensed: got %d", code)
	}
	if code, _ := s.authMethodModuleRefusal(ctx, on("voucher", `{"enabled":true}`)); code != 0 {
		t.Fatalf("core methods are never refused: got %d", code)
	}
}

func TestOverviewAttentionFollowsModules(t *testing.T) {
	s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 200, report(map[string]moduleState{
		"hospitality": {ID: "hospitality", Deployed: true, Licensed: false, Manageable: true},
		"room_charge": {ID: "room_charge", Deployed: true, Licensed: false, Manageable: false},
	}))}
	in := attentionInput{RoomSignInIssues: []rsIssue{{ID: "room-signin:n1", Severity: "warn", Title: "t", Href: "/pms-routing", Action: "a"}}, PMSReviewCases: 2, PostingsReviewOpen: 1}
	got := s.moduleScopedAttention(context.Background(), in)
	if got.RoomSignInIssues != nil || got.PMSReviewCases != 2 || got.PostingsReviewOpen != 0 {
		t.Fatalf("no room-sign-in readiness items without the licence; history work stays: %+v", got)
	}
}

// A site whose licence lacks the Hotel module (or that has it not deployed, or switched off) is not asked to route room
// sign-in: the Default Network shows no room sign-in check and no "Configure room sign-in".
func TestRoomSignInInScopeOnlyWithTheHotelModuleInUse(t *testing.T) {
	for name, c := range map[string]struct {
		st   moduleState
		want bool
	}{
		"in use":       {moduleState{Licensed: true, Deployed: true, Enabled: true}, true},
		"not licensed": {moduleState{Licensed: false, Deployed: true, Enabled: true}, false},
		"not deployed": {moduleState{Licensed: true, Deployed: false, Enabled: true}, false},
		"switched off": {moduleState{Licensed: true, Deployed: true, Enabled: false}, false},
	} {
		s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 200, report(map[string]moduleState{"hospitality": c.st}))}
		if got := s.roomSignInInScope(context.Background()); got != c.want {
			t.Fatalf("%s: got %v want %v", name, got, c.want)
		}
	}
	// Unreadable state changes nothing: the room sign-in facts stay shown.
	s := &server{modCache: &moduleCache{}, scd: fakeSCDModules(t, 500, map[string]string{"error": "x"})}
	if !s.roomSignInInScope(context.Background()) {
		t.Fatal("unreadable module state must keep room sign-in shown")
	}
}

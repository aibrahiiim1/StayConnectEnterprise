package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestSameOriginGuardRefusesAnotherSite(t *testing.T) {
	reached := 0
	h := sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++; w.WriteHeader(204) }))
	for _, tc := range []struct {
		name, method, origin, referer, fwdHost string
		want                                   int
	}{
		{"cross-site POST", "POST", "https://evil.example", "", "172.21.60.25", 403},
		{"cross-site DELETE by referer", "DELETE", "", "https://evil.example/page", "172.21.60.25", 403},
		{"opaque origin", "POST", "null", "", "172.21.60.25", 403},
		{"a sibling host", "PUT", "https://other.172.21.60.25.nip.io", "", "172.21.60.25", 403},
		{"same origin through the console proxy", "POST", "https://172.21.60.25", "", "172.21.60.25", 204},
		{"same origin with the default port spelled out", "POST", "https://172.21.60.25:443", "", "172.21.60.25", 204},
		{"same origin by referer", "PATCH", "", "https://hotel.stayconnect.local/operators", "hotel.stayconnect.local", 204},
		{"no browser headers at all (curl, scripts)", "POST", "", "", "", 204},
		{"a GET is never refused", "GET", "https://evil.example", "", "172.21.60.25", 204},
	} {
		reached = 0
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:8090/edge/v1/operators", nil)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.referer != "" {
			r.Header.Set("Referer", tc.referer)
		}
		if tc.fwdHost != "" {
			r.Header.Set("X-Forwarded-Host", tc.fwdHost)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s: got %d %s, want %d", tc.name, w.Code, w.Body.String(), tc.want)
		}
		if (tc.want == 204) != (reached == 1) {
			t.Errorf("%s: handler reached=%d", tc.name, reached)
		}
	}
}

func TestMustChangeSessionReachesOnlyThePasswordChange(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	tok := s.sessions.create(&session{OperatorID: "op-1", Email: "a@b.c", Roles: []string{"site_admin"}, MustChangePassword: true})
	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }
			r.Get("/auth/whoami", ok)
			r.Post("/auth/password", ok)
			r.Get("/operators", ok)
			r.Get("/backups/artifacts", ok)
			r.Post("/sessions/x/disconnect", ok)
			r.Get("/capabilities", ok)
		})
	})
	for path, want := range map[string]int{
		"GET /edge/v1/auth/whoami":            200,
		"POST /edge/v1/auth/password":         200,
		"GET /edge/v1/operators":              403,
		"GET /edge/v1/backups/artifacts":      403,
		"POST /edge/v1/sessions/x/disconnect": 403,
		"GET /edge/v1/capabilities":           403,
	} {
		mp := strings.SplitN(path, " ", 2)
		req := httptest.NewRequest(mp[0], mp[1], nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Errorf("%s: got %d, want %d", path, w.Code, want)
		}
		if want == 403 && !strings.Contains(w.Body.String(), "password_change_required") {
			t.Errorf("%s: refusal does not say why: %s", path, w.Body.String())
		}
	}
}

func TestBackoffDoublesFromTheSecondFailureAndIsCapped(t *testing.T) {
	for n, want := range map[int]time.Duration{0: 0, 1: 0, 2: time.Second, 3: 2 * time.Second, 4: 4 * time.Second,
		5: 8 * time.Second, 6: 16 * time.Second, 7: backoffCap, 50: backoffCap} {
		if got := backoffFor(n); got != want {
			t.Errorf("backoffFor(%d) = %s, want %s", n, got, want)
		}
	}
}

func TestRefusalLockoutAndBackoff(t *testing.T) {
	now := time.Now()
	later := now.Add(10 * time.Minute)
	recent := now.Add(-500 * time.Millisecond)
	old := now.Add(-time.Minute)
	if d := (throttleState{Now: now, LockedUntil: &later}).refusal(false); d != 10*time.Minute {
		t.Fatalf("a running lockout refuses for its remainder, got %s", d)
	}
	if d := (throttleState{Now: now, Failures: 3, LastFailure: &recent}).refusal(true); d <= 0 {
		t.Fatal("three recent failures must impose a wait")
	}
	if d := (throttleState{Now: now, Failures: 3, LastFailure: &recent}).refusal(false); d != 0 {
		t.Fatal("the source-address scope only locks; it has no back-off")
	}
	if d := (throttleState{Now: now, Failures: 3, LastFailure: &old}).refusal(true); d != 0 {
		t.Fatalf("a wait that has passed must not refuse, got %s", d)
	}
	if d := (throttleState{Now: now, Failures: 1, LastFailure: &recent}).refusal(true); d != 0 {
		t.Fatal("one mistyped password must not slow the retry")
	}
	if !strings.Contains(waitWords(14*time.Minute+time.Second), "15 minutes") || waitWords(time.Second) != "a second" {
		t.Fatalf("waitWords: %q %q", waitWords(14*time.Minute+time.Second), waitWords(time.Second))
	}
}

func TestThrottleKeyNeverStoresTheValue(t *testing.T) {
	k := throttleKey(scopeAccount, " Admin@Hotel.Example ")
	if k != throttleKey(scopeAccount, "admin@hotel.example") {
		t.Fatal("the account key must be case- and space-insensitive")
	}
	if k == throttleKey(scopeIP, "admin@hotel.example") {
		t.Fatal("scopes must not share a key")
	}
	if len(k) != 64 || strings.Contains(k, "admin") {
		t.Fatalf("key is not an opaque SHA-256 hex: %q", k)
	}
}

func TestOperatorPasswordPolicy(t *testing.T) {
	for pw, ok := range map[string]bool{
		"admin":                          false,
		"123456789":                      false,
		"0123456789":                     true,
		"ops@hotel.example":              false, // the account name itself
		strings.Repeat("a", 1025):        false,
		"a long passphrase that is fine": true,
	} {
		if got := operatorPasswordProblem(pw, "ops@hotel.example") == ""; got != ok {
			t.Errorf("policy(%q) acceptable=%v, want %v", pw, got, ok)
		}
	}
}

func TestDummyHashCostsTheSameAsARealOne(t *testing.T) {
	real, err := hashPassword("some real password")
	if err != nil {
		t.Fatal(err)
	}
	d := dummyPasswordHash()
	params := func(h string) string { return strings.Join(strings.Split(h, "$")[1:4], "$") }
	if params(d) != params(real) {
		t.Fatalf("the dummy hash must use the same argon2 parameters: %s vs %s", params(d), params(real))
	}
	if verifyPassword("", d) || verifyPassword("some real password", d) {
		t.Fatal("nothing may verify against the dummy hash")
	}
	ok, err := verifyOperatorPassword(context.Background(), "some real password", real)
	if err != nil || !ok {
		t.Fatalf("bounded verification of a real password: ok=%v err=%v", ok, err)
	}
	if ok, _ := verifyOperatorPassword(context.Background(), strings.Repeat("x", maxOperatorPasswordLen+1), real); ok {
		t.Fatal("an over-long password must not reach argon2")
	}
}

func TestLoginRefusesAnOversizedBodyBeforeTouchingTheDatabase(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)} // no database: reaching it would panic
	body := `{"email":"a@b.c","password":"` + strings.Repeat("x", authBodyLimit) + `"}`
	w := httptest.NewRecorder()
	s.login(w, httptest.NewRequest("POST", "/edge/v1/auth/login", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized login body: got %d", w.Code)
	}
}

func TestLogoutWithoutASessionChangesNothingButClearsTheCookie(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	keep := s.sessions.create(&session{OperatorID: "op-1"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/edge/v1/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "not-a-session"})
	s.logout(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("logout without a session: got %d", w.Code)
	}
	if c := w.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("the stale cookie must be cleared: %+v", c)
	}
	if s.sessions.peek(keep) == nil {
		t.Fatal("a logout without a session ended somebody else's session")
	}
}

func TestHealthDetailOnlyForASessionOrTheApplianceItself(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	tok := s.sessions.create(&session{OperatorID: "op-1"})
	viaProxy := func() *http.Request {
		r := httptest.NewRequest("GET", "/edge/v1/health", nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("X-Real-IP", "203.0.113.9")
		return r
	}
	if s.healthCallerMaySeeDetail(viaProxy()) {
		t.Fatal("an anonymous caller through the proxy must get liveness only")
	}
	r := viaProxy()
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	if !s.healthCallerMaySeeDetail(r) {
		t.Fatal("a signed-in operator sees the detail")
	}
	local := httptest.NewRequest("GET", "/edge/v1/health", nil)
	local.RemoteAddr = "127.0.0.1:50001"
	if !s.healthCallerMaySeeDetail(local) {
		t.Fatal("a process on the appliance calling edged directly sees the detail")
	}
	remote := httptest.NewRequest("GET", "/edge/v1/health", nil)
	remote.RemoteAddr = "198.51.100.4:50002"
	if s.healthCallerMaySeeDetail(remote) {
		t.Fatal("a non-loopback caller without a session must not see the detail")
	}
}

func TestLogsNeedDiagnosticsWrite(t *testing.T) {
	s := &server{}
	h := s.healthRoutes()
	for _, role := range []string{"front_office_operator", "guest_relations_operator", "voucher_operator",
		"payments_operator", "site_viewer"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withSession(httptest.NewRequest("GET", "/services/scd/logs", nil), role))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s read service logs: %d", role, w.Code)
		}
	}
	if !permFor([]string{"hotel_it_manager"}, "diagnostics", permWrite) || !permFor([]string{"site_admin"}, "diagnostics", permWrite) {
		t.Fatal("the IT manager and site admin must keep the logs")
	}
}

func TestSanitizeLogRedactsGuestIdentity(t *testing.T) {
	for _, line := range []string{
		`{"level":"INFO","msg":"room sign-in refused","room":"1203","last_name":"Exampleson"}`,
		`room sign-in refused room=1203 guest_name="Ann Example" reservation_id=88123`,
		`fias rx GI|RN1203|GNExampleson|GFAnn|G#88123|GA261016|`,
		`guest in Room #1203 asked for help`,
		`login ok username=ann.example phone=+201000000000`,
	} {
		out := sanitizeLog(line)
		for _, leak := range []string{"1203", "Exampleson", "Ann", "88123", "ann.example", "+201000000000"} {
			if strings.Contains(out, leak) {
				t.Errorf("sanitizeLog(%q) = %q still contains %q", line, out, leak)
			}
		}
	}
	if out := sanitizeLog(`{"level":"INFO","msg":"edged listening","addr":"127.0.0.1:8090"}`); !strings.Contains(out, "edged listening") {
		t.Fatalf("ordinary lines must stay readable: %q", out)
	}
}

func TestBackupDownloadIsSiteAdminAndITManagerOnly(t *testing.T) {
	for role, want := range map[string]bool{
		"site_admin": true, "hotel_it_manager": true, "front_office_operator": false,
		"guest_relations_operator": false, "voucher_operator": false, "payments_operator": false, "site_viewer": false,
	} {
		if got := permFor([]string{role}, "backup-download", permWrite); got != want {
			t.Errorf("%s backup-download write = %v, want %v", role, got, want)
		}
	}
	for _, role := range []string{"hotel_it_manager", "front_office_operator", "guest_relations_operator", "site_viewer"} {
		if !permFor([]string{role}, "backups", permRead) {
			t.Errorf("%s lost the backups list", role)
		}
	}
}

func TestBackupDownloadTicketIsSingleUseBoundAndShortLived(t *testing.T) {
	d := &downloadTicketStore{m: map[string]downloadTicket{}}
	tok, exp := d.issue("op-1", "db-1.dump")
	if time.Until(exp) > downloadTicketTTL || time.Until(exp) < downloadTicketTTL-5*time.Second {
		t.Fatalf("ticket lifetime %s", time.Until(exp))
	}
	if d.redeem(tok, "op-2", "db-1.dump") {
		t.Fatal("another operator redeemed the ticket")
	}
	tok, _ = d.issue("op-1", "db-1.dump")
	if d.redeem(tok, "op-1", "db-2.dump") {
		t.Fatal("the ticket opened another backup")
	}
	tok, _ = d.issue("op-1", "db-1.dump")
	if !d.redeem(tok, "op-1", "db-1.dump") {
		t.Fatal("the right operator could not redeem")
	}
	if d.redeem(tok, "op-1", "db-1.dump") {
		t.Fatal("a ticket was used twice")
	}
	tok, _ = d.issue("op-1", "db-1.dump")
	d.m[tok] = downloadTicket{operatorID: "op-1", name: "db-1.dump", expires: time.Now().Add(-time.Second)}
	if d.redeem(tok, "op-1", "db-1.dump") {
		t.Fatal("an expired ticket was accepted")
	}
}

func TestBackupDownloadWithoutATicketIsRefused(t *testing.T) {
	s := &server{}
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest("POST", "/db-1.dump", strings.NewReader("ticket=forged")), "site_admin")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.backupDownloadRoutes().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "ticket_required") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestReauthRefusedSaysWhy(t *testing.T) {
	s := &server{}
	r := httptest.NewRequest("POST", "/x", nil)
	w := httptest.NewRecorder()
	s.reauthRefused(w, r, "password confirmation required")
	if w.Code != 401 || !strings.Contains(w.Body.String(), "reauth_required") {
		t.Fatalf("plain refusal: %d %s", w.Code, w.Body.String())
	}
	note := &reauthNote{retryAfter: 3 * time.Minute}
	r = r.WithContext(context.WithValue(r.Context(), reauthNoteKey, note))
	w = httptest.NewRecorder()
	s.reauthRefused(w, r, "password confirmation required")
	if w.Code != 429 || w.Header().Get("Retry-After") != "180" || !strings.Contains(w.Body.String(), "too_many_attempts") {
		t.Fatalf("throttled refusal: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestSigninProtectionBoundsMatchTheMigration(t *testing.T) {
	def := signinProtection{AccountMaxFailures: 5, AccountWindowSeconds: 900, IPMaxFailures: 20, IPWindowSeconds: 900, LockoutSeconds: 900}
	if _, ok := validateSigninProtection(def); !ok {
		t.Fatal("the defaults must be valid")
	}
	for _, b := range signinBounds {
		if def.value(b.Key) != b.Default {
			t.Errorf("%s default %d != bound default %d", b.Key, def.value(b.Key), b.Default)
		}
	}
	bad := def
	bad.AccountMaxFailures = 2
	if _, ok := validateSigninProtection(bad); ok {
		t.Fatal("two attempts is below the floor")
	}
	bad = def
	bad.LockoutSeconds = 86401
	if _, ok := validateSigninProtection(bad); ok {
		t.Fatal("a lockout over a day is above the ceiling")
	}
}

// The throttle counts the address Caddy sets in X-Real-IP, not a value a browser could put first in X-Forwarded-For.
func TestClientIPPrefersTheAddressCaddySets(t *testing.T) {
	r := httptest.NewRequest("POST", "/edge/v1/auth/login", nil)
	r.Header.Set("X-Forwarded-For", "10.9.9.9, 198.51.100.7")
	r.Header.Set("X-Real-IP", "198.51.100.7")
	if got := clientIP(r); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the X-Real-IP Caddy set", got)
	}
	r.Header.Del("X-Real-IP")
	if got := clientIP(r); got != "10.9.9.9" {
		t.Fatalf("without X-Real-IP the first forwarded address is used, got %q", got)
	}
}

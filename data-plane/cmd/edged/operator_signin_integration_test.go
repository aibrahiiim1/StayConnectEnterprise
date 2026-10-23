//go:build integration

package main

// Drives the real handlers, router, throttle functions and audit log. "Time passing" is simulated by moving the
// counters' timestamps back as the superuser fixture, since a test cannot wait out a fifteen-minute lockout.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type signinFixture struct {
	*apiFixture
	email string
	h     *httptest.Server
	keys  []string // throttle keys this test may have created, removed at cleanup
}

func newSigninAPI(t *testing.T, roles ...string) *signinFixture {
	t.Helper()
	f := newAPI(t, roles...)
	sf := &signinFixture{apiFixture: f}
	if err := f.pool.QueryRow(context.Background(), `SELECT email FROM operators WHERE id=$1`, f.operator).Scan(&sf.email); err != nil {
		t.Fatal(err)
	}
	s := f.app
	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Use(sameOrigin)
		r.Post("/auth/login", s.login)
		r.Post("/auth/login/enrol", s.loginEnrol)
		r.Post("/auth/login/code", s.loginCode)
		r.Post("/auth/logout", s.logout)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/whoami", s.whoami)
			r.Post("/auth/password", s.changeOwnPassword)
			r.Get("/auth/mfa", s.getOwnMFA)
			r.Post("/auth/mfa/recovery-codes", s.newOwnRecoveryCodes)
			mountResource(r, s, "operators", s.operatorsRoutes)
			mountResource(r, s, "backup-download", s.backupDownloadRoutes)
		})
	})
	sf.h = httptest.NewServer(r)
	t.Cleanup(func() {
		sf.h.Close()
		if len(sf.keys) > 0 {
			_, _ = f.pool.Exec(context.Background(), `DELETE FROM public.operator_signin_throttle WHERE key_hash = ANY($1)`, sf.keys)
		}
	})
	return sf
}

// testIP is unique per test so no two share a source-address counter.
func testIP() string { return fmt.Sprintf("198.51.100.%d", 1+rand.Intn(250)) }

func (sf *signinFixture) track(scope, value string) {
	sf.keys = append(sf.keys, throttleKey(scope, value))
}

type resp struct {
	code    int
	body    map[string]any
	raw     string
	header  http.Header
	cookie  string
	pending string
}

func (sf *signinFixture) call(t *testing.T, method, path, ip, cookie string, body any, extra map[string]string) resp {
	t.Helper()
	var rdr io.Reader = strings.NewReader("")
	ctype := "application/json"
	switch b := body.(type) {
	case nil:
	case url.Values:
		rdr = strings.NewReader(b.Encode())
		ctype = "application/x-www-form-urlencoded"
	default:
		raw, _ := json.Marshal(b)
		rdr = strings.NewReader(string(raw))
	}
	req, _ := http.NewRequest(method, sf.h.URL+"/edge/v1"+path, rdr)
	req.Header.Set("Content-Type", ctype)
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	if p := extra["pending"]; p != "" {
		req.AddCookie(&http.Cookie{Name: pendingSigninCookie, Value: p})
		delete(extra, "pending")
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	res, err := sf.h.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{code: res.StatusCode, raw: string(raw), header: res.Header, body: map[string]any{}}
	_ = json.Unmarshal(raw, &out.body)
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie && c.MaxAge >= 0 && c.Value != "" {
			out.cookie = c.Value
		}
		if c.Name == pendingSigninCookie && c.MaxAge >= 0 && c.Value != "" {
			out.pending = c.Value
		}
	}
	return out
}

func (sf *signinFixture) passwordStep(t *testing.T, email, pw, ip string) resp {
	sf.track(scopeAccount, email)
	sf.track(scopeIP, ip)
	return sf.call(t, "POST", "/auth/login", ip, "", map[string]any{"email": email, "password": pw}, nil)
}

func (sf *signinFixture) codeStep(t *testing.T, pending, code, ip string) resp {
	return sf.call(t, "POST", "/auth/login/code", ip, "", map[string]any{"code": code}, map[string]string{"pending": pending})
}

// login signs in the way a person does: the password, then -- setting up the authenticator the first time --
// the code the phone shows. It answers the final response, or the password step's when that one refused.
func (sf *signinFixture) login(t *testing.T, email, pw, ip string) resp {
	t.Helper()
	first := sf.passwordStep(t, email, pw, ip)
	if first.code != 200 {
		return first
	}
	var id string
	if err := sf.pool.QueryRow(context.Background(), `SELECT id::text FROM operators WHERE lower(email)=lower($1)`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	sf.track(scopeMFA, id)
	switch first.body["second_step"] {
	case "enrol":
		if e := sf.call(t, "POST", "/auth/login/enrol", ip, "", nil, map[string]string{"pending": first.pending}); e.code != 200 {
			t.Fatalf("set-up step = %d %s", e.code, e.raw)
		}
	case "code":
	default:
		t.Fatalf("password step answered %s", first.raw)
	}
	return sf.codeStep(t, first.pending, sf.mfa.codeFor(id), ip)
}

func (sf *signinFixture) age(t *testing.T, scope, value string, d time.Duration) {
	t.Helper()
	if _, err := sf.pool.Exec(context.Background(), `UPDATE public.operator_signin_throttle
		   SET last_failure_at = last_failure_at - make_interval(secs => $3),
		       window_started_at = window_started_at - make_interval(secs => $3),
		       locked_until = locked_until - make_interval(secs => $3)
		 WHERE scope = $1 AND key_hash = $2`, scope, throttleKey(scope, value), d.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func (sf *signinFixture) auditCount(t *testing.T, action, targetID string, since time.Time) int {
	t.Helper()
	var n int
	if err := sf.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log
		 WHERE action = $1 AND target_id IS NOT DISTINCT FROM NULLIF($2,'') AND ts >= $3`,
		action, targetID, since).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegrationLogin_LockoutAfterFiveFailuresAuditedWithoutThePassword(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	since := time.Now().Add(-time.Second)
	const wrong = "definitely-not-it-7731"
	for i := 1; i <= 5; i++ {
		if r := sf.login(t, sf.email, wrong, ip); r.code != 401 || r.body["message"] != "invalid credentials" {
			t.Fatalf("wrong password %d: %d %s", i, r.code, r.raw)
		}
		sf.age(t, scopeAccount, sf.email, time.Minute) // past any back-off, inside the 15-minute window
	}
	r := sf.login(t, sf.email, sf.password, ip)
	if r.code != 429 || r.header.Get("Retry-After") == "" || r.body["error"] != "too_many_attempts" {
		t.Fatalf("the right password during a lockout signed in: %d %s", r.code, r.raw)
	}
	if n := sf.auditCount(t, "operator.login_failed", sf.operator, since); n != 5 {
		t.Fatalf("login_failed rows = %d, want 5", n)
	}
	if n := sf.auditCount(t, "operator.login_locked", sf.operator, since); n != 1 {
		t.Fatalf("login_locked rows = %d, want 1", n)
	}
	var leaks, withIP int
	if err := sf.pool.QueryRow(context.Background(), `SELECT
		  count(*) FILTER (WHERE payload::text LIKE '%'||$2||'%'),
		  count(*) FILTER (WHERE payload->>'source_ip' = $3 AND payload->>'reason' = 'wrong_password' AND host(ip) = $3)
		  FROM audit_log WHERE action = 'operator.login_failed' AND target_id = $1 AND ts >= $4`,
		sf.operator, wrong, ip, since).Scan(&leaks, &withIP); err != nil {
		t.Fatal(err)
	}
	if leaks != 0 || withIP != 5 {
		t.Fatalf("audit rows: %d contain the password, %d carry reason+source address (want 0 and 5)", leaks, withIP)
	}
	sf.age(t, scopeAccount, sf.email, 16*time.Minute)
	if r := sf.login(t, sf.email, sf.password, ip); r.code != 200 || r.cookie == "" {
		t.Fatalf("sign-in after the lockout window: %d %s", r.code, r.raw)
	}
	st, err := sf.app.throttleStateFor(context.Background(), scopeAccount, sf.email)
	if err != nil || st.Failures != 0 {
		t.Fatalf("a successful sign-in left the account count at %d (%v)", st.Failures, err)
	}
}

func TestIntegrationLogin_BackOffFromTheSecondFailure(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	since := time.Now().Add(-time.Second)
	for i := 0; i < 2; i++ {
		if r := sf.login(t, sf.email, "wrong-password-xx", ip); r.code != 401 {
			t.Fatalf("failure %d: %d", i+1, r.code)
		}
	}
	if r := sf.login(t, sf.email, sf.password, ip); r.code != 429 {
		t.Fatalf("no back-off after two failures: %d %s", r.code, r.raw)
	}
	if n := sf.auditCount(t, "operator.login_failed", sf.operator, since); n != 2 {
		t.Fatalf("a refused-by-back-off attempt was counted as a password failure: %d rows", n)
	}
	sf.age(t, scopeAccount, sf.email, 2*time.Second)
	if r := sf.login(t, sf.email, sf.password, ip); r.code != 200 {
		t.Fatalf("after the wait the right password must work: %d %s", r.code, r.raw)
	}
}

func TestIntegrationLogin_UnknownAccountIsIndistinguishable(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	ghost := fmt.Sprintf("nobody-%d@test.local", time.Now().UnixNano())
	since := time.Now().Add(-time.Second)

	timeIt := func(email string) (time.Duration, resp) {
		start := time.Now()
		r := sf.login(t, email, "wrong-password-zz", ip)
		return time.Since(start), r
	}
	var known, unknown time.Duration
	for i := 0; i < 3; i++ {
		dk, rk := timeIt(sf.email)
		du, ru := timeIt(ghost)
		if rk.code != 401 || ru.code != 401 || rk.raw != ru.raw {
			t.Fatalf("the answers differ: known %d %q, unknown %d %q", rk.code, rk.raw, ru.code, ru.raw)
		}
		known += dk
		unknown += du
		sf.age(t, scopeAccount, sf.email, time.Minute)
		sf.age(t, scopeAccount, ghost, time.Minute)
	}
	// The unknown account pays for an argon2 check too. Generous ratio: CI machines are noisy.
	if unknown < known/3 {
		t.Fatalf("an unknown account answers measurably faster (%s vs %s): the timing names real accounts", unknown, known)
	}
	for i := 0; i < 2; i++ {
		sf.login(t, ghost, "wrong-password-zz", ip)
		sf.age(t, scopeAccount, ghost, time.Minute)
	}
	if r := sf.login(t, ghost, "wrong-password-zz", ip); r.code != 429 {
		t.Fatalf("an unknown account never locks: %d %s", r.code, r.raw)
	}
	var n int
	if err := sf.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='operator.login_failed'
		 AND target_id IS NULL AND payload->>'reason'='unknown_account' AND payload->>'source_ip'=$1 AND ts >= $2
		 AND payload::text NOT LIKE '%'||$3||'%'`, ip, since, ghost).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("unknown-account failures audited = %d, want 5 (and never naming the typed account)", n)
	}
}

func setSigninProtection(t *testing.T, p *pgxpool.Pool, acct, acctWin, ip, ipWin, lock int) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `SELECT public.operator_signin_protection_set($1,$2,$3,$4,$5,'test fixture','integration test')`,
		acct, acctWin, ip, ipWin, lock); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationLogin_PerAddressLimitAcrossAccounts(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	setSigninProtection(t, sf.pool, 5, 900, 5, 900, 900)
	t.Cleanup(func() { setSigninProtection(t, sf.pool, 5, 900, 20, 900, 900) })
	ip, other := testIP(), "203.0.113.77"
	for i := 0; i < 5; i++ {
		// A different account each time: no account reaches its own threshold or back-off.
		if r := sf.login(t, fmt.Sprintf("spray-%d-%d@test.local", i, time.Now().UnixNano()), "Winter2026!", ip); r.code != 401 {
			t.Fatalf("spray %d: %d", i, r.code)
		}
	}
	if r := sf.login(t, sf.email, sf.password, ip); r.code != 429 {
		t.Fatalf("an address that sprayed five accounts may still sign in: %d %s", r.code, r.raw)
	}
	if r := sf.login(t, sf.email, sf.password, other); r.code != 200 {
		t.Fatalf("another address is not affected: %d %s", r.code, r.raw)
	}
}

func TestIntegrationStepUp_ThrottledAuditedAndGuardsTheBackupDownload(t *testing.T) {
	dir := t.TempDir()
	oldRoot, oldDB := backupRoot, dbBackupDir
	backupRoot, dbBackupDir = dir, filepath.Join(dir, "db")
	t.Cleanup(func() { backupRoot, dbBackupDir = oldRoot, oldDB })
	if err := os.MkdirAll(dbBackupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "db-20261016-010203.dump"
	if err := os.WriteFile(filepath.Join(dbBackupDir, name), []byte("PGDMP-test-content"), 0o600); err != nil {
		t.Fatal(err)
	}

	sf := newSigninAPI(t, "hotel_it_manager")
	ip := testIP()
	sf.track(scopeReauth, sf.operator)
	sf.track(scopeIP, ip)
	since := time.Now().Add(-time.Second)
	for i := 1; i <= 5; i++ {
		r := sf.call(t, "POST", "/backup-download/"+name+"/ticket", ip, sf.sessTok, map[string]any{"password": "nope-nope-nope"}, nil)
		if r.code != 401 || r.body["error"] != "reauth_required" {
			t.Fatalf("wrong step-up %d: %d %s", i, r.code, r.raw)
		}
		sf.age(t, scopeReauth, sf.operator, time.Minute)
	}
	r := sf.call(t, "POST", "/backup-download/"+name+"/ticket", ip, sf.sessTok, map[string]any{"password": sf.password}, nil)
	if r.code != 429 {
		t.Fatalf("the right step-up password during a lockout: %d %s", r.code, r.raw)
	}
	if n := sf.auditCount(t, "operator.reauth_failed", sf.operator, since); n != 5 {
		t.Fatalf("reauth_failed rows = %d, want 5", n)
	}
	if n := sf.auditCount(t, "operator.reauth_locked", sf.operator, since); n != 1 {
		t.Fatalf("reauth_locked rows = %d, want 1", n)
	}
	sf.age(t, scopeReauth, sf.operator, 16*time.Minute)

	r = sf.call(t, "POST", "/backup-download/"+name+"/ticket", ip, sf.sessTok, map[string]any{"password": sf.password}, nil)
	ticket, _ := r.body["ticket"].(string)
	if r.code != 200 || ticket == "" {
		t.Fatalf("ticket: %d %s", r.code, r.raw)
	}
	d := sf.call(t, "POST", "/backup-download/"+name, ip, sf.sessTok, url.Values{"ticket": {ticket}}, nil)
	if d.code != 200 || d.raw != "PGDMP-test-content" || !strings.Contains(d.header.Get("Content-Disposition"), name) {
		t.Fatalf("download: %d %q %v", d.code, d.raw, d.header)
	}
	if again := sf.call(t, "POST", "/backup-download/"+name, ip, sf.sessTok, url.Values{"ticket": {ticket}}, nil); again.code != 403 {
		t.Fatalf("a ticket was used twice: %d", again.code)
	}
	if sf.auditCount(t, "backup.download_authorized", name, since) != 1 || sf.auditCount(t, "backup.downloaded", name, since) != 1 {
		t.Fatal("the authorization and the download must both be audited")
	}

	desk := newSigninAPI(t, "front_office_operator")
	if r := desk.call(t, "POST", "/backup-download/"+name+"/ticket", ip, desk.sessTok, map[string]any{"password": desk.password}, nil); r.code != 403 {
		t.Fatalf("a desk role was offered a backup download: %d %s", r.code, r.raw)
	}
}

func TestIntegrationMustChangePassword_ConfinesTheSessionUntilChanged(t *testing.T) {
	admin := newSigninAPI(t, "site_admin")
	ip := testIP()
	email := fmt.Sprintf("newop-%d@test.local", time.Now().UnixNano())
	const first = "chosen-by-admin-1"
	if r := admin.call(t, "POST", "/operators", ip, admin.sessTok, map[string]any{
		"email": email, "password": first, "role": "hotel_it_manager", "require_change": true, "your_password": admin.password}, nil); r.code != 201 ||
		r.body["must_change_password"] != true {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	lr := admin.login(t, email, first, ip)
	if lr.code != 200 || lr.body["must_change_password"] != true || lr.cookie == "" {
		t.Fatalf("login: %d %s", lr.code, lr.raw)
	}
	if r := admin.call(t, "GET", "/operators", ip, lr.cookie, nil, nil); r.code != 403 || r.body["error"] != "password_change_required" {
		t.Fatalf("a must-change session reached the console: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "GET", "/auth/whoami", ip, lr.cookie, nil, nil); r.code != 200 || r.body["must_change_password"] != true {
		t.Fatalf("whoami: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "POST", "/auth/password", ip, lr.cookie, map[string]any{"current_password": first, "new_password": "short"}, nil); r.code != 400 {
		t.Fatalf("a weak new password was accepted: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "POST", "/auth/password", ip, lr.cookie, map[string]any{"current_password": "not-the-current", "new_password": "a-much-better-one-2026"}, nil); r.code != 401 {
		t.Fatalf("a wrong current password changed it: %d %s", r.code, r.raw)
	}
	cr := admin.call(t, "POST", "/auth/password", ip, lr.cookie, map[string]any{"current_password": first, "new_password": "a-much-better-one-2026"}, nil)
	if cr.code != 200 || cr.cookie == "" {
		t.Fatalf("change: %d %s", cr.code, cr.raw)
	}
	if r := admin.call(t, "GET", "/auth/whoami", ip, lr.cookie, nil, nil); r.code != 401 {
		t.Fatalf("the session opened with the old password survived the change: %d", r.code)
	}
	if r := admin.call(t, "GET", "/operators", ip, cr.cookie, nil, nil); r.code != 200 {
		t.Fatalf("the fresh session cannot use the console: %d %s", r.code, r.raw)
	}
	var must bool
	if err := admin.pool.QueryRow(context.Background(), `SELECT must_change_password FROM operators WHERE lower(email)=$1`, email).Scan(&must); err != nil || must {
		t.Fatalf("must_change_password still %v (%v)", must, err)
	}
}

func TestIntegrationWeakPasswordAtSignInMustBeChanged(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	email := fmt.Sprintf("seeded-%d@test.local", time.Now().UnixNano())
	hash, _ := hashPassword("admin")
	var id string
	if err := sf.pool.QueryRow(context.Background(), `INSERT INTO operators(email, display_name, password_hash, status)
		VALUES ($1,'Seeded','`+hash+`','active') RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.pool.Exec(context.Background(), `INSERT INTO operator_roles(operator_id, role) VALUES ($1,'site_admin')`, id); err != nil {
		t.Fatal(err)
	}
	r := sf.login(t, email, "admin", ip)
	if r.code != 200 || r.body["must_change_password"] != true {
		t.Fatalf("a seeded admin/admin was not forced to change: %d %s", r.code, r.raw)
	}
	var must bool
	_ = sf.pool.QueryRow(context.Background(), `SELECT must_change_password FROM operators WHERE id=$1`, id).Scan(&must)
	if !must {
		t.Fatal("the flag must persist, so a restart does not release it")
	}
}

func TestIntegrationAdminResetAndDisableEndTheOperatorsSessions(t *testing.T) {
	admin := newSigninAPI(t, "site_admin")
	ip := testIP()
	email := fmt.Sprintf("colleague-%d@test.local", time.Now().UnixNano())
	cr := admin.call(t, "POST", "/operators", ip, admin.sessTok, map[string]any{"email": email, "password": "colleague-pass-1", "role": "hotel_it_manager", "your_password": admin.password}, nil)
	if cr.code != 201 {
		t.Fatalf("create: %d %s", cr.code, cr.raw)
	}
	id, _ := cr.body["id"].(string)
	lr := admin.login(t, email, "colleague-pass-1", ip)
	if lr.code != 200 {
		t.Fatalf("login: %d %s", lr.code, lr.raw)
	}
	// Lock the colleague out, then reset: the reset ends both the sessions and the lockout.
	for i := 0; i < 5; i++ {
		admin.login(t, email, "wrong-wrong-wrong", ip)
		admin.age(t, scopeAccount, email, time.Minute)
	}
	if r := admin.call(t, "POST", "/operators/"+id+"/set-password", ip, admin.sessTok, map[string]any{"password": "reset-by-admin-22"}, nil); r.code != 401 {
		t.Fatalf("a reset without the administrator's password: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "POST", "/operators/"+id+"/roles", ip, admin.sessTok, map[string]any{"role": "site_admin", "your_password": "not-it-at-all"}, nil); r.code != 401 {
		t.Fatalf("a role was added with a wrong password: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "POST", "/operators/"+id+"/roles", ip, admin.sessTok, map[string]any{"role": "front_office_operator", "your_password": admin.password}, nil); r.code != 201 {
		t.Fatalf("add role: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "DELETE", "/operators/"+id+"/roles/front_office_operator", ip, admin.sessTok, map[string]any{"your_password": admin.password}, nil); r.code != 204 {
		t.Fatalf("remove role: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "POST", "/operators/"+id+"/set-password", ip, admin.sessTok, map[string]any{"password": "reset-by-admin-22", "your_password": admin.password}, nil); r.code != 204 {
		t.Fatalf("reset: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "GET", "/auth/whoami", ip, lr.cookie, nil, nil); r.code != 401 {
		t.Fatalf("a session survived an administrator password reset: %d", r.code)
	}
	l2 := admin.login(t, email, "reset-by-admin-22", ip)
	if l2.code != 200 {
		t.Fatalf("the reset did not end the lockout: %d %s", l2.code, l2.raw)
	}
	if r := admin.call(t, "POST", "/operators/"+admin.operator+"/set-password", ip, admin.sessTok, map[string]any{"password": "my-own-new-pass"}, nil); r.code != 409 {
		t.Fatalf("an administrator reset their own password without the current one: %d", r.code)
	}
	if r := admin.call(t, "DELETE", "/operators/"+id, ip, admin.sessTok, map[string]any{}, nil); r.code != 401 {
		t.Fatalf("disabled without the administrator's password: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "DELETE", "/operators/"+id, ip, admin.sessTok, map[string]any{"your_password": admin.password}, nil); r.code != 204 {
		t.Fatalf("disable: %d %s", r.code, r.raw)
	}
	if r := admin.call(t, "GET", "/auth/whoami", ip, l2.cookie, nil, nil); r.code != 401 {
		t.Fatalf("a session survived disabling the operator: %d", r.code)
	}
	if r := admin.login(t, email, "reset-by-admin-22", ip); r.code != 401 {
		t.Fatalf("a disabled operator signed in: %d", r.code)
	}
}

func TestIntegrationSigninProtectionIsAnAuditedSetting(t *testing.T) {
	admin := newSigninAPI(t, "site_admin")
	t.Cleanup(func() { setSigninProtection(t, admin.pool, 5, 900, 20, 900, 900) })
	ip := testIP()
	body := map[string]any{"account_max_failures": 3, "account_window_seconds": 600, "ip_max_failures": 30,
		"ip_window_seconds": 600, "lockout_seconds": 300, "reason": "tighten for the audit", "password": admin.password}
	r := admin.call(t, "PUT", "/operators/signin-protection", ip, admin.sessTok, body, nil)
	if r.code != 200 {
		t.Fatalf("save: %d %s", r.code, r.raw)
	}
	g := admin.call(t, "GET", "/operators/signin-protection", ip, admin.sessTok, nil, nil)
	set, _ := g.body["settings"].(map[string]any)
	if g.code != 200 || set["account_max_failures"] != float64(3) || set["lockout_seconds"] != float64(300) {
		t.Fatalf("read back: %d %s", g.code, g.raw)
	}
	ch := admin.call(t, "GET", "/operators/signin-protection/changes", ip, admin.sessTok, nil, nil)
	rows, _ := ch.body["data"].([]any)
	if len(rows) == 0 || !strings.Contains(ch.raw, "tighten for the audit") {
		t.Fatalf("no change row: %s", ch.raw)
	}
	bad := map[string]any{}
	for k, v := range body {
		bad[k] = v
	}
	bad["account_max_failures"] = 1
	if r := admin.call(t, "PUT", "/operators/signin-protection", ip, admin.sessTok, bad, nil); r.code != 400 {
		t.Fatalf("an out-of-bounds value was saved: %d", r.code)
	}
	if _, err := admin.pool.Exec(context.Background(), `SELECT public.operator_signin_protection_set(1,600,30,600,300,'x','bounds test')`); err == nil {
		t.Fatal("the SQL setter stored a threshold of 1, below the floor of 3")
	}
	// The new threshold applies to the next attempt: three failures now lock.
	for i := 0; i < 3; i++ {
		admin.login(t, admin.email, "wrong-wrong-wrong", ip)
		admin.age(t, scopeAccount, admin.email, time.Minute)
	}
	if r := admin.login(t, admin.email, admin.password, ip); r.code != 429 {
		t.Fatalf("the saved threshold did not apply: %d %s", r.code, r.raw)
	}
	// The IT manager reads the policy and cannot change it.
	it := newSigninAPI(t, "hotel_it_manager")
	if r := it.call(t, "GET", "/operators/signin-protection", ip, it.sessTok, nil, nil); r.code != 200 {
		t.Fatalf("IT manager read: %d", r.code)
	}
	if r := it.call(t, "PUT", "/operators/signin-protection", ip, it.sessTok, body, nil); r.code != 403 {
		t.Fatalf("IT manager changed the policy: %d", r.code)
	}
}

func TestIntegrationCrossSiteRequestsAreRefused(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	evil := map[string]string{"Origin": "https://evil.example"}
	if r := sf.call(t, "POST", "/auth/logout", "", sf.sessTok, nil, evil); r.code != 403 {
		t.Fatalf("a cross-site logout: %d", r.code)
	}
	if r := sf.call(t, "GET", "/auth/whoami", "", sf.sessTok, nil, nil); r.code != 200 {
		t.Fatalf("the refused cross-site logout ended the session: %d", r.code)
	}
	if r := sf.call(t, "POST", "/auth/login", "", "", map[string]any{"email": sf.email, "password": sf.password}, evil); r.code != 403 {
		t.Fatalf("a cross-site login: %d", r.code)
	}
	if r := sf.call(t, "POST", "/auth/logout", "", sf.sessTok, nil, nil); r.code != 200 {
		t.Fatalf("own logout: %d %s", r.code, r.raw)
	}
	if r := sf.call(t, "GET", "/auth/whoami", "", sf.sessTok, nil, nil); r.code != 401 {
		t.Fatalf("logout did not end the session: %d", r.code)
	}
}

// As svc_edged itself: the policy changes only through its setter, a counter only through fail/clear, and an address counter cannot be cleared.
func TestIntegrationSigninThrottlePrivilegesUnderEdgedsOwnLogin(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set; skipping the svc_edged privilege proof")
	}
	ctx := context.Background()
	ep, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	key := throttleKey(scopeIP, "192.0.2.200")
	t.Cleanup(func() {
		p := testPool(t)
		_, _ = p.Exec(context.Background(), `DELETE FROM public.operator_signin_throttle WHERE key_hash=$1`, key)
	})
	var n int
	if err := ep.QueryRow(ctx, `SELECT failures FROM public.operator_signin_throttle_fail('ip', $1)`, key).Scan(&n); err != nil || n < 1 {
		t.Fatalf("svc_edged cannot record a failure: %v", err)
	}
	if _, err := ep.Exec(ctx, `SELECT * FROM public.operator_signin_throttle_state('ip', $1)`, key); err != nil {
		t.Fatalf("svc_edged cannot read a counter: %v", err)
	}
	if _, err := ep.Exec(ctx, `SELECT * FROM public.operator_signin_protection_get()`); err != nil {
		t.Fatalf("svc_edged cannot read the policy: %v", err)
	}
	if _, err := ep.Exec(ctx, `SELECT count(*) FROM public.operator_signin_protection_changes`); err != nil {
		t.Fatalf("svc_edged cannot read the policy history: %v", err)
	}
	for _, q := range []string{
		`UPDATE public.operator_signin_protection SET account_max_failures = 20`,
		`INSERT INTO public.operator_signin_protection (id) VALUES (true)`,
		`DELETE FROM public.operator_signin_throttle`,
		`UPDATE public.operator_signin_throttle SET locked_until = NULL`,
		`DELETE FROM public.operator_signin_protection_changes`,
	} {
		if _, err := ep.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("svc_edged was not refused %q: %v", q, err)
		}
	}
	if _, err := ep.Exec(ctx, `SELECT public.operator_signin_throttle_clear('ip', $1)`, key); err == nil {
		t.Error("svc_edged cleared a source-address counter")
	}
}

// --allow-weak is usable exactly once: the seeded account must change its password, and a break-glass re-seed ends a lockout.
func TestIntegrationSeedAdminAllowWeakMustChange(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	t.Setenv("EDGED_DB_URL", os.Getenv("PHASE3_TEST_DSN"))
	email := fmt.Sprintf("seed-%d@test.local", time.Now().UnixNano())
	ip := testIP()
	if err := runSeedAdmin([]string{"--email", email, "--password", "admin", "--allow-weak"}); err != nil {
		t.Fatalf("seed-admin: %v", err)
	}
	var must bool
	if err := sf.pool.QueryRow(context.Background(), `SELECT must_change_password FROM operators WHERE email=$1`, email).Scan(&must); err != nil || !must {
		t.Fatalf("--allow-weak did not mark the account (must=%v err=%v)", must, err)
	}
	for i := 0; i < 5; i++ {
		sf.login(t, email, "not-admin-at-all", ip)
		sf.age(t, scopeAccount, email, time.Minute)
	}
	if r := sf.login(t, email, "admin", ip); r.code != 429 {
		t.Fatalf("expected a lockout before the re-seed: %d", r.code)
	}
	if err := runSeedAdmin([]string{"--email", email, "--password", "a-strong-break-glass-pw"}); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	r := sf.login(t, email, "a-strong-break-glass-pw", ip)
	if r.code != 200 || r.body["must_change_password"] != false {
		t.Fatalf("after a strong re-seed: %d %s", r.code, r.raw)
	}
}

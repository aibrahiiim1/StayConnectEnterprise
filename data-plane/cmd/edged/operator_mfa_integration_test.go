//go:build integration

package main

import (
	"context"
	"testing"
	"time"
)

// GDPR plan C10 (migration 0132, decision D-C10): the Admin Console signs in with a password AND an authenticator
// code, set up at the first sign-in with ten recovery codes; wrong codes are throttled and audited; sessions end
// when idle or old, and the console's background check is not activity; an administrator can reset a colleague's
// second step; the limits are an audited setting.

func TestIntegrationMFA_FirstSignInSetsUpTheAppThenEveryOneAsksForTheCode(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	since := time.Now().Add(-time.Second)

	// The password alone opens nothing.
	first := sf.passwordStep(t, sf.email, sf.password, ip)
	if first.code != 200 || first.body["second_step"] != "enrol" || first.cookie != "" || first.pending == "" {
		t.Fatalf("password step = %d %s cookie=%q", first.code, first.raw, first.cookie)
	}
	if r := sf.call(t, "GET", "/auth/whoami", ip, first.pending, nil, nil); r.code != 401 {
		t.Fatalf("the pending sign-in reached the console: %d", r.code)
	}
	// Without the pending cookie there is no second step.
	if r := sf.codeStep(t, "", "123456", ip); r.code != 401 || r.body["error"] != "signin_expired" {
		t.Fatalf("code without a password step = %d %s", r.code, r.raw)
	}
	enrol := sf.call(t, "POST", "/auth/login/enrol", ip, "", nil, map[string]string{"pending": first.pending})
	if enrol.code != 200 || enrol.body["secret"] == "" || enrol.body["otpauth_uri"] == nil {
		t.Fatalf("set-up = %d %s", enrol.code, enrol.raw)
	}
	sf.track(scopeMFA, sf.operator)
	if r := sf.codeStep(t, first.pending, "000000", ip); r.code != 401 || r.body["error"] != "wrong_code" || r.cookie != "" {
		t.Fatalf("wrong first code = %d %s", r.code, r.raw)
	}
	done := sf.codeStep(t, first.pending, sf.mfa.codeFor(sf.operator), ip)
	codes, _ := done.body["recovery_codes"].([]any)
	if done.code != 200 || done.cookie == "" || len(codes) != 10 {
		t.Fatalf("confirming the app = %d %s", done.code, done.raw)
	}
	if n := sf.auditCount(t, "operator.mfa_enrolled", sf.operator, since); n != 1 {
		t.Fatalf("set-up audited %d times", n)
	}
	if n := sf.auditCount(t, "operator.mfa_failed", sf.operator, since); n != 1 {
		t.Fatalf("wrong code audited %d times", n)
	}
	if r := sf.call(t, "GET", "/auth/mfa", ip, done.cookie, nil, nil); r.code != 200 || r.body["enrolled"] != true ||
		r.body["recovery_codes_left"] != float64(10) {
		t.Fatalf("own two-step state = %d %s", r.code, r.raw)
	}
	// The pending sign-in was used up.
	if r := sf.codeStep(t, first.pending, sf.mfa.codeFor(sf.operator), ip); r.code != 401 {
		t.Fatalf("a used pending sign-in opened a second session: %d", r.code)
	}

	// From now on: the password, then the code.
	again := sf.passwordStep(t, sf.email, sf.password, ip)
	if again.body["second_step"] != "code" {
		t.Fatalf("second sign-in = %s", again.raw)
	}
	if r := sf.call(t, "POST", "/auth/login/enrol", ip, "", nil, map[string]string{"pending": again.pending}); r.code != 409 {
		t.Fatalf("set-up offered to an operator who has an app: %d", r.code)
	}
	// A recovery code works once.
	rc := codes[0].(string)
	if r := sf.codeStep(t, again.pending, rc, ip); r.code != 200 || r.cookie == "" || r.body["recovery_codes_left"] != float64(9) {
		t.Fatalf("recovery code = %d %s", r.code, r.raw)
	}
	third := sf.passwordStep(t, sf.email, sf.password, ip)
	if r := sf.codeStep(t, third.pending, rc, ip); r.code != 401 {
		t.Fatalf("a used recovery code worked again: %d", r.code)
	}
	if n := sf.auditCount(t, "operator.mfa_recovery_code_used", sf.operator, since); n != 1 {
		t.Fatalf("recovery code use audited %d times", n)
	}
}

func TestIntegrationMFA_WrongCodesLockLikeWrongPasswords(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	since := time.Now().Add(-time.Second)
	if r := sf.login(t, sf.email, sf.password, ip); r.code != 200 {
		t.Fatalf("set-up sign-in = %d %s", r.code, r.raw)
	}
	p := sf.passwordStep(t, sf.email, sf.password, ip)
	for i := 1; i <= 5; i++ {
		r := sf.codeStep(t, p.pending, "000000", ip)
		if r.code != 401 {
			t.Fatalf("wrong code %d = %d %s", i, r.code, r.raw)
		}
		sf.age(t, scopeMFA, sf.operator, time.Minute)
	}
	if r := sf.codeStep(t, p.pending, sf.mfa.codeFor(sf.operator), ip); r.code != 429 {
		t.Fatalf("the right code during a lockout = %d %s", r.code, r.raw)
	}
	if n := sf.auditCount(t, "operator.mfa_locked", sf.operator, since); n < 1 {
		t.Fatal("the code lockout was not audited")
	}
}

func TestIntegrationMFA_SessionsEndWhenIdleOrOldAndTheBackgroundCheckIsNotActivity(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	r := sf.login(t, sf.email, sf.password, ip)
	if r.code != 200 {
		t.Fatalf("sign-in = %d %s", r.code, r.raw)
	}
	sess := sf.app.sessions.peek(r.cookie)
	back := func(idle, age time.Duration) {
		sf.app.sessions.mu.Lock()
		sess.LastSeen = sess.LastSeen.Add(-idle)
		sess.CreatedAt = sess.CreatedAt.Add(-age)
		sf.app.sessions.mu.Unlock()
	}
	// whoami does not move LastSeen; a screen does.
	back(10*time.Minute, 10*time.Minute)
	seen := sess.LastSeen
	if w := sf.call(t, "GET", "/auth/whoami", ip, r.cookie, nil, nil); w.code != 200 || w.body["idle_timeout_minutes"] != float64(30) {
		t.Fatalf("whoami = %d %s", w.code, w.raw)
	}
	if !sess.LastSeen.Equal(seen) {
		t.Fatal("the background whoami check counted as activity")
	}
	if w := sf.call(t, "GET", "/operators", ip, r.cookie, nil, nil); w.code != 200 || !sess.LastSeen.After(seen) {
		t.Fatalf("a screen did not count as activity: %d", w.code)
	}
	// Idle past 30 minutes: ended.
	back(31*time.Minute, 0)
	if w := sf.call(t, "GET", "/operators", ip, r.cookie, nil, nil); w.code != 401 {
		t.Fatalf("an idle session was still open: %d", w.code)
	}
	// Active, but older than 12 hours: ended.
	r2 := sf.login(t, sf.email, sf.password, ip)
	s2 := sf.app.sessions.peek(r2.cookie)
	sf.app.sessions.mu.Lock()
	s2.CreatedAt = s2.CreatedAt.Add(-12*time.Hour - time.Minute)
	sf.app.sessions.mu.Unlock()
	if w := sf.call(t, "GET", "/operators", ip, r2.cookie, nil, nil); w.code != 401 {
		t.Fatalf("a session older than the limit was still open: %d", w.code)
	}
}

func TestIntegrationMFA_SessionLimitsAreAnAuditedSetting(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ctx := context.Background()
	var before struct{ idle, max int }
	_ = sf.pool.QueryRow(ctx, `SELECT idle_timeout_minutes, max_session_hours FROM public.operator_session_policy_get()`).Scan(&before.idle, &before.max)
	t.Cleanup(func() {
		_, _ = sf.pool.Exec(ctx, `SELECT public.operator_session_policy_set($1,$2,'test cleanup','restore after test')`, before.idle, before.max)
	})
	ip := testIP()
	why := "shorter at night " + ip
	r := sf.login(t, sf.email, sf.password, ip)
	if g := sf.call(t, "GET", "/operators/session-policy", ip, r.cookie, nil, nil); g.code != 200 {
		t.Fatalf("read = %d %s", g.code, g.raw)
	}
	for _, bad := range []map[string]any{
		{"idle_timeout_minutes": 4, "max_session_hours": 12, "reason": "too short", "password": sf.password},
		{"idle_timeout_minutes": 30, "max_session_hours": 25, "reason": "too long", "password": sf.password},
		{"idle_timeout_minutes": 30, "max_session_hours": 12, "reason": "", "password": sf.password},
	} {
		if w := sf.call(t, "PUT", "/operators/session-policy", ip, r.cookie, bad, nil); w.code != 400 {
			t.Fatalf("accepted %v: %d", bad, w.code)
		}
	}
	if w := sf.call(t, "PUT", "/operators/session-policy", ip, r.cookie,
		map[string]any{"idle_timeout_minutes": 15, "max_session_hours": 8, "reason": why, "password": "wrong-pw-123"}, nil); w.code != 401 {
		t.Fatalf("saved without the password: %d", w.code)
	}
	if w := sf.call(t, "PUT", "/operators/session-policy", ip, r.cookie,
		map[string]any{"idle_timeout_minutes": 15, "max_session_hours": 8, "reason": why, "password": sf.password}, nil); w.code != 200 {
		t.Fatalf("save = %d %s", w.code, w.raw)
	}
	if w := sf.call(t, "GET", "/auth/whoami", ip, r.cookie, nil, nil); w.body["idle_timeout_minutes"] != float64(15) {
		t.Fatalf("the new limit did not apply to the open session: %s", w.raw)
	}
	var n int
	_ = sf.pool.QueryRow(ctx, `SELECT count(*) FROM public.operator_session_policy_changes WHERE change_reason=$1`, why).Scan(&n)
	if n != 1 || sf.auditCount(t, "operator.session_policy_changed", "", time.Now().Add(-time.Minute)) < 1 {
		t.Fatalf("change logged %d times / not audited", n)
	}
}

func TestIntegrationMFA_AnAdministratorResetsAColleagueNotThemselves(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	ctx := context.Background()
	admin := sf.login(t, sf.email, sf.password, ip)
	// A colleague with an app and an open session.
	var colleague string
	hash, _ := hashPassword("colleague-password-2026")
	if err := sf.pool.QueryRow(ctx, `INSERT INTO operators (email, display_name, password_hash, status)
		VALUES ('colleague-'||$1||'@test.local','Colleague',$2,'active') RETURNING id::text`, testIP(), hash).Scan(&colleague); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.pool.Exec(ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'front_office_operator')`, colleague); err != nil {
		t.Fatal(err)
	}
	var cEmail string
	_ = sf.pool.QueryRow(ctx, `SELECT email FROM operators WHERE id=$1`, colleague).Scan(&cEmail)
	cs := sf.login(t, cEmail, "colleague-password-2026", ip)
	if cs.code != 200 {
		t.Fatalf("colleague sign-in = %d %s", cs.code, cs.raw)
	}
	list := sf.call(t, "GET", "/operators", ip, admin.cookie, nil, nil)
	found := false
	for _, it := range list.body["data"].([]any) {
		if m := it.(map[string]any); m["id"] == colleague {
			found = m["two_step_enrolled"] == true
		}
	}
	if !found {
		t.Fatalf("the list does not show the colleague's app: %s", list.raw)
	}
	body := map[string]any{"password": sf.password, "reason": "lost their phone"}
	if r := sf.call(t, "POST", "/operators/"+sf.operator+"/reset-mfa", ip, admin.cookie, body, nil); r.code != 409 {
		t.Fatalf("an administrator reset their own second step: %d", r.code)
	}
	if r := sf.call(t, "POST", "/operators/"+colleague+"/reset-mfa", ip, admin.cookie,
		map[string]any{"password": sf.password, "reason": ""}, nil); r.code != 400 {
		t.Fatalf("reset without a reason: %d", r.code)
	}
	if r := sf.call(t, "POST", "/operators/"+colleague+"/reset-mfa", ip, admin.cookie, body, nil); r.code != 200 || r.body["removed"] != true {
		t.Fatalf("reset = %d %s", r.code, r.raw)
	}
	if r := sf.call(t, "GET", "/auth/whoami", ip, cs.cookie, nil, nil); r.code != 401 {
		t.Fatalf("the colleague's session outlived the reset: %d", r.code)
	}
	if p := sf.passwordStep(t, cEmail, "colleague-password-2026", ip); p.body["second_step"] != "enrol" {
		t.Fatalf("after a reset the next sign-in must set up a new app: %s", p.raw)
	}
	if sf.auditCount(t, "operator.mfa_reset", colleague, time.Now().Add(-time.Minute)) != 1 {
		t.Fatal("the reset was not audited")
	}
}

// D-R3: new recovery codes need the password AND a current authenticator code -- never a recovery code -- and the
// code is throttled, audited and accepted once, like the sign-in code.
func TestIntegrationMFA_NewRecoveryCodesNeedACurrentAuthenticatorCode(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	since := time.Now().Add(-time.Second)
	sess := sf.login(t, sf.email, sf.password, ip)
	if sess.code != 200 {
		t.Fatalf("sign-in = %d %s", sess.code, sess.raw)
	}
	sf.track(scopeMFA, sf.operator)
	sf.track(scopeReauth, sf.operator)
	ask := func(body map[string]any) resp {
		return sf.call(t, "POST", "/auth/mfa/recovery-codes", ip, sess.cookie, body, nil)
	}
	if r := ask(map[string]any{"password": sf.password}); r.code != 400 || r.body["error"] != "code_required" {
		t.Fatalf("without a code = %d %s", r.code, r.raw)
	}
	if r := ask(map[string]any{"password": sf.password, "code": "ABCDE-FGHJK"}); r.code != 400 || r.body["error"] != "code_required" {
		t.Fatalf("with a recovery code = %d %s", r.code, r.raw)
	}
	wrong := "000000"
	if sf.mfa.codeFor(sf.operator) == wrong {
		wrong = "000001"
	}
	if r := ask(map[string]any{"password": sf.password, "code": wrong}); r.code != 401 || r.body["error"] != "wrong_code" {
		t.Fatalf("with a wrong code = %d %s", r.code, r.raw)
	}
	if n := sf.auditCount(t, "operator.mfa_failed", sf.operator, since); n != 1 {
		t.Fatalf("the wrong code was audited %d times", n)
	}
	sf.age(t, scopeMFA, sf.operator, time.Minute)
	code := sf.mfa.codeFor(sf.operator)
	if r := ask(map[string]any{"password": "not-the-password", "code": code}); r.code != 401 || r.body["error"] != "reauth_required" {
		t.Fatalf("with a wrong password = %d %s", r.code, r.raw)
	}
	sf.age(t, scopeReauth, sf.operator, time.Minute)
	r := ask(map[string]any{"password": sf.password, "code": code})
	if fresh, _ := r.body["recovery_codes"].([]any); r.code != 200 || len(fresh) != 10 {
		t.Fatalf("with the password and the code = %d %s", r.code, r.raw)
	}
	if r := ask(map[string]any{"password": sf.password, "code": code}); r.code != 401 || r.body["error"] != "wrong_code" {
		t.Fatalf("the same code again = %d %s", r.code, r.raw)
	}
	if n := sf.auditCount(t, "operator.mfa_recovery_codes_replaced", sf.operator, since); n != 1 {
		t.Fatalf("new codes audited %d times", n)
	}
}

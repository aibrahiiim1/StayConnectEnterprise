//go:build integration

package main

import (
	"context"
	"testing"
	"time"
)

// Two-step sign-in is optional per account (D68): the password alone opens the session until the operator turns it
// on; from then on only the code does, and turning it off needs the password and a code.
func TestIntegrationMFA_OptionalPerAccountAndEnforcedOnceOn(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	since := time.Now().Add(-time.Second)
	sf.track(scopeMFA, sf.operator)
	sf.track(scopeReauth, sf.operator)

	first := sf.passwordStep(t, sf.email, sf.password, ip)
	if first.code != 200 || first.body["second_step"] != "none" || first.cookie == "" || first.pending != "" ||
		first.body["operator_id"] != sf.operator {
		t.Fatalf("password-only sign-in = %d %s cookie=%q pending=%q", first.code, first.raw, first.cookie, first.pending)
	}
	if r := sf.call(t, "GET", "/auth/whoami", ip, first.cookie, nil, nil); r.code != 200 {
		t.Fatalf("the password-only session = %d %s", r.code, r.raw)
	}
	var none int
	_ = sf.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='operator.login' AND target_id=$1
		AND payload->>'second_step'='none' AND ts >= $2`, sf.operator, since).Scan(&none)
	if none != 1 {
		t.Fatalf("password-only sign-in audited %d times with second_step none", none)
	}
	sess := first.cookie

	// Turning it on needs the password; a set-up that is never confirmed changes nothing and can be started again.
	if r := sf.call(t, "POST", "/auth/mfa/setup", ip, sess, map[string]any{}, nil); r.code != 401 || r.body["error"] != "reauth_required" {
		t.Fatalf("set-up without the password = %d %s", r.code, r.raw)
	}
	if r := sf.call(t, "POST", "/auth/mfa/setup", ip, sess, map[string]any{"password": "not-the-password"}, nil); r.code != 401 {
		t.Fatalf("set-up with a wrong password = %d %s", r.code, r.raw)
	}
	sf.age(t, scopeReauth, sf.operator, time.Minute)
	s1 := sf.call(t, "POST", "/auth/mfa/setup", ip, sess, map[string]any{"password": sf.password}, nil)
	if s1.code != 200 || s1.body["secret"] == "" || s1.body["otpauth_uri"] == nil {
		t.Fatalf("set-up = %d %s", s1.code, s1.raw)
	}
	if r := sf.passwordStep(t, sf.email, sf.password, ip); r.body["second_step"] != "none" || r.cookie == "" {
		t.Fatalf("an unconfirmed set-up changed sign-in: %s", r.raw)
	}
	s2 := sf.call(t, "POST", "/auth/mfa/setup", ip, sess, map[string]any{"password": sf.password}, nil)
	if s2.code != 200 || s2.body["secret"] == s1.body["secret"] {
		t.Fatalf("a second set-up = %d %s", s2.code, s2.raw)
	}
	if r := sf.call(t, "POST", "/auth/mfa/confirm", ip, sess, map[string]any{"code": "000000"}, nil); r.code != 401 || r.body["error"] != "wrong_code" {
		t.Fatalf("a wrong first code = %d %s", r.code, r.raw)
	}
	done := sf.call(t, "POST", "/auth/mfa/confirm", ip, sess, map[string]any{"code": sf.mfa.codeFor(sf.operator)}, nil)
	codes, _ := done.body["recovery_codes"].([]any)
	if done.code != 200 || len(codes) != 10 {
		t.Fatalf("confirming the app = %d %s", done.code, done.raw)
	}
	if r := sf.call(t, "POST", "/auth/mfa/confirm", ip, sess, map[string]any{"code": sf.mfa.codeFor(sf.operator)}, nil); r.code != 409 {
		t.Fatalf("a confirmed authenticator was confirmed again: %d %s", r.code, r.raw)
	}
	if r := sf.call(t, "POST", "/auth/mfa/setup", ip, sess, map[string]any{"password": sf.password}, nil); r.code != 409 || r.body["error"] != "already_enrolled" {
		t.Fatalf("set-up replaced a confirmed authenticator: %d %s", r.code, r.raw)
	}
	if r := sf.call(t, "GET", "/auth/mfa", ip, sess, nil, nil); r.body["enrolled"] != true || r.body["recovery_codes_left"] != float64(10) {
		t.Fatalf("own two-step state = %d %s", r.code, r.raw)
	}
	for action, want := range map[string]int{"operator.mfa_setup_started": 2, "operator.mfa_enrolled": 1, "operator.mfa_failed": 1} {
		if n := sf.auditCount(t, action, sf.operator, since); n != want {
			t.Fatalf("%s audited %d times, want %d", action, n, want)
		}
	}
	sf.age(t, scopeMFA, sf.operator, time.Minute)

	// Once on, the password opens no session; nothing but the code step does.
	p := sf.passwordStep(t, sf.email, sf.password, ip)
	if p.code != 200 || p.body["second_step"] != "code" || p.cookie != "" || p.pending == "" || p.body["operator_id"] != nil {
		t.Fatalf("password step with two-step on = %d %s cookie=%q", p.code, p.raw, p.cookie)
	}
	for _, path := range []string{"/auth/whoami", "/auth/keepalive", "/auth/mfa"} {
		if r := sf.call(t, "GET", path, ip, p.pending, nil, map[string]string{"pending": p.pending}); r.code != 401 {
			t.Fatalf("the pending sign-in reached %s: %d", path, r.code)
		}
	}
	if r := sf.call(t, "POST", "/auth/password", ip, p.pending, map[string]any{"current_password": sf.password,
		"new_password": "another-password-2026"}, map[string]string{"pending": p.pending}); r.code != 401 || r.cookie != "" {
		t.Fatalf("the pending sign-in changed the password: %d %s", r.code, r.raw)
	}
	if r := sf.codeStep(t, "", "123456", ip); r.code != 401 || r.body["error"] != "signin_expired" {
		t.Fatalf("code without a password step = %d %s", r.code, r.raw)
	}
	if r := sf.codeStep(t, p.pending, "000000", ip); r.code != 401 || r.body["error"] != "wrong_code" || r.cookie != "" {
		t.Fatalf("a wrong code = %d %s", r.code, r.raw)
	}
	ok := sf.codeStep(t, p.pending, sf.mfa.codeFor(sf.operator), ip)
	if ok.code != 200 || ok.cookie == "" || ok.body["operator_id"] != sf.operator {
		t.Fatalf("the code step = %d %s", ok.code, ok.raw)
	}
	if r := sf.codeStep(t, p.pending, sf.mfa.codeFor(sf.operator), ip); r.code != 401 {
		t.Fatalf("a used pending sign-in opened a second session: %d", r.code)
	}
	rc := codes[0].(string)
	again := sf.passwordStep(t, sf.email, sf.password, ip)
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
	sf.age(t, scopeMFA, sf.operator, time.Minute)

	// Turning it off needs the password and a code from the second factor.
	off := func(body map[string]any) resp {
		return sf.call(t, "POST", "/auth/mfa/disable", ip, ok.cookie, body, nil)
	}
	if r := off(map[string]any{"password": sf.password}); r.code != 400 || r.body["error"] != "code_required" {
		t.Fatalf("off without a code = %d %s", r.code, r.raw)
	}
	if r := off(map[string]any{"password": "not-the-password", "code": sf.mfa.codeFor(sf.operator)}); r.code != 401 || r.body["error"] != "reauth_required" {
		t.Fatalf("off with a wrong password = %d %s", r.code, r.raw)
	}
	sf.age(t, scopeReauth, sf.operator, time.Minute)
	if r := off(map[string]any{"password": sf.password, "code": "ABCDE-FGHJK"}); r.code != 401 || r.body["error"] != "wrong_code" {
		t.Fatalf("off with a wrong code = %d %s", r.code, r.raw)
	}
	if st, _ := sf.mfa.Status(context.Background(), sf.operator); !st.Enrolled {
		t.Fatal("a refused request turned two-step sign-in off")
	}
	sf.age(t, scopeMFA, sf.operator, time.Minute)
	if r := off(map[string]any{"password": sf.password, "code": codes[1].(string)}); r.code != 200 || r.body["enrolled"] != false {
		t.Fatalf("off with the password and a recovery code = %d %s", r.code, r.raw)
	}
	var disabled int
	_ = sf.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='operator.mfa_disabled' AND target_id=$1
		AND payload->>'second_step'='recovery_code' AND ts >= $2`, sf.operator, since).Scan(&disabled)
	if disabled != 1 {
		t.Fatalf("turning it off audited %d times", disabled)
	}
	if r := sf.passwordStep(t, sf.email, sf.password, ip); r.body["second_step"] != "none" || r.cookie == "" {
		t.Fatalf("after turning it off the password alone must sign in: %s", r.raw)
	}
	if r := off(map[string]any{"password": sf.password, "code": "123456"}); r.code != 409 {
		t.Fatalf("off when already off = %d %s", r.code, r.raw)
	}
}

func TestIntegrationMFA_WrongCodesLockLikeWrongPasswords(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	since := time.Now().Add(-time.Second)
	r := sf.login(t, sf.email, sf.password, ip)
	if r.code != 200 {
		t.Fatalf("set-up sign-in = %d %s", r.code, r.raw)
	}
	sf.enableMFA(t, r.cookie, sf.operator, sf.password, ip)
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
	back(31*time.Minute, 0)
	if w := sf.call(t, "GET", "/operators", ip, r.cookie, nil, nil); w.code != 401 {
		t.Fatalf("an idle session was still open: %d", w.code)
	}
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
	listed := func() any {
		list := sf.call(t, "GET", "/operators", ip, admin.cookie, nil, nil)
		for _, it := range list.body["data"].([]any) {
			if m := it.(map[string]any); m["id"] == colleague {
				return m["two_step_enrolled"]
			}
		}
		t.Fatalf("the colleague is not listed: %s", list.raw)
		return nil
	}
	if v := listed(); v != false {
		t.Fatalf("the list shows two-step sign-in on before it was turned on: %v", v)
	}
	sf.enableMFA(t, cs.cookie, colleague, "colleague-password-2026", ip)
	if v := listed(); v != true {
		t.Fatalf("the list does not show the colleague's app: %v", v)
	}
	cs = sf.login(t, cEmail, "colleague-password-2026", ip)
	if cs.code != 200 || cs.cookie == "" {
		t.Fatalf("colleague sign-in with the code = %d %s", cs.code, cs.raw)
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
	if p := sf.passwordStep(t, cEmail, "colleague-password-2026", ip); p.body["second_step"] != "none" || p.cookie == "" {
		t.Fatalf("after a reset the password alone must sign in: %s", p.raw)
	}
	if sf.auditCount(t, "operator.mfa_reset", colleague, time.Now().Add(-time.Minute)) != 1 {
		t.Fatal("the reset was not audited")
	}
}

// New recovery codes need the password and a current authenticator code (never a recovery code), throttled, audited and accepted once.
func TestIntegrationMFA_NewRecoveryCodesNeedACurrentAuthenticatorCode(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	since := time.Now().Add(-time.Second)
	sess := sf.login(t, sf.email, sf.password, ip)
	if sess.code != 200 {
		t.Fatalf("sign-in = %d %s", sess.code, sess.raw)
	}
	if r := sf.call(t, "POST", "/auth/mfa/recovery-codes", ip, sess.cookie, map[string]any{"password": sf.password, "code": "123456"}, nil); r.code != 409 {
		t.Fatalf("new codes without two-step sign-in on = %d %s", r.code, r.raw)
	}
	sf.enableMFA(t, sess.cookie, sf.operator, sf.password, ip)
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

// The code step builds the session from the account as it is then, not the password step's snapshot.
func TestIntegrationMFA_TheCodeStepReloadsTheAccount(t *testing.T) {
	sf := newSigninAPI(t, "site_admin")
	ip := testIP()
	ctx := context.Background()
	first := sf.login(t, sf.email, sf.password, ip)
	if first.code != 200 {
		t.Fatalf("set-up sign-in = %d %s", first.code, first.raw)
	}
	sf.enableMFA(t, first.cookie, sf.operator, sf.password, ip)

	p := sf.passwordStep(t, sf.email, sf.password, ip)
	if _, err := sf.pool.Exec(ctx, `DELETE FROM operator_roles WHERE operator_id=$1`, sf.operator); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.pool.Exec(ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'front_office_operator')`, sf.operator); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.pool.Exec(ctx, `UPDATE operators SET must_change_password=true WHERE id=$1`, sf.operator); err != nil {
		t.Fatal(err)
	}
	r := sf.codeStep(t, p.pending, sf.mfa.codeFor(sf.operator), ip)
	roles, _ := r.body["roles"].([]any)
	if r.code != 200 || len(roles) != 1 || roles[0] != "front_office_operator" || r.body["must_change_password"] != true {
		t.Fatalf("the session was built from the stale snapshot: %d %s", r.code, r.raw)
	}
	if _, err := sf.pool.Exec(ctx, `UPDATE operators SET must_change_password=false WHERE id=$1`, sf.operator); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.pool.Exec(ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'site_admin')`, sf.operator); err != nil {
		t.Fatal(err)
	}

	p = sf.passwordStep(t, sf.email, sf.password, ip)
	other, _ := hashPassword("another-password-2026")
	var old string
	_ = sf.pool.QueryRow(ctx, `SELECT password_hash FROM operators WHERE id=$1`, sf.operator).Scan(&old)
	if _, err := sf.pool.Exec(ctx, `UPDATE operators SET password_hash=$2 WHERE id=$1`, sf.operator, other); err != nil {
		t.Fatal(err)
	}
	if r := sf.codeStep(t, p.pending, sf.mfa.codeFor(sf.operator), ip); r.code != 401 || r.cookie != "" {
		t.Fatalf("a pending sign-in outlived a password change: %d %s", r.code, r.raw)
	}
	if _, err := sf.pool.Exec(ctx, `UPDATE operators SET password_hash=$2 WHERE id=$1`, sf.operator, old); err != nil {
		t.Fatal(err)
	}

	admin := sf.login(t, sf.email, sf.password, ip)
	p = sf.passwordStep(t, sf.email, sf.password, ip)
	if r := sf.call(t, "POST", "/operators/"+sf.operator+"/roles", ip, admin.cookie,
		map[string]any{"role": "front_office_operator", "your_password": sf.password}, nil); r.code != 201 {
		t.Fatalf("add role = %d %s", r.code, r.raw)
	}
	if r := sf.codeStep(t, p.pending, sf.mfa.codeFor(sf.operator), ip); r.code != 401 || r.body["error"] != "signin_expired" {
		t.Fatalf("a pending sign-in outlived a role change: %d %s", r.code, r.raw)
	}
}

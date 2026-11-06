//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type sentOperatorMail struct {
	To, Template, Code string
}

type fakeOperatorMail struct {
	mu    sync.Mutex
	ready bool
	sent  chan sentOperatorMail
}

func (f *fakeOperatorMail) RoundTrip(r *http.Request) (*http.Response, error) {
	answer := func(code int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	}
	f.mu.Lock()
	ready := f.ready
	f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/admin/operator-mail/state":
		if ready {
			return answer(200, `{"kind":"smtp","ready":true}`)
		}
		return answer(200, `{"kind":"stub","ready":false}`)
	case "/v1/admin/operator-mail/send":
		var in struct {
			To, Template, Code string
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !ready {
			return answer(409, `{"error":"mail_not_ready"}`)
		}
		f.sent <- sentOperatorMail{To: in.To, Template: in.Template, Code: in.Code}
		return answer(200, `{"sent":true}`)
	}
	return answer(404, `{}`)
}

type resetFixture struct {
	*signinFixture
	mail  *fakeOperatorMail
	reset *httptest.Server
}

func newResetAPI(t *testing.T) *resetFixture {
	t.Helper()
	sf := newSigninAPI(t, "site_admin")
	ctx := context.Background()
	raw, err := os.ReadFile("../../migrations/0214_an_operator_can_reset_a_forgotten_password_by_email.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sf.pool.Exec(ctx, string(raw)); err != nil {
		t.Fatalf("apply 0214: %v", err)
	}
	if _, err := sf.pool.Exec(ctx, `DELETE FROM public.operator_password_reset_policy;
		DELETE FROM public.operator_password_reset_requests; DELETE FROM public.operator_password_resets`); err != nil {
		t.Fatal(err)
	}
	var other string
	if err := sf.pool.QueryRow(ctx, `INSERT INTO operators(tenant_id,email,display_name,password_hash,status)
		VALUES ($1, 'other-admin-'||gen_random_uuid()||'@test.local', 'Other', 'x', 'active') RETURNING id::text`, sf.tenant).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.pool.Exec(ctx, `INSERT INTO operator_roles(operator_id, role) VALUES ($1, 'site_admin')`, other); err != nil {
		t.Fatal(err)
	}
	m := &fakeOperatorMail{ready: true, sent: make(chan sentOperatorMail, 16)}
	sf.app.scd = &scdClient{http: &http.Client{Transport: m}}
	s := sf.app
	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Use(sameOrigin)
		r.Post("/auth/login", s.login)
		r.Post("/auth/login/code", s.loginCode)
		r.Get("/auth/password-reset", s.passwordResetAvailable)
		r.Post("/auth/password-reset/request", s.passwordResetRequest)
		r.Post("/auth/password-reset/confirm", s.passwordResetConfirm)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/whoami", s.whoami)
		})
	})
	sf.h.Close()
	sf.h = httptest.NewServer(r)
	t.Cleanup(func() {
		_, _ = sf.pool.Exec(context.Background(), `DELETE FROM public.operator_password_reset_policy;
			DELETE FROM public.operator_password_reset_requests; DELETE FROM public.operator_password_resets;
			DELETE FROM operators WHERE id = $1`, other)
	})
	return &resetFixture{signinFixture: sf, mail: m}
}

func (rf *resetFixture) setPolicy(t *testing.T, enabled bool, maxAttempts int, allowLast bool) {
	t.Helper()
	if _, err := rf.pool.Exec(context.Background(),
		`SELECT public.operator_password_reset_policy_set($1, 15, $2, 5, 20, $3, NULL, 'test', 'integration test')`,
		enabled, maxAttempts, allowLast); err != nil {
		t.Fatal(err)
	}
}

func (rf *resetFixture) nextMail(t *testing.T) sentOperatorMail {
	t.Helper()
	select {
	case m := <-rf.mail.sent:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no email was sent")
	}
	return sentOperatorMail{}
}

func (rf *resetFixture) noMail(t *testing.T) {
	t.Helper()
	select {
	case m := <-rf.mail.sent:
		t.Fatalf("an email was sent: %+v", m)
	case <-time.After(700 * time.Millisecond):
	}
}

func (rf *resetFixture) request(t *testing.T, email, ip string) resp {
	return rf.call(t, "POST", "/auth/password-reset/request", ip, "", map[string]any{"email": email}, nil)
}

func (rf *resetFixture) confirm(t *testing.T, email, code, pw, mfa, ip string) resp {
	rf.track(scopeIP, ip)
	return rf.call(t, "POST", "/auth/password-reset/confirm", ip, "", map[string]any{"email": email, "code": code, "password": pw, "mfa_code": mfa}, nil)
}

func TestForgotPasswordIsOfferedOnlyWhenTurnedOnAndSMTPIsReady(t *testing.T) {
	rf := newResetAPI(t)
	ip := testIP()
	if r := rf.call(t, "GET", "/auth/password-reset", ip, "", nil, nil); r.body["available"] != false {
		t.Fatalf("off by default: %s", r.raw)
	}
	if r := rf.request(t, rf.email, ip); r.code != http.StatusConflict {
		t.Fatalf("a request while off: %d %s", r.code, r.raw)
	}
	rf.noMail(t)
	rf.setPolicy(t, true, 5, false)
	rf.mail.mu.Lock()
	rf.mail.ready = false
	rf.mail.mu.Unlock()
	if r := rf.call(t, "GET", "/auth/password-reset", ip, "", nil, nil); r.body["available"] != false {
		t.Fatalf("on without SMTP: %s", r.raw)
	}
	if !rf.app.passwordResetNeedsMail(context.Background()) {
		t.Fatal("the Overview must warn that forgot password has no SMTP server")
	}
	rf.mail.mu.Lock()
	rf.mail.ready = true
	rf.mail.mu.Unlock()
	if r := rf.call(t, "GET", "/auth/password-reset", ip, "", nil, nil); r.body["available"] != true {
		t.Fatalf("on with SMTP: %s", r.raw)
	}
}

func TestAResetCodeChangesThePasswordOnceAndEndsEverySession(t *testing.T) {
	rf := newResetAPI(t)
	rf.setPolicy(t, true, 5, false)
	ip := testIP()
	if r := rf.request(t, rf.email, ip); r.code != http.StatusAccepted {
		t.Fatalf("request: %d %s", r.code, r.raw)
	}
	m := rf.nextMail(t)
	if m.To != rf.email || m.Template != "reset_code" || !resetCodePattern.MatchString(m.Code) {
		t.Fatalf("mail %+v", m)
	}
	var stored string
	_ = rf.pool.QueryRow(context.Background(), `SELECT code_hash FROM public.operator_password_resets WHERE operator_id = $1`, rf.operator).Scan(&stored)
	if stored == "" || strings.Contains(stored, m.Code) {
		t.Fatalf("the code must be stored only as a hash: %q", stored)
	}
	if r := rf.confirm(t, rf.email, "00000000", "a-brand-new-password-1", "", ip); r.code != http.StatusBadRequest || r.body["error"] != "code_refused" {
		t.Fatalf("wrong code: %d %s", r.code, r.raw)
	}
	if r := rf.confirm(t, rf.email, m.Code, "short", "", ip); r.code != http.StatusBadRequest || r.body["error"] != "password_weak" {
		t.Fatalf("weak password: %d %s", r.code, r.raw)
	}
	if r := rf.confirm(t, rf.email, m.Code, "a-brand-new-password-1", "", ip); r.code != http.StatusOK {
		t.Fatalf("right code: %d %s", r.code, r.raw)
	}
	if n := rf.nextMail(t); n.Template != "password_changed" || n.To != rf.email {
		t.Fatalf("notice %+v", n)
	}
	if r := rf.call(t, "GET", "/auth/whoami", ip, rf.sessTok, nil, nil); r.code != http.StatusUnauthorized {
		t.Fatalf("the old session must end: %d", r.code)
	}
	if r := rf.confirm(t, rf.email, m.Code, "another-new-password-2", "", ip); r.code != http.StatusBadRequest {
		t.Fatalf("a used code must not work again: %d %s", r.code, r.raw)
	}
	if r := rf.login(t, rf.email, rf.password, testIP()); r.code == http.StatusOK {
		t.Fatal("the old password still signs in")
	}
	if r := rf.login(t, rf.email, "a-brand-new-password-1", testIP()); r.code != http.StatusOK {
		t.Fatalf("the new password: %d %s", r.code, r.raw)
	}
	var n int
	_ = rf.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'operator.password_reset_by_email' AND target_id = $1`, rf.operator).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows: %d", n)
	}
}

func TestAnUnknownAddressGetsTheSameAnswerAndNoEmail(t *testing.T) {
	rf := newResetAPI(t)
	rf.setPolicy(t, true, 5, false)
	known := rf.request(t, rf.email, testIP())
	rf.nextMail(t)
	unknown := rf.request(t, "nobody-"+time.Now().Format("150405.000")+"@test.local", testIP())
	if unknown.code != known.code || unknown.body["message"] != known.body["message"] {
		t.Fatalf("known %d %s, unknown %d %s", known.code, known.raw, unknown.code, unknown.raw)
	}
	rf.noMail(t)
	if r := rf.confirm(t, "nobody@test.local", "12345678", "a-brand-new-password-1", "", testIP()); r.body["error"] != "code_refused" {
		t.Fatalf("unknown confirm: %s", r.raw)
	}
}

func TestTooManyWrongCodesEndTheCode(t *testing.T) {
	rf := newResetAPI(t)
	rf.setPolicy(t, true, 3, false)
	ip := testIP()
	rf.request(t, rf.email, ip)
	m := rf.nextMail(t)
	for i := 0; i < 3; i++ {
		rf.confirm(t, rf.email, "00000000", "a-brand-new-password-1", "", ip)
	}
	if r := rf.confirm(t, rf.email, m.Code, "a-brand-new-password-1", "", ip); r.body["error"] != "code_refused" {
		t.Fatalf("the code must stop working after 3 wrong tries: %s", r.raw)
	}
}

func TestAnExpiredCodeDoesNotWork(t *testing.T) {
	rf := newResetAPI(t)
	rf.setPolicy(t, true, 5, false)
	ip := testIP()
	rf.request(t, rf.email, ip)
	m := rf.nextMail(t)
	if _, err := rf.pool.Exec(context.Background(), `UPDATE public.operator_password_resets SET expires_at = now() - interval '1 second' WHERE operator_id = $1`, rf.operator); err != nil {
		t.Fatal(err)
	}
	if r := rf.confirm(t, rf.email, m.Code, "a-brand-new-password-1", "", ip); r.body["error"] != "code_refused" {
		t.Fatalf("expired: %s", r.raw)
	}
}

func TestTheLastSiteAdminResetsByEmailOnlyWhenAllowed(t *testing.T) {
	rf := newResetAPI(t)
	if _, err := rf.pool.Exec(context.Background(), `UPDATE operators SET status = 'disabled' WHERE id <> $1 AND id IN (SELECT operator_id FROM operator_roles WHERE role = 'site_admin') AND status = 'active'`, rf.operator); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = rf.pool.Exec(context.Background(), `UPDATE operators SET status = 'active' WHERE status = 'disabled' AND email LIKE 'other-admin-%'`)
	})
	rf.setPolicy(t, true, 5, false)
	if r := rf.request(t, rf.email, testIP()); r.code != http.StatusAccepted {
		t.Fatalf("request: %s", r.raw)
	}
	rf.noMail(t)
	rf.setPolicy(t, true, 5, true)
	if _, err := rf.pool.Exec(context.Background(), `DELETE FROM public.operator_password_reset_requests`); err != nil {
		t.Fatal(err)
	}
	rf.request(t, rf.email, testIP())
	if m := rf.nextMail(t); m.To != rf.email {
		t.Fatalf("allowed: %+v", m)
	}
}

func TestAnAccountWithAnAuthenticatorNeedsItsCodeToReset(t *testing.T) {
	rf := newResetAPI(t)
	rf.setPolicy(t, true, 5, false)
	ctx := context.Background()
	if _, _, err := rf.mfa.Enrol(ctx, rf.operator, rf.email); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := rf.mfa.Confirm(ctx, rf.operator, rf.mfa.codeFor(rf.operator)); err != nil || !ok {
		t.Fatal("confirm the authenticator", err)
	}
	rf.track(scopeMFA, rf.operator)
	ip := testIP()
	rf.request(t, rf.email, ip)
	m := rf.nextMail(t)
	if r := rf.confirm(t, rf.email, m.Code, "a-brand-new-password-1", "", ip); r.code != http.StatusUnauthorized || r.body["error"] != "mfa_required" {
		t.Fatalf("no authenticator code: %d %s", r.code, r.raw)
	}
	if r := rf.confirm(t, rf.email, m.Code, "a-brand-new-password-1", "000000", ip); r.body["error"] != "mfa_wrong" {
		t.Fatalf("wrong authenticator code: %s", r.raw)
	}
	if r := rf.confirm(t, rf.email, m.Code, "a-brand-new-password-1", rf.mfa.codeFor(rf.operator), ip); r.code != http.StatusOK {
		t.Fatalf("right codes: %d %s", r.code, r.raw)
	}
}

func TestResetRequestsAreLimited(t *testing.T) {
	rf := newResetAPI(t)
	rf.setPolicy(t, true, 5, false)
	rf.request(t, rf.email, testIP())
	rf.nextMail(t)
	if r := rf.request(t, rf.email, testIP()); r.code != http.StatusAccepted {
		t.Fatalf("a second request within a minute still gets the same answer: %s", r.raw)
	}
	rf.noMail(t)
	ip := testIP()
	for i := 0; i < 20; i++ {
		rf.request(t, "someone-"+time.Now().Format("150405.000000")+"@test.local", ip)
	}
	if r := rf.request(t, rf.email, ip); r.code != http.StatusTooManyRequests {
		t.Fatalf("the 21st request from one address in an hour: %d %s", r.code, r.raw)
	}
}

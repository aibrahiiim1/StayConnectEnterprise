package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

// EMAIL ACCOUNTS: the pure decisions (self_service.go). The database-backed journey is
// self_service_integration_test.go.

func ssKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func tp(t time.Time) *time.Time { return &t }

// THE SUSPENSION PREDICATE, as a truth table. Each row changes one fact from a suspended account.
func TestAccountSuspendedTruthTable(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	set := defaultSelfServiceSettings()
	warn := set
	warn.SuspensionPolicy = "WARN_ONLY"
	notReq := set
	notReq.VerificationRequired = false
	suspended := selfServiceAccountRow{SelfRegistered: true, Enabled: true, Deadline: tp(past)}
	with := func(f func(*selfServiceAccountRow)) selfServiceAccountRow { a := suspended; f(&a); return a }
	cases := []struct {
		name string
		a    selfServiceAccountRow
		set  selfServiceSettings
		want bool
	}{
		{"unverified past the deadline, no grace", suspended, set, true},
		{"deadline not reached", with(func(a *selfServiceAccountRow) { a.Deadline = tp(future) }), set, false},
		{"deadline exactly now is not past", with(func(a *selfServiceAccountRow) { a.Deadline = tp(now) }), set, false},
		{"no deadline at all", with(func(a *selfServiceAccountRow) { a.Deadline = nil }), set, false},
		{"verified", with(func(a *selfServiceAccountRow) { a.VerifiedAt = tp(past) }), set, false},
		{"grace running", with(func(a *selfServiceAccountRow) { a.GraceUntil = tp(future) }), set, false},
		{"grace ended", with(func(a *selfServiceAccountRow) { a.GraceUntil = tp(past) }), set, true},
		{"grace ends exactly now", with(func(a *selfServiceAccountRow) { a.GraceUntil = tp(now) }), set, true},
		{"staff-created Personal account", with(func(a *selfServiceAccountRow) { a.SelfRegistered = false }), set, false},
		{"policy WARN_ONLY", suspended, warn, false},
		{"verification not required", suspended, notReq, false},
	}
	for _, c := range cases {
		if got := accountSuspended(c.a, c.set, now); got != c.want {
			t.Errorf("%s: suspended=%v, want %v", c.name, got, c.want)
		}
	}
}

// THE GRACE ALLOWANCE: only a suspended account, only while unused; 0 means never, the hard maximum is 3.
func TestGraceIsOfferedOnlyWithinTheAllowance(t *testing.T) {
	now := time.Now()
	a := selfServiceAccountRow{SelfRegistered: true, Deadline: tp(now.Add(-time.Hour))}
	set := defaultSelfServiceSettings() // max 1
	if !graceAvailable(a, set, now) {
		t.Fatal("a suspended account with an unused allowance may take grace")
	}
	a.GraceUsed = 1
	if graceAvailable(a, set, now) {
		t.Fatal("the default allowance is one grace per account")
	}
	set.MaxGraceExtensions = 3
	a.GraceUsed = 2
	if !graceAvailable(a, set, now) {
		t.Fatal("within a larger allowance grace is offered again")
	}
	set.MaxGraceExtensions = 0
	a.GraceUsed = 0
	if graceAvailable(a, set, now) {
		t.Fatal("an allowance of 0 never offers grace")
	}
	set.MaxGraceExtensions = 9
	if got := set.normalized().MaxGraceExtensions; got != 3 {
		t.Fatalf("the hard maximum is 3, got %d", got)
	}
	notSuspended := a
	notSuspended.Deadline = tp(now.Add(time.Hour))
	if graceAvailable(notSuspended, defaultSelfServiceSettings(), now) {
		t.Fatal("an account that is not suspended needs no grace")
	}
}

// THE SETTINGS are clamped into the contract bounds (§6) whatever was read, and the defaults are the contract's.
func TestSelfServiceSettingsBoundsAndDefaults(t *testing.T) {
	d := defaultSelfServiceSettings()
	if !d.VerificationRequired || d.DeadlineHours != 24 || d.SuspensionPolicy != "SUSPEND" || d.ReminderHoursBefore != 4 ||
		d.ResendCooldownSeconds != 60 || d.ResendMaxPerDay != 5 || d.GraceMinutes != 60 || d.MaxGraceExtensions != 1 ||
		d.RegistrationsPerDevicePerDay != 3 {
		t.Fatalf("defaults drifted from the contract: %+v", d)
	}
	if d.normalized() != d {
		t.Fatal("the defaults are inside their own bounds")
	}
	low := selfServiceSettings{DeadlineHours: 0, ReminderHoursBefore: -1, ResendCooldownSeconds: 1, ResendMaxPerDay: 0,
		GraceMinutes: 1, MaxGraceExtensions: -1, RegistrationsPerDevicePerDay: 0, SuspensionPolicy: "nonsense"}.normalized()
	if low.DeadlineHours != 1 || low.ReminderHoursBefore != 0 || low.ResendCooldownSeconds != 30 || low.ResendMaxPerDay != 1 ||
		low.GraceMinutes != 15 || low.MaxGraceExtensions != 0 || low.RegistrationsPerDevicePerDay != 1 || low.SuspensionPolicy != "SUSPEND" {
		t.Fatalf("low values not clamped up: %+v", low)
	}
	high := selfServiceSettings{DeadlineHours: 999, ReminderHoursBefore: 999, ResendCooldownSeconds: 99999, ResendMaxPerDay: 99,
		GraceMinutes: 9999, MaxGraceExtensions: 99, RegistrationsPerDevicePerDay: 99, SuspensionPolicy: "WARN_ONLY"}.normalized()
	if high.DeadlineHours != 168 || high.ReminderHoursBefore != 72 || high.ResendCooldownSeconds != 3600 || high.ResendMaxPerDay != 20 ||
		high.GraceMinutes != 240 || high.MaxGraceExtensions != 3 || high.RegistrationsPerDevicePerDay != 10 || high.SuspensionPolicy != "WARN_ONLY" {
		t.Fatalf("high values not clamped down: %+v", high)
	}
	// A reminder at or after the deadline is no reminder: it is switched off rather than sent at registration.
	if r := (selfServiceSettings{DeadlineHours: 4, ReminderHoursBefore: 4}).normalized(); r.ReminderHoursBefore != 0 {
		t.Fatalf("reminder >= deadline must become 0, got %d", r.ReminderHoursBefore)
	}
}

// THE RESEND WAIT: the cooldown after the newest message, and the rolling daily allowance.
func TestResendWaitHonoursCooldownAndDailyAllowance(t *testing.T) {
	now := time.Now()
	set := defaultSelfServiceSettings() // 60 s, 5/day
	if w, lim := resendWait(nil, nil, 0, set, now); w != 0 || lim {
		t.Fatal("nothing sent yet: resend now")
	}
	if w, lim := resendWait(tp(now.Add(-20*time.Second)), tp(now.Add(-20*time.Second)), 1, set, now); lim || ceilSeconds(w) != 40 {
		t.Fatalf("20 s after a message the wait is 40 s, got %v limited=%v", w, lim)
	}
	if w, _ := resendWait(tp(now.Add(-61*time.Second)), tp(now.Add(-61*time.Second)), 1, set, now); w != 0 {
		t.Fatal("after the cooldown a resend is allowed")
	}
	oldest := now.Add(-23 * time.Hour)
	w, lim := resendWait(tp(now.Add(-10*time.Minute)), &oldest, 5, set, now)
	if !lim || ceilSeconds(w) != 3600 {
		t.Fatalf("with the daily allowance spent the wait is until the oldest leaves the 24 h window: %v %v", w, lim)
	}
}

// THE EMAILED LINK: only an HMAC is stored, under a key derived for exactly this purpose.
func TestEmailTokensAreKeyedAndDomainSeparated(t *testing.T) {
	s := &server{resumeKey: ssKey(t)}
	k := s.selfServiceKey(accountEmailTokenDomain)
	other := s.selfServiceKey(accountSessionTokenDomain)
	if len(k) != 32 || bytes.Equal(k, other) || bytes.Equal(k, s.resumeKey) {
		t.Fatal("each purpose needs its own derived key, never the resume key itself")
	}
	tok, err := newEmailToken()
	if err != nil || len(tok) != 43 || strings.ContainsAny(tok, "+/=") {
		t.Fatalf("a token is 256 bits, URL-safe and unpadded: %q %v", tok, err)
	}
	if tok2, _ := newEmailToken(); tok2 == tok {
		t.Fatal("tokens must be random")
	}
	h := emailTokenHMAC(k, tok)
	if len(h) != 32 || !bytes.Equal(h, emailTokenHMAC(k, tok)) || bytes.Equal(h, emailTokenHMAC(other, tok)) {
		t.Fatal("the index is a deterministic HMAC under the link key only")
	}
	if strings.Contains(string(h), tok) || !strings.HasPrefix(emailTokenKeyID(k), "aet1:") || emailTokenKeyID(k) == emailTokenKeyID(other) {
		t.Fatal("the key id names the key without being it")
	}
	if (&server{resumeKey: []byte("short")}).selfServiceKey(accountEmailTokenDomain) != nil {
		t.Fatal("without a real local key nothing can be minted")
	}
	if !verifyTokenExpiry(nil, time.Unix(0, 0)).Equal(time.Unix(0, 0).Add(24 * time.Hour)) {
		t.Fatal("a VERIFY link lives at least 24 hours")
	}
	late := time.Unix(0, 0).Add(48 * time.Hour)
	if !verifyTokenExpiry(&late, time.Unix(0, 0)).Equal(late) {
		t.Fatal("a VERIFY link lives until a later deadline")
	}
}

// THE ACCOUNT TOKEN is bound to the account, the device and 15 minutes; anything else is the same refusal.
func TestAccountTokenIsBoundToTheDeviceAndExpires(t *testing.T) {
	s := &server{resumeKey: ssKey(t)}
	now := time.Now()
	const acct, mac = "0f8b9a0e-0000-4000-8000-000000000001", "02:00:00:aa:bb:01"
	tok := s.issueAccountToken(acct, "02:00:00:AA:BB:01", now)
	if tok == "" || strings.ContainsAny(tok, "+/= ") {
		t.Fatalf("issue: %q", tok)
	}
	if got := s.parseAccountToken(tok, mac, now.Add(time.Minute)); got != acct {
		t.Fatalf("the device it was issued to gets the account back: %q", got)
	}
	if s.parseAccountToken(tok, "02:00:00:aa:bb:02", now) != "" {
		t.Fatal("another device must not borrow the token")
	}
	if s.parseAccountToken(tok, mac, now.Add(accountTokenTTL)) != "" {
		t.Fatal("the token expires after 15 minutes")
	}
	parts := strings.Split(tok, ".")
	if s.parseAccountToken(parts[0]+"."+parts[1]+"."+parts[1], mac, now) != "" || s.parseAccountToken("", mac, now) != "" ||
		s.parseAccountToken("v2."+parts[1]+"."+parts[2], mac, now) != "" {
		t.Fatal("a forged or malformed token says nothing")
	}
	if (&server{resumeKey: ssKey(t)}).parseAccountToken(tok, mac, now) != "" {
		t.Fatal("a token from another appliance's key is not valid here")
	}
	if (&server{}).issueAccountToken(acct, mac, now) != "" || s.issueAccountToken(acct, "not-a-mac", now) != "" {
		t.Fatal("no key or no device: no token")
	}
}

func TestNormalizeAccountEmail(t *testing.T) {
	for in, want := range map[string]string{
		" Ann.Lee+wifi@Example.COM ":             "ann.lee+wifi@example.com",
		"a@b.co":                                 "a@b.co",
		"ann@example":                            "",
		"ann@@example.com":                       "",
		"@example.com":                           "",
		"ann@.example.com":                       "",
		"ann@exa_mple.com":                       "",
		"ann lee@example.com":                    "",
		".ann@example.com":                       "",
		"an..n@example.com":                      "",
		"ann@example.c":                          "",
		"ann@example.123":                        "",
		"ann@-example.com":                       "",
		"":                                       "",
		strings.Repeat("a", 65) + "@example.com": "",
	} {
		if got := normalizeAccountEmail(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// THE LINK goes to the local portal: the canonical HTTPS hostname while HTTPS is active, else the portal address.
func TestAccountLinkBase(t *testing.T) {
	cases := map[string]PortalTLSState{
		"https://ab12cd34.gw.onegate.work": {HTTPSActive: true, Canonical: "ab12cd34.gw.onegate.work", PortalIP: "10.255.255.1"},
		"http://10.255.255.1":              {Canonical: "ab12cd34.gw.onegate.work", PortalIP: "10.255.255.1"},
		"http://10.254.254.1":              {PortalIP: "10.254.254.1/32"},
		"":                                 {},
	}
	for want, st := range cases {
		if got := accountLinkBase(st); got != want {
			t.Errorf("%+v -> %q, want %q", st, got, want)
		}
	}
	msg := accountEmailMessage(tokenPurposeVerify, "ann@example.com", "https://h/account/verify?t=x", tp(time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)))
	if msg.To != "ann@example.com" || !strings.Contains(msg.Text, "https://h/account/verify?t=x") || !strings.Contains(msg.Text, "2026-10-16 09:30 UTC") {
		t.Fatalf("verify message: %+v", msg)
	}
	if r := accountEmailMessage(tokenPurposeReset, "ann@example.com", "L", nil); !strings.Contains(r.Text, "30 minutes") || !strings.Contains(r.Subject, "Reset") {
		t.Fatalf("reset message: %+v", r)
	}
}

func TestSendBackoffAndLoopCadence(t *testing.T) {
	for n, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 7: 30 * time.Minute, 50: 30 * time.Minute} {
		if got := sendBackoff(n); got != want {
			t.Errorf("attempt %d: %v, want %v", n, got, want)
		}
	}
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	s := &server{}
	s.initSelfService(get)
	if s.selfService.sendTick != 15*time.Second || s.selfService.sweepTick != 30*time.Second {
		t.Fatalf("defaults: %v %v", s.selfService.sendTick, s.selfService.sweepTick)
	}
	env["SCD_SELF_SERVICE_SEND_TICK"], env["SCD_SELF_SERVICE_SWEEP_TICK"] = "2s", "1ms"
	s.initSelfService(get)
	if s.selfService.sendTick != 2*time.Second || s.selfService.sweepTick != 5*time.Second {
		t.Fatalf("override and floor: %v %v", s.selfService.sendTick, s.selfService.sweepTick)
	}
	env["SCD_SELF_SERVICE_SEND_TICK"], env["SCD_SELF_SERVICE_SWEEP_TICK"] = "0", "nonsense"
	s.initSelfService(get)
	if s.selfService.sendTick != time.Second || s.selfService.sweepTick != 30*time.Second {
		t.Fatalf("floor and malformed: %v %v", s.selfService.sendTick, s.selfService.sweepTick)
	}
	s.kickSelfServiceOutbox()
	s.kickSelfServiceOutbox() // a pending kick is enough; never blocks
}

func TestEmailAccountSwitch(t *testing.T) {
	on := &tenantcfg.AuthMethod{Enabled: true}
	if !methodSwitchOn(&tenantcfg.AuthMethods{EmailAccount: on}, guestMethodEmailAccount) ||
		methodSwitchOn(&tenantcfg.AuthMethods{GuestAccount: on}, guestMethodEmailAccount) ||
		methodSwitchOn(&tenantcfg.AuthMethods{EmailAccount: on}, guestMethodAccount) {
		t.Fatal("Email accounts and Personal accounts are separate switches")
	}
}

// THE REGISTER ROUTE refuses before touching anything: the licence, the switch, the address, the password.
func TestRegisterRefusesBeforeAnythingIsStored(t *testing.T) {
	switches := &tenantcfg.AuthMethods{EmailAccount: &tenantcfg.AuthMethod{Enabled: true}}
	s := &server{
		lic: licenceWithModules(t, lic.ModuleEmailOTP), met: metrics.New("test", prometheus.Labels{}),
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) { return switches, nil },
	}
	call := func(body map[string]string) (int, map[string]any) {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		s.accountRegister(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/account/register", bytes.NewReader(b)))
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	good := map[string]string{"email": "ann@example.com", "password": "correct horse", "ip": "10.0.0.5", "mac": "02:00:00:00:00:05"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range good {
			m[a] = b
		}
		m[k] = v
		return m
	}
	if code, out := call(with("email", "not-an-address")); code != 422 || out["error"] != "invalid_email" {
		t.Fatalf("invalid email: %d %v", code, out)
	}
	if code, out := call(with("password", "short")); code != 422 || out["error"] != "weak_password" {
		t.Fatalf("weak password: %d %v", code, out)
	}
	if code, out := call(with("password", "ann@example.com")); code != 422 || out["error"] != "weak_password" {
		t.Fatalf("the address as password: %d %v", code, out)
	}
	if code, _ := call(with("mac", "")); code != 400 {
		t.Fatalf("a device is required: %d", code)
	}
	if code, out := call(good); code != 503 {
		t.Fatalf("no database: nothing can be registered: %d %v", code, out)
	}
	switches = &tenantcfg.AuthMethods{GuestAccount: &tenantcfg.AuthMethod{Enabled: true}}
	if code, out := call(good); code != 403 || out["error"] != "METHOD_DISABLED" {
		t.Fatalf("switched off: %d %v", code, out)
	}
	s.lic = licenceWithModules(t, lic.ModuleSMSOTP)
	switches = &tenantcfg.AuthMethods{EmailAccount: &tenantcfg.AuthMethod{Enabled: true}}
	if code, out := call(good); code != 403 || out["error"] != "feature_not_licensed" {
		t.Fatalf("unlicensed: %d %v", code, out)
	}
}

// FORGOT always answers the same, whatever is wrong with the request.
func TestForgotAlwaysAnswersTheSame(t *testing.T) {
	s := &server{methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) { return &tenantcfg.AuthMethods{}, nil }}
	for _, body := range []string{`{"email":"ann@example.com","ip":"10.0.0.5","mac":"02:00:00:00:00:05"}`,
		`{"email":"nobody","ip":"","mac":""}`, `{}`} {
		rec := httptest.NewRecorder()
		s.accountForgot(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/account/forgot", strings.NewReader(body)))
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"queued":true`) {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

// Status, resend and verify refuse an invalid proof with one answer each.
func TestTokenRoutesRefuseWithoutAValidProof(t *testing.T) {
	s := &server{resumeKey: ssKey(t)}
	for _, h := range []http.HandlerFunc{s.accountStatus, s.accountResend} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"account_token":"v1.x.y","ip":"10.0.0.5","mac":"02:00:00:00:00:05"}`)))
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_account_token") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	for _, h := range []http.HandlerFunc{s.accountVerify, s.accountReset} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"token":"abcdefghijklmnopqrstuvwxyz","password":"correct horse"}`)))
		if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "invalid_or_used") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
}

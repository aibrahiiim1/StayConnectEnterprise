package main

// EMAIL ACCOUNTS (contract docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md): the settings' bounds and
// refusals, the suspended rule the accounts list shows, staff password refusal for self-service accounts, the
// auth-methods key change and the resend relay. The database half is in self_service_settings_integration_test.go.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func validSelfServiceBody(over map[string]any) string {
	m := map[string]any{
		"verification_required": true, "verification_deadline_hours": 24, "suspension_policy": "SUSPEND",
		"reminder_hours_before": 4, "resend_cooldown_seconds": 60, "resend_max_per_day": 5, "grace_minutes": 60,
		"max_grace_extensions": 1, "registrations_per_device_per_day": 3, "reason": "front desk asked",
	}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// The defaults and bounds are the contract's §6 table, number for number.
func TestSelfServiceDefaultsAndBoundsAreTheContracts(t *testing.T) {
	d := defaultSelfServiceSettings
	if !d.VerificationRequired || d.VerificationDeadlineHours != 24 || d.SuspensionPolicy != "SUSPEND" ||
		d.ReminderHoursBefore != 4 || d.ResendCooldownSeconds != 60 || d.ResendMaxPerDay != 5 ||
		d.GraceMinutes != 60 || d.MaxGraceExtensions != 1 || d.RegistrationsPerDevicePerDay != 3 {
		t.Fatalf("defaults drifted from the contract: %+v", d)
	}
	want := map[string][2]int{
		"verification_deadline_hours": {1, 168}, "reminder_hours_before": {0, 72},
		"resend_cooldown_seconds": {30, 3600}, "resend_max_per_day": {1, 20}, "grace_minutes": {15, 240},
		"max_grace_extensions": {0, 3}, "registrations_per_device_per_day": {1, 10},
	}
	if len(selfServiceBounds) != len(want) {
		t.Fatalf("bounds = %d entries, want %d", len(selfServiceBounds), len(want))
	}
	for _, b := range selfServiceBounds {
		w, ok := want[b.Key]
		if !ok || b.Min != w[0] || b.Max != w[1] {
			t.Errorf("%s bounds %d-%d, want %v", b.Key, b.Min, b.Max, w)
		}
		if b.Default != d.numeric(b.Key) {
			t.Errorf("%s default %d disagrees with the defaults struct %d", b.Key, b.Default, d.numeric(b.Key))
		}
		if b.Unit == "" {
			t.Errorf("%s has no unit", b.Key)
		}
	}
	if _, _, ok := validateSelfServiceSettings(d); !ok {
		t.Fatal("the defaults do not validate")
	}
}

// Every edge of every bound: the limits themselves are accepted, one past them is refused.
func TestSelfServiceBoundsAreEnforcedAtEveryEdge(t *testing.T) {
	for _, b := range selfServiceBounds {
		for _, c := range []struct {
			n  int
			ok bool
		}{{b.Min, true}, {b.Max, true}, {b.Min - 1, false}, {b.Max + 1, false}} {
			v := defaultSelfServiceSettings
			v.VerificationDeadlineHours = 168 // room for the reminder's own bound
			switch b.Key {
			case "verification_deadline_hours":
				v.VerificationDeadlineHours, v.ReminderHoursBefore = c.n, 0
			case "reminder_hours_before":
				v.ReminderHoursBefore = c.n
			case "resend_cooldown_seconds":
				v.ResendCooldownSeconds = c.n
			case "resend_max_per_day":
				v.ResendMaxPerDay = c.n
			case "grace_minutes":
				v.GraceMinutes = c.n
			case "max_grace_extensions":
				v.MaxGraceExtensions = c.n
			case "registrations_per_device_per_day":
				v.RegistrationsPerDevicePerDay = c.n
			}
			key, _, ok := validateSelfServiceSettings(v)
			if ok != c.ok {
				t.Errorf("%s=%d: ok=%v, want %v", b.Key, c.n, ok, c.ok)
			}
			if !ok && key != b.Key {
				t.Errorf("%s=%d refused as %s", b.Key, c.n, key)
			}
		}
	}
}

func TestSelfServiceReminderMustComeBeforeTheDeadline(t *testing.T) {
	v := defaultSelfServiceSettings
	v.VerificationDeadlineHours, v.ReminderHoursBefore = 4, 4
	if key, _, ok := validateSelfServiceSettings(v); ok || key != "reminder_hours_before" {
		t.Fatalf("a reminder at the deadline itself was accepted (%s)", key)
	}
	v.ReminderHoursBefore = 0 // no reminder: valid with any deadline
	v.VerificationDeadlineHours = 1
	if _, _, ok := validateSelfServiceSettings(v); !ok {
		t.Fatal("reminder 0 (off) must be valid with a one-hour deadline")
	}
	v.SuspensionPolicy = "BLOCK"
	if key, _, ok := validateSelfServiceSettings(v); ok || key != "suspension_policy" {
		t.Fatal("an unknown suspension policy was accepted")
	}
	v.SuspensionPolicy = "WARN_ONLY"
	if _, _, ok := validateSelfServiceSettings(v); !ok {
		t.Fatal("WARN_ONLY must be accepted")
	}
}

// The PUT refuses a missing value, an out-of-bounds value and a missing reason BEFORE the password is checked,
// and a valid body without the password is refused by the step-up -- all without touching the database.
func TestSelfServicePutRefusals(t *testing.T) {
	s := &server{tenantID: "11111111-1111-1111-1111-111111111111", siteID: "22222222-2222-2222-2222-222222222222"}
	h := s.selfServiceSettingsRoutes()
	for _, c := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"missing verification_required", validSelfServiceBody(map[string]any{"verification_required": nil}), 400, "invalid_setting"},
		{"missing grace", validSelfServiceBody(map[string]any{"grace_minutes": nil}), 400, "invalid_setting"},
		{"deadline too long", validSelfServiceBody(map[string]any{"verification_deadline_hours": 169}), 400, "invalid_setting"},
		{"grace too short", validSelfServiceBody(map[string]any{"grace_minutes": 14}), 400, "invalid_setting"},
		{"four graces", validSelfServiceBody(map[string]any{"max_grace_extensions": 4}), 400, "invalid_setting"},
		{"eleven registrations", validSelfServiceBody(map[string]any{"registrations_per_device_per_day": 11}), 400, "invalid_setting"},
		{"bad policy", validSelfServiceBody(map[string]any{"suspension_policy": "NEVER"}), 400, "invalid_setting"},
		{"no reason", validSelfServiceBody(map[string]any{"reason": nil}), 400, "reason_required"},
		{"short reason", validSelfServiceBody(map[string]any{"reason": "no"}), 400, "reason_required"},
		{"unknown field", validSelfServiceBody(map[string]any{"tenant_id": "x"}), 400, "invalid"},
		{"no password", validSelfServiceBody(nil), 401, "reauth_required"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, withSession(httptest.NewRequest(http.MethodPut, "/", strings.NewReader(c.body)), "site_admin"))
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%s: got %d %s, want %d %s", c.name, rec.Code, rec.Body.String(), c.status, c.code)
		}
	}
}

// Without a site nothing is saved, but the screen still gets the defaults and the bounds.
func TestSelfServiceGetBeforeAssignmentShowsDefaults(t *testing.T) {
	s := &server{}
	rec := httptest.NewRecorder()
	s.selfServiceSettingsRoutes().ServeHTTP(rec, withSession(httptest.NewRequest(http.MethodGet, "/", nil), "site_viewer"))
	var body struct {
		Settings selfServiceSettings `json:"settings"`
		Limits   struct {
			Bounds []selfServiceBound `json:"bounds"`
		} `json:"limits"`
		Awaiting bool `json:"awaiting_assignment"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 || !body.Awaiting ||
		body.Settings != defaultSelfServiceSettings || len(body.Limits.Bounds) != len(selfServiceBounds) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// Who may read and change the settings is exactly who may read and change the sign-in methods: the route is
// mounted behind the auth-methods key (main.go), so the documented matrix row for auth-methods governs it.
func TestSelfServiceSettingsAreGovernedByAuthMethods(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(src), `r.Route("/self-service-settings"`)
	if i < 0 {
		t.Fatal("/self-service-settings is not mounted")
	}
	block := string(src[i:])
	if end := strings.Index(block, "})"); end > 0 {
		block = block[:end]
	}
	if !strings.Contains(block, `s.resourcePermission("auth-methods")`) {
		t.Fatalf("/self-service-settings is not behind the auth-methods permission:\n%s", block)
	}
}

// THE SUSPENDED RULE (contract §3): unverified, past the deadline, no live grace, on a site that requires
// verification and suspends. Every other combination is not suspended.
func TestAccountSuspendedRule(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Minute)
	set := defaultSelfServiceSettings
	acct := func(mut func(*iamv2Account)) iamv2Account {
		a := iamv2Account{SelfRegistered: true, VerificationDeadline: &past}
		if mut != nil {
			mut(&a)
		}
		return a
	}
	if !accountSuspended(acct(nil), set, now) {
		t.Fatal("unverified past the deadline must be suspended")
	}
	for name, a := range map[string]iamv2Account{
		"staff-created":       acct(func(a *iamv2Account) { a.SelfRegistered = false }),
		"verified":            acct(func(a *iamv2Account) { a.EmailVerifiedAt = &past }),
		"before deadline":     acct(func(a *iamv2Account) { a.VerificationDeadline = &future }),
		"no deadline":         acct(func(a *iamv2Account) { a.VerificationDeadline = nil }),
		"grace still running": acct(func(a *iamv2Account) { a.GraceUntil = &future }),
	} {
		if accountSuspended(a, set, now) {
			t.Errorf("%s must not be suspended", name)
		}
	}
	if !accountSuspended(acct(func(a *iamv2Account) { a.GraceUntil = &past }), set, now) {
		t.Error("an ended grace suspends again")
	}
	warn := set
	warn.SuspensionPolicy = "WARN_ONLY"
	if accountSuspended(acct(nil), warn, now) {
		t.Error("WARN_ONLY never suspends")
	}
	off := set
	off.VerificationRequired = false
	if accountSuspended(acct(nil), off, now) {
		t.Error("verification not required never suspends")
	}

	// Unreadable settings leave the flag OUT rather than guessing; readable ones always set it.
	a := acct(nil)
	a.withVerificationState(nil, now)
	if a.Suspended != nil {
		t.Error("suspended must be absent when the settings could not be read")
	}
	a.withVerificationState(&set, now)
	if a.Suspended == nil || !*a.Suspended || a.EmailVerified {
		t.Errorf("state = %+v", a)
	}
}

// Contract §5: no staff password set or reveal for a self-service account; staff-created accounts unchanged.
func TestSelfServiceAccountsRefuseStaffPasswords(t *testing.T) {
	if st, _, _ := selfServicePasswordRefusal(false); st != 0 {
		t.Fatalf("a staff-created account was refused: %d", st)
	}
	st, code, msg := selfServicePasswordRefusal(true)
	if st != http.StatusForbidden || code != "self_service_account" || !strings.Contains(msg, "reset") {
		t.Fatalf("got %d %s %q", st, code, msg)
	}
}

// AUTH-METHODS: the email method's key is email_account; the old email key is refused; email_account is gated by
// the email_otp module and may lead the portal.
func TestAuthMethodsEmailAccountKey(t *testing.T) {
	if err := validateAuthMethodsPatch(patch(t, `{"email_account":{"enabled":true}}`)); err != nil {
		t.Fatalf("email_account refused: %v", err)
	}
	if err := validateAuthMethodsPatch(patch(t, `{"email_account":"on"}`)); err == nil {
		t.Fatal("a non-object email_account was accepted")
	}
	if err := validateAuthMethodsPatch(patch(t, `{"email":{"enabled":true}}`)); err == nil ||
		!strings.Contains(err.Error(), "email_account") {
		t.Fatalf("the retired email key must be refused, naming its replacement: %v", err)
	}
	if err := validateAuthMethodsPatch(patch(t, `{"portal":{"primary_method":"email_account"}}`)); err != nil {
		t.Fatalf("email_account must be able to lead the portal: %v", err)
	}
	if err := validateAuthMethodsPatch(patch(t, `{"portal":{"primary_method":"email"}}`)); err == nil {
		t.Fatal("the retired email primary method was accepted")
	}
	// The mail-service allowlist is gone: its keys are no longer validated as such (and no longer typed).
	if m, ok := authMethodModules["email_account"]; !ok || m.module != "email_otp" {
		t.Fatalf("email_account is not gated by the email_otp module: %+v", m)
	}
	if _, ok := authMethodModules["email"]; ok {
		t.Fatal("the retired email key still has a module gate")
	}
}

// The mail-provider presets route is removed with the mail-service allowlist.
func TestAuthMethodsMailProvidersRouteIsRemoved(t *testing.T) {
	s := &server{}
	rec := httptest.NewRecorder()
	s.authMethodsRoutes().ServeHTTP(rec, withSession(httptest.NewRequest(http.MethodGet, "/mail-providers", nil), "site_admin"))
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /auth-methods/mail-providers still answers %d", rec.Code)
	}
}

// RESEND: scd's answer, mapped. 2xx is queued; 404 means this scd does not serve the route (the account was found
// before scd was asked); scd's own refusals are relayed as they are.
func TestResendVerificationRelay(t *testing.T) {
	for _, c := range []struct {
		scdStatus int
		scdBody   string
		want      int
		says      string
		accepted  bool
	}{
		{http.StatusAccepted, `{}`, http.StatusAccepted, "queued", true},
		{http.StatusNotFound, `{"error":"not_found"}`, http.StatusNotImplemented, "resend_unavailable", false},
		{http.StatusTooManyRequests, `{"error":"resend_wait","retry_after_seconds":42}`, http.StatusTooManyRequests, "retry_after_seconds", false},
	} {
		var gotPath, gotBody string
		sock := fakeUnixService(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			gotBody = string(raw)
			w.WriteHeader(c.scdStatus)
			_, _ = w.Write([]byte(c.scdBody))
		})
		s := &server{scd: newSCDClient(sock)}
		rec := httptest.NewRecorder()
		ok := s.relayResendVerification(rec, withSession(httptest.NewRequest(http.MethodPost, "/", nil), "front_office_operator"), "acc-1")
		if ok != c.accepted || rec.Code != c.want || !strings.Contains(rec.Body.String(), c.says) {
			t.Errorf("scd %d: got %v %d %s", c.scdStatus, ok, rec.Code, rec.Body.String())
		}
		if gotPath != "/v1/admin/accounts/acc-1/resend-verification" || !strings.Contains(gotBody, "it@hotel.test") {
			t.Errorf("scd was asked %s %s", gotPath, gotBody)
		}
	}
}

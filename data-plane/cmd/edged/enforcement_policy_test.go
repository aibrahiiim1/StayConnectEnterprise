package main

// Enforcement policy and licence clock reset handlers: validation, atomic apply, operator attribution, RBAC and the
// refusal text. No database: the policy store is replaced, and scd is a canned round tripper.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeEnforcementStore struct {
	pol       enforcementPolicy
	hist      []enforcementChange
	applied   []enforcementFieldChange
	operator  string
	reason    string
	applyErr  error
	applyCall int
}

func (f *fakeEnforcementStore) read(context.Context) (enforcementPolicy, error) { return f.pol, nil }
func (f *fakeEnforcementStore) history(context.Context, int) ([]enforcementChange, error) {
	return f.hist, nil
}
func (f *fakeEnforcementStore) apply(_ context.Context, ch []enforcementFieldChange, op, reason string) error {
	f.applyCall++
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied, f.operator, f.reason = ch, op, reason
	return nil
}

func shippedPolicy() enforcementPolicy {
	return enforcementPolicy{PrivateDenyEnabled: true, PrivateAllowCIDRs: []string{}, AllowUpstreamInfrastructure: true,
		CutoffSeconds: 5, OutageContinuityMinutes: 60, ConfigVersion: 1}
}

func enforcementRouter(s *server) http.Handler {
	r := chi.NewRouter()
	r.Use(s.requireAuth)
	mountResource(r, s, "network", func() http.Handler {
		rr := chi.NewRouter()
		rr.Get("/enforcement-policy", s.getEnforcementPolicy)
		rr.Patch("/enforcement-policy", s.patchEnforcementPolicy)
		return rr
	})
	return r
}

func enfDo(t *testing.T, h http.Handler, method, cookie, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, "/network/enforcement-policy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func newEnfServer(st *fakeEnforcementStore) *server {
	return &server{sessions: newSessionStore(time.Hour), enforcementStoreOverride: st,
		enforcementPasswordOK: func(p string) bool { return p == "right-password" }}
}

func TestEnforcementPolicy_GetReturnsValuesBoundsDefaultsAndHistory(t *testing.T) {
	st := &fakeEnforcementStore{pol: shippedPolicy(), hist: []enforcementChange{{ChangedBy: "migration 0166", Reason: "defaults created", Field: "all"}}}
	s := newEnfServer(st)
	h := enforcementRouter(s)
	code, body := enfDo(t, h, http.MethodGet, loginAs(t, s, []string{"site_viewer"}), "")
	if code != 200 {
		t.Fatalf("code %d: %s", code, body)
	}
	var out struct {
		Policy   enforcementPolicy       `json:"policy"`
		Settings []enforcementSettingOut `json:"settings"`
		Limits   map[string]int          `json:"limits"`
		History  []enforcementChange     `json:"history"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Policy.PrivateDenyEnabled || out.Policy.MACBindingEnabled || out.Policy.CutoffSeconds != 5 {
		t.Errorf("the shipped defaults are not reported: %+v", out.Policy)
	}
	if len(out.Settings) != 6 || out.Limits["max_cidrs"] != 64 || out.Limits["cutoff_max"] != 60 || len(out.History) != 1 {
		t.Errorf("settings/limits/history missing: %d settings, %v, %d history", len(out.Settings), out.Limits, len(out.History))
	}
	for _, d := range out.Settings {
		if d.Explanation == "" || d.Label == "" || d.Default == nil {
			t.Errorf("setting %s lacks a label, default or explanation", d.Field)
		}
	}
}

func TestEnforcementPolicy_PatchAppliesOnlyChangedFieldsAtomicallyWithOperatorAndReason(t *testing.T) {
	st := &fakeEnforcementStore{pol: shippedPolicy()}
	s := newEnfServer(st)
	h := enforcementRouter(s)
	cookie := loginAs(t, s, []string{"hotel_it_manager"})
	code, body := enfDo(t, h, http.MethodPatch, cookie,
		`{"private_deny_enabled":true,"mac_binding_enabled":true,"cutoff_seconds":10,"private_allow_cidrs":["192.168.50.0/24","192.168.50.0/24","10.9.0.5/32"],"reason":"enable AD-23 on the test appliance","password":"right-password"}`)
	if code != 200 {
		t.Fatalf("code %d: %s", code, body)
	}
	got := map[string]string{}
	for _, c := range st.applied {
		got[c.Field] = c.Value
	}
	want := map[string]string{"mac_binding_enabled": "true", "cutoff_seconds": "10", "private_allow_cidrs": "10.9.0.5/32,192.168.50.0/24"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("applied %v, want %v (an unchanged field must not be written)", got, want)
	}
	if st.operator != "op@example.invalid" || st.reason != "enable AD-23 on the test appliance" || st.applyCall != 1 {
		t.Errorf("operator %q reason %q calls %d", st.operator, st.reason, st.applyCall)
	}
}

func TestEnforcementPolicy_PatchRefusesBadInputWith400AndNeverWrites(t *testing.T) {
	long := make([]string, 65)
	for i := range long {
		long[i] = fmt.Sprintf("10.%d.0.0/16", i)
	}
	longJSON, _ := json.Marshal(long)
	cases := []struct{ name, body, msg string }{
		{"no reason", `{"mac_binding_enabled":true,"password":"right-password"}`, "reason is required"},
		{"blank reason", `{"mac_binding_enabled":true,"reason":"   ","password":"right-password"}`, "reason is required"},
		{"cutoff low", `{"cutoff_seconds":0,"reason":"because","password":"right-password"}`, "between 1 and 60"},
		{"cutoff high", `{"cutoff_seconds":61,"reason":"because","password":"right-password"}`, "between 1 and 60"},
		{"continuity high", `{"outage_continuity_minutes":121,"reason":"because","password":"right-password"}`, "between 0 and 120"},
		{"continuity low", `{"outage_continuity_minutes":-1,"reason":"because","password":"right-password"}`, "between 0 and 120"},
		{"too wide", `{"private_allow_cidrs":["10.0.0.0/7"],"reason":"because","password":"right-password"}`, "between /8 and /32"},
		{"ipv6", `{"private_allow_cidrs":["fd00::/8"],"reason":"because","password":"right-password"}`, "IPv4"},
		{"junk", `{"private_allow_cidrs":["nonsense"],"reason":"because","password":"right-password"}`, "IPv4"},
		{"host bits", `{"private_allow_cidrs":["192.168.1.7/24"],"reason":"because","password":"right-password"}`, "192.168.1.0/24"},
		{"too many", `{"private_allow_cidrs":` + string(longJSON) + `,"reason":"because","password":"right-password"}`, "at most 64"},
		{"unknown field", `{"mac_bindin":true,"reason":"because","password":"right-password"}`, "could not be read"},
		{"nothing changed", `{"private_deny_enabled":true,"reason":"because","password":"right-password"}`, "already in effect"},
	}
	for _, c := range cases {
		st := &fakeEnforcementStore{pol: shippedPolicy()}
		s := newEnfServer(st)
		code, body := enfDo(t, enforcementRouter(s), http.MethodPatch, loginAs(t, s, []string{"hotel_it_manager"}), c.body)
		if code != 400 || !strings.Contains(body, c.msg) {
			t.Errorf("%s: code %d body %s (want 400 containing %q)", c.name, code, body, c.msg)
		}
		if st.applyCall != 0 {
			t.Errorf("%s: a refused change was written", c.name)
		}
	}
}

func TestEnforcementPolicy_DatabaseValidationSurfacesAs400(t *testing.T) {
	st := &fakeEnforcementStore{pol: shippedPolicy(), applyErr: &pgconn.PgError{Code: "22023", Message: "cutoff_seconds must be between 1 and 60"}}
	s := newEnfServer(st)
	code, body := enfDo(t, enforcementRouter(s), http.MethodPatch, loginAs(t, s, []string{"site_admin"}), `{"mac_binding_enabled":true,"reason":"because","password":"right-password"}`)
	if code != 400 || !strings.Contains(body, "must be between 1 and 60") {
		t.Errorf("code %d body %s", code, body)
	}
	st.applyErr = errors.New("connection reset")
	code, _ = enfDo(t, enforcementRouter(s), http.MethodPatch, loginAs(t, s, []string{"site_admin"}), `{"mac_binding_enabled":true,"reason":"because","password":"right-password"}`)
	if code != 500 {
		t.Errorf("an infrastructure failure must be 500, got %d", code)
	}
}

func TestEnforcementPolicy_RolesAndAnonymous(t *testing.T) {
	cases := []struct {
		role        string
		read, write int
	}{
		{"site_admin", 200, 200},
		{"hotel_it_manager", 200, 200},
		{"site_viewer", 200, 403},
		{"voucher_operator", 403, 403},
		{"front_office_operator", 403, 403},
	}
	for _, c := range cases {
		st := &fakeEnforcementStore{pol: shippedPolicy()}
		s := newEnfServer(st)
		h := enforcementRouter(s)
		cookie := loginAs(t, s, []string{c.role})
		if code, _ := enfDo(t, h, http.MethodGet, cookie, ""); code != c.read {
			t.Errorf("%s read = %d, want %d", c.role, code, c.read)
		}
		if code, _ := enfDo(t, h, http.MethodPatch, cookie, `{"mac_binding_enabled":true,"reason":"because","password":"right-password"}`); code != c.write {
			t.Errorf("%s write = %d, want %d", c.role, code, c.write)
		}
	}
	s := newEnfServer(&fakeEnforcementStore{pol: shippedPolicy()})
	if code, _ := enfDo(t, enforcementRouter(s), http.MethodGet, "", ""); code != 401 {
		t.Errorf("anonymous = %d, want 401", code)
	}
}

// ---- licence clock reset ----

type scdStub struct {
	status int
	body   string
}

func (c scdStub) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: c.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(c.body))}, nil
}

func clockResetRouter(s *server) http.Handler {
	r := chi.NewRouter()
	r.Use(s.requireAuth)
	r.With(s.requireRole("license", permWrite)).Post("/license/clock-reset", s.licenseClockReset)
	return r
}

func clockDo(t *testing.T, h http.Handler, cookie, body string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/license/clock-reset", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestLicenseClockReset_RequiresReasonAndWritePermission(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour), scd: &scdClient{http: &http.Client{Transport: scdStub{200, `{"status":"reset"}`}}}}
	h := clockResetRouter(s)
	if code, _ := clockDo(t, h, loginAs(t, s, []string{"hotel_it_manager"}), `{"reason":"  ","password":"right-password"}`); code != 403 {
		// hotel_it_manager holds license READ only: the button must not be offered to it either.
		t.Errorf("hotel_it_manager = %d, want 403", code)
	}
	cookie := loginAs(t, s, []string{"site_admin"})
	if code, _ := clockDo(t, h, cookie, `{"reason":"  ","password":"right-password"}`); code != 400 {
		t.Errorf("blank reason = %d, want 400", code)
	}
	if code, body := clockDo(t, h, cookie, `{"reason":"Clock was set wrong after the power cut","password":"right-password"}`); code != 200 || !strings.Contains(body, "reset") {
		t.Errorf("code %d body %s", code, body)
	}
}

func TestLicenseClockReset_PassesCentralsRefusalThroughVerbatim(t *testing.T) {
	msg := `{"error":"the local clock still disagrees with Central by 3h; correct the clock first"}`
	s := &server{sessions: newSessionStore(time.Hour), scd: &scdClient{http: &http.Client{Transport: scdStub{409, msg}}}}
	code, body := clockDo(t, clockResetRouter(s), loginAs(t, s, []string{"site_admin"}), `{"reason":"Clock was set wrong after the power cut","password":"right-password"}`)
	if code != 409 || !strings.Contains(body, "still disagrees with Central") {
		t.Errorf("code %d body %s", code, body)
	}
}

func TestEnforcementPolicy_PatchNeedsTheOperatorsPasswordAndWritesNothingWithout(t *testing.T) {
	for name, body := range map[string]string{
		"no password":    `{"mac_binding_enabled":true,"reason":"enable the binding"}`,
		"wrong password": `{"mac_binding_enabled":true,"reason":"enable the binding","password":"guess"}`,
	} {
		st := &fakeEnforcementStore{pol: shippedPolicy()}
		s := newEnfServer(st)
		code, out := enfDo(t, enforcementRouter(s), http.MethodPatch, loginAs(t, s, []string{"hotel_it_manager"}), body)
		if code != http.StatusUnauthorized || !strings.Contains(out, "reauth_required") {
			t.Errorf("%s: %d %s, want 401 reauth_required", name, code, out)
		}
		if st.applyCall != 0 {
			t.Errorf("%s: the policy was written without a confirmed password", name)
		}
	}
}

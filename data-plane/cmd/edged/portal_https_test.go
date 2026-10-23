package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeUnixService serves handler on a unix socket the way scd and netd do, and returns the socket path.
func fakeUnixService(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "og")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

type recordedCall struct {
	Method, Path string
	Body         string
}

type callRecorder struct {
	mu    sync.Mutex
	calls []recordedCall
}

func (c *callRecorder) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.calls = append(c.calls, recordedCall{r.Method, r.URL.Path, string(b)})
	c.mu.Unlock()
}

func (c *callRecorder) all() []recordedCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]recordedCall(nil), c.calls...)
}

func withSession(r *http.Request, roles ...string) *http.Request {
	sess := &session{OperatorID: "op-1", Email: "it@hotel.test", Roles: roles}
	return r.WithContext(context.WithValue(r.Context(), sessKey, sess))
}

func TestValidatePortalIP(t *testing.T) {
	for in, want := range map[string]string{
		"10.255.255.1":     "10.255.255.1",
		" 10.254.254.1 ":   "10.254.254.1",
		"10.253.253.1/32":  "10.253.253.1",
		"172.20.0.9":       "172.20.0.9",
		"192.168.200.1":    "192.168.200.1",
		"192.0.2.1":        "", // RFC 5737: not for local use (contract §2.1)
		"8.8.8.8":          "",
		"172.32.0.1":       "", // just outside 172.16.0.0/12
		"10.0.0.0":         "",
		"10.0.0.255":       "",
		"10.0.0.1/24":      "",
		"::ffff:10.0.0.1":  "",
		"fd00::1":          "",
		"":                 "",
		"wifi.example.com": "",
	} {
		got, msg := validatePortalIP(in)
		if got != want {
			t.Errorf("validatePortalIP(%q) = %q (%s), want %q", in, got, msg, want)
		}
		if want == "" && msg == "" {
			t.Errorf("validatePortalIP(%q) refused without a reason", in)
		}
	}
}

func TestValidatePortalDomain(t *testing.T) {
	if got := normalisePortalDomain("  WiFi.NileHotel.COM. "); got != "wifi.nilehotel.com" {
		t.Fatalf("normalise = %q", got)
	}
	for in, ok := range map[string]bool{
		"wifi.nilehotel.com":                   true,
		"guest-wifi.hotel.co.uk":               true,
		"nilehotel.com":                        false, // an apex
		"abcd1234.gw.onegate.work":             false, // OneGate's own zone
		"onegate.work":                         false,
		"wifi_.hotel.com":                      false,
		"-wifi.hotel.com":                      false,
		"wifi.hotel.c":                         false,
		"wifi.hotel.123":                       false,
		"":                                     false,
		"https://wifi.nilehotel.com":           false,
		strings.Repeat("a", 64) + ".hotel.com": false,
	} {
		msg := validatePortalDomain(in)
		if ok && msg != "" {
			t.Errorf("%q refused: %s", in, msg)
		}
		if !ok && msg == "" {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestEvaluatePortalHTTPS(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	ago := now.Add(-48 * time.Hour)
	in := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	installed := []portalCertificate{{Hostname: "abcd1234.gw.onegate.work", InstalledAt: &ago, NotAfter: in(60 * 24 * time.Hour)}}
	active := portalTLSState{HTTPSActive: true, CanonicalHostname: "abcd1234.gw.onegate.work", OneGateHostname: "abcd1234.gw.onegate.work"}
	fallback := func(reason string) portalTLSState {
		return portalTLSState{FallbackReason: reason, FallbackSince: &ago}
	}
	cases := []struct {
		name   string
		st     portalTLSState
		certs  []portalCertificate
		level  string
		reason string
		says   string
	}{
		{"active", active, installed, portalCheckOK, "", "served over HTTPS"},
		{"not configured", fallback("NOT_CONFIGURED"), nil, portalCheckWarn, "NOT_CONFIGURED", "not been set up"},
		{"no hostname", fallback("NO_HOSTNAME"), nil, portalCheckWarn, "NO_HOSTNAME", "portal hostname"},
		{"first certificate pending", fallback("NO_CERTIFICATE"), nil, portalCheckWarn, "NO_CERTIFICATE", "no valid certificate"},
		{"certificate lost after working", fallback("NO_CERTIFICATE"), installed, portalCheckCritical, "NO_CERTIFICATE", "was working before"},
		{"expired", fallback("CERTIFICATE_EXPIRED"), installed, portalCheckCritical, "CERTIFICATE_EXPIRED", "expired"},
		{"address down", fallback("PORTAL_ADDRESS_NOT_ACTIVE"), installed, portalCheckCritical, "PORTAL_ADDRESS_NOT_ACTIVE", "apply the network"},
		{"central unreachable before any certificate", fallback("CENTRAL_UNREACHABLE"), nil, portalCheckWarn, "CENTRAL_UNREACHABLE", "cannot be reached"},
		{"not configured after having worked", fallback("NOT_CONFIGURED"), installed, portalCheckCritical, "NOT_CONFIGURED", "was working before"},
		{"expiring unrenewed", active, []portalCertificate{{Hostname: "abcd1234.gw.onegate.work", InstalledAt: &ago,
			NotAfter: in(3 * 24 * time.Hour), LastError: "order refused"}}, portalCheckCritical, "CERTIFICATE_EXPIRING", "order refused"},
		{"eight days left is fine", active, []portalCertificate{{Hostname: "x.gw.onegate.work", InstalledAt: &ago,
			NotAfter: in(8 * 24 * time.Hour)}}, portalCheckOK, "", "HTTPS"},
		{"a pending certificate never installed is not an expiry", active, []portalCertificate{{Hostname: "wifi.hotel.com",
			NotAfter: in(time.Hour)}}, portalCheckOK, "", "HTTPS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := evaluatePortalHTTPS(tc.st, tc.certs, now)
			if c.Name != "portal_https" || c.Level != tc.level || c.Reason != tc.reason {
				t.Fatalf("got %+v, want level=%s reason=%s", c, tc.level, tc.reason)
			}
			if !strings.Contains(c.Message, tc.says) {
				t.Fatalf("message %q does not say %q", c.Message, tc.says)
			}
			if c.Level != portalCheckOK && tc.st.FallbackSince != nil && !strings.Contains(c.Message, "since 2026-10-11") {
				t.Fatalf("a fallback message should say since when: %q", c.Message)
			}
		})
	}
}

func TestPortalHTTPSAttention(t *testing.T) {
	if _, ok := portalHTTPSAttention(nil); ok {
		t.Fatal("unknown state raised an attention item")
	}
	if _, ok := portalHTTPSAttention(&portalHTTPSCheck{Level: portalCheckOK}); ok {
		t.Fatal("ok raised an attention item")
	}
	warn, _ := portalHTTPSAttention(&portalHTTPSCheck{Level: portalCheckWarn, Message: "m"})
	crit, _ := portalHTTPSAttention(&portalHTTPSCheck{Level: portalCheckCritical, Message: "m"})
	if warn.Severity != "warn" || crit.Severity != "err" || crit.Href != "/portal-https" {
		t.Fatalf("warn=%+v crit=%+v", warn, crit)
	}
	items := deriveAttention(attentionInput{PortalHTTPS: &portalHTTPSCheck{Level: portalCheckCritical, Message: "down"}})
	if len(items) != 1 || items[0].ID != "portal-https" || items[0].Detail != "down" {
		t.Fatalf("overview attention = %+v", items)
	}
}

func TestPortalHTTPSRoleMatrix(t *testing.T) {
	for role, want := range map[string]perm{
		"site_admin": permWrite, "hotel_it_manager": permWrite, "tenant_operator": permWrite,
		"site_viewer": permRead, "front_office_operator": permNone, "guest_relations_operator": permNone,
		"voucher_operator": permNone, "payments_operator": permNone,
	} {
		read := permFor([]string{role}, "portal-https", permRead)
		write := permFor([]string{role}, "portal-https", permWrite)
		if read != (want >= permRead) || write != (want >= permWrite) {
			t.Errorf("%s: read=%v write=%v, want %v", role, read, write, want)
		}
	}
}

func TestPortalHTTPSMatchesTheDocumentedRoleMatrix(t *testing.T) {
	row := docMatrixRow(t, "portal-https")
	for role := range rolePerms {
		if role == "tenant_admin" || role == "tenant_operator" {
			continue
		}
		cell, ok := row[role]
		if !ok {
			t.Errorf("the documented portal-https row says nothing about %s", role)
			continue
		}
		read := permFor([]string{role}, "portal-https", permRead)
		write := permFor([]string{role}, "portal-https", permWrite)
		switch cell {
		case "W":
			if !write {
				t.Errorf("%s documented W but the code refuses the write", role)
			}
		case "R":
			if !read || write {
				t.Errorf("%s documented R but read=%v write=%v", role, read, write)
			}
		default:
			if read || write {
				t.Errorf("%s documented %q but read=%v write=%v", role, cell, read, write)
			}
		}
	}
}

// The middleware enforces portal_https.manage: a read-only role gets GET and is refused writes before any handler.
func TestPortalHTTPSWritesNeedManage(t *testing.T) {
	s := &server{}
	reached := false
	h := s.resourcePermission("portal-https")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		role, method string
		want         int
	}{
		{"site_viewer", http.MethodGet, http.StatusNoContent},
		{"site_viewer", http.MethodPut, http.StatusForbidden},
		{"site_viewer", http.MethodPost, http.StatusForbidden},
		{"site_viewer", http.MethodDelete, http.StatusForbidden},
		{"front_office_operator", http.MethodGet, http.StatusForbidden},
		{"hotel_it_manager", http.MethodPut, http.StatusNoContent},
	} {
		reached = false
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, withSession(httptest.NewRequest(tc.method, "/", nil), tc.role))
		if rec.Code != tc.want {
			t.Errorf("%s %s: got %d want %d", tc.role, tc.method, rec.Code, tc.want)
		}
		if reached != (tc.want == http.StatusNoContent) {
			t.Errorf("%s %s: handler reached=%v", tc.role, tc.method, reached)
		}
	}
}

// Changes to the portal or its certificates require the password; without one nothing downstream is touched.
func TestPortalHTTPSChangesRequireStepUp(t *testing.T) {
	rec := &callRecorder{}
	sock := fakeUnixService(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusAccepted)
	})
	s := &server{scd: newSCDClient(sock), netd: newNetdClient(sock)}
	h := s.portalHTTPSRoutes()
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/address", `{"portal_ip":"10.255.255.1"}`},
		{http.MethodPut, "/custom-domain", `{"fqdn":"wifi.nilehotel.com"}`},
		{http.MethodDelete, "/custom-domain", ``},
		{http.MethodPost, "/renew", `{}`},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withSession(httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)), "site_admin"))
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "reauth_required") {
			t.Errorf("%s %s: got %d %s, want 401 reauth_required", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if calls := rec.all(); len(calls) != 0 {
		t.Fatalf("a refused step-up still reached scd/netd: %+v", calls)
	}
}

func TestPortalHTTPSRefusesBadInputFirst(t *testing.T) {
	s := &server{}
	h := s.portalHTTPSRoutes()
	for _, tc := range []struct{ path, body, says string }{
		{"/address", `{"portal_ip":"192.0.2.1","password":"x"}`, "private address"},
		{"/address", `{"portal_ip":"10.0.0.1/24","password":"x"}`, "IPv4"},
		{"/custom-domain", `{"fqdn":"x.gw.onegate.work","password":"x"}`, "onegate.work"},
		{"/custom-domain", `{"fqdn":"nilehotel.com","password":"x"}`, "subdomain"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withSession(httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(tc.body)), "site_admin"))
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), tc.says) {
			t.Errorf("%s %s: got %d %s", tc.path, tc.body, w.Code, w.Body.String())
		}
	}
}

func TestPortalIPValidationAsksNetdAndFailsClosed(t *testing.T) {
	rec := &callRecorder{}
	sock := fakeUnixService(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"issues":[{"code":"overlaps_guest_network","message":"10.255.255.0/24 is the Lobby network"}]}`))
	})
	s := &server{netd: newNetdClient(sock)}
	ok, issues, unavailable := s.validatePortalIPWithNetd(context.Background(), "10.255.255.1")
	if ok || unavailable != "" || len(issues) != 1 || issues[0].Code != "overlaps_guest_network" {
		t.Fatalf("ok=%v issues=%+v unavailable=%q", ok, issues, unavailable)
	}
	calls := rec.all()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/v1/portal-ip/validate" {
		t.Fatalf("netd calls = %+v", calls)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].Body), &body); err != nil || body["portal_ip"] != "10.255.255.1" {
		t.Fatalf("netd body = %s", calls[0].Body)
	}

	// Unreachable netd is a refusal, never an approval.
	dead := &server{netd: newNetdClient(filepath.Join(t.TempDir(), "nothing-here"))}
	if ok, _, unavailable := dead.validatePortalIPWithNetd(context.Background(), "10.255.255.1"); ok || unavailable == "" {
		t.Fatalf("an unreachable netd approved the address (ok=%v)", ok)
	}
	if ok, _, unavailable := (&server{}).validatePortalIPWithNetd(context.Background(), "10.255.255.1"); ok || unavailable == "" {
		t.Fatal("a missing netd client approved the address")
	}
	// netd answering ok=false with no issue still refuses, with a reason.
	empty := fakeUnixService(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":false}`)) })
	if ok, issues, _ := (&server{netd: newNetdClient(empty)}).validatePortalIPWithNetd(context.Background(), "10.255.255.1"); ok || len(issues) == 0 {
		t.Fatalf("ok=%v issues=%v", ok, issues)
	}
}

func TestKickPortalTLSCallsSCD(t *testing.T) {
	rec := &callRecorder{}
	sock := fakeUnixService(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusAccepted)
	})
	s := &server{scd: newSCDClient(sock)}
	if !s.kickPortalTLS(context.Background()) {
		t.Fatal("a 202 kick reported failure")
	}
	calls := rec.all()
	if len(calls) != 1 || calls[0].Method != http.MethodPost || calls[0].Path != "/v1/portal/tls/kick" {
		t.Fatalf("scd calls = %+v", calls)
	}
	if (&server{}).kickPortalTLS(context.Background()) {
		t.Fatal("a missing scd client reported a kick")
	}
}

func TestPortalHostnamesAndCallbackURLs(t *testing.T) {
	st := portalTLSState{OneGateHostname: "abcd1234.gw.onegate.work", CustomDomain: "wifi.nilehotel.com",
		CustomDomainStatus: "VERIFIED", HTTPSActive: true, CanonicalHostname: "wifi.nilehotel.com"}
	got := portalHostnames(st)
	if len(got) != 2 || got[0].URL != "https://abcd1234.gw.onegate.work/auth/social/callback" ||
		got[1].URL != "https://wifi.nilehotel.com/auth/social/callback" || got[0].Canonical || !got[1].Canonical {
		t.Fatalf("hostnames = %+v", got)
	}
	st.CustomDomainStatus = "REMOVED"
	if got := portalHostnames(st); len(got) != 1 {
		t.Fatalf("a removed hotel domain is still offered: %+v", got)
	}
	if got := portalHostnames(portalTLSState{}); len(got) != 0 {
		t.Fatalf("no hostnames yet, got %+v", got)
	}
}

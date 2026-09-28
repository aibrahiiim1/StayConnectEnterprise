package main

// WHATSAPP ONE-TIME CODE: its own licence module, its own Sign-in methods switch, its own sender.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/otp"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/whatsapp"
	lic "github.com/stayconnect/enterprise/license"
)

// licenceWithModules installs a vendor-signed, production-profile (required) licence authorising exactly ids.
func licenceWithModules(t *testing.T, ids ...string) *licstate.Manager {
	t.Helper()
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubPath := filepath.Join(dir, "vendor.pub")
	if err := os.WriteFile(pubPath, pub, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mods, err := lic.ModulesFromIDs(ids)
	if err != nil {
		t.Fatal(err)
	}
	doc := &lic.Document{
		LicenseID: "44444444-4444-4444-8444-444444444445", Status: lic.DocActive,
		TenantID: "22222222-2222-2222-2222-222222222222", SiteID: "33333333-3333-3333-3333-333333333333",
		IssuedAt: now.Add(-time.Hour), ValidUntil: now.AddDate(0, 6, 0), SchemaVersion: lic.CurrentSchemaVersion,
		LicenseVersion: 1, MaxConcurrentOnlineGuests: 10, Modules: mods, Features: lic.ProjectFeatures(mods),
	}
	env, err := lic.NewSigner(priv).Sign(doc)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	m := licstate.New(nil, "", filepath.Join(dir, "license"), pubPath, true)
	if _, err := m.Install(context.Background(), raw); err != nil {
		t.Fatalf("install licence: %v", err)
	}
	return m
}

type capturedWA struct {
	msgs []whatsapp.Message
}

func (c *capturedWA) Send(_ context.Context, m whatsapp.Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}

type waFixture struct {
	srv    *server
	sender *capturedWA
	issued []otp.IssueParams
}

func newWAFixture(t *testing.T, l *licstate.Manager, cfg *tenantcfg.AuthMethods) *waFixture {
	t.Helper()
	f := &waFixture{sender: &capturedWA{}}
	f.srv = &server{
		lic:      l,
		tenID:    "22222222-2222-2222-2222-222222222222",
		applID:   "55555555-5555-4555-8555-555555555555",
		whatsapp: f.sender,
		met:      metrics.New("test", prometheus.Labels{}),
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			c := *cfg
			return &c, nil
		},
		otpIssuer: func(_ context.Context, p otp.IssueParams) (*otp.Issued, error) {
			f.issued = append(f.issued, p)
			return &otp.Issued{ChallengeID: "chal-1", Code: "731946", ExpiresAt: time.Now().Add(otp.DefaultTTL)}, nil
		},
	}
	return f
}

func (f *waFixture) issue(t *testing.T, dest string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(otpIssueReq{Channel: "whatsapp", Destination: dest, IP: "10.0.0.5"})
	rec := httptest.NewRecorder()
	f.srv.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
	return rec
}

func whatsappOn() *tenantcfg.AuthMethods {
	return &tenantcfg.AuthMethods{WhatsApp: &tenantcfg.AuthMethod{Enabled: true}, SMS: &tenantcfg.AuthMethod{Enabled: true}}
}

func TestWhatsAppCodeRefusedWhenNotLicensed(t *testing.T) {
	// SMS is licensed; WhatsApp is not. An SMS grant never covers WhatsApp.
	f := newWAFixture(t, licenceWithModules(t, lic.ModuleSMSOTP), whatsappOn())
	rec := f.issue(t, "+201001234567")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "feature_not_licensed") ||
		!strings.Contains(rec.Body.String(), "whatsapp_otp") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(f.issued) != 0 || len(f.sender.msgs) != 0 {
		t.Fatal("a code was issued or sent without a licence")
	}
}

func TestWhatsAppCodeRefusedWhenMethodDisabled(t *testing.T) {
	cfg := &tenantcfg.AuthMethods{WhatsApp: &tenantcfg.AuthMethod{Enabled: false}, SMS: &tenantcfg.AuthMethod{Enabled: true}}
	f := newWAFixture(t, licenceWithModules(t, lic.ModuleWhatsAppOTP), cfg)
	rec := f.issue(t, "+201001234567")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "whatsapp auth disabled") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(f.issued) != 0 || len(f.sender.msgs) != 0 {
		t.Fatal("a code was issued for a disabled method")
	}
}

func TestWhatsAppCodeRefusesAnInvalidPhone(t *testing.T) {
	f := newWAFixture(t, licenceWithModules(t, lic.ModuleWhatsAppOTP), whatsappOn())
	if rec := f.issue(t, "0100 123"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	if len(f.issued) != 0 {
		t.Fatal("issued for an invalid phone")
	}
}

func TestWhatsAppCodeIsIssuedAndSentThroughTheWhatsAppSender(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newWAFixture(t, licenceWithModules(t, lic.ModuleWhatsAppOTP), whatsappOn())
	rec := f.issue(t, "+20 100 123 4567")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(f.issued) != 1 || f.issued[0].Channel != "whatsapp" || f.issued[0].Destination != "+201001234567" {
		t.Fatalf("issued %+v", f.issued)
	}
	if len(f.sender.msgs) != 1 {
		t.Fatalf("sent %d", len(f.sender.msgs))
	}
	m := f.sender.msgs[0]
	if m.To != "+201001234567" || m.Code != "731946" || m.TTLMinutes != int(otp.DefaultTTL.Minutes()) {
		t.Fatalf("message %+v", m)
	}
	if strings.Contains(rec.Body.String(), "731946") || strings.Contains(logs.String(), "731946") {
		t.Fatal("the code leaked into the response or the log")
	}
}

func TestUnlicensedWhatsAppNeverReachesThePortal(t *testing.T) {
	s := &server{lic: licenceWithModules(t, lic.ModuleSMSOTP)}
	cfg := whatsappOn()
	s.applyLicenseToMethods(cfg)
	if cfg.WhatsApp != nil || cfg.SMS == nil {
		t.Fatalf("unlicensed whatsapp kept (%v) or sms dropped (%v)", cfg.WhatsApp, cfg.SMS)
	}
	s = &server{lic: licenceWithModules(t, lic.ModuleWhatsAppOTP)}
	cfg = whatsappOn()
	s.applyLicenseToMethods(cfg)
	if cfg.WhatsApp == nil || cfg.SMS != nil {
		t.Fatalf("licensed whatsapp dropped (%v) or unlicensed sms kept (%v)", cfg.WhatsApp, cfg.SMS)
	}
}

func TestWhatsAppSwitchIsTheLocalEnablementOfItsModule(t *testing.T) {
	s := &server{methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
		return &tenantcfg.AuthMethods{WhatsApp: &tenantcfg.AuthMethod{Enabled: true}}, nil
	}}
	got, err := s.identitySwitches(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	if !got[lic.ModuleWhatsAppOTP] || got[lic.ModuleSMSOTP] {
		t.Fatalf("switches %v", got)
	}
}

func TestAWhatsAppCodeProvesAPhone(t *testing.T) {
	for ch, want := range map[string]string{"email": "EMAIL", "sms": "PHONE", "whatsapp": "PHONE"} {
		if got := otpFactorType(ch); got != want {
			t.Fatalf("%s -> %s, want %s", ch, got, want)
		}
	}
}

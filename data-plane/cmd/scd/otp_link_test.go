package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/otp"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

// The SMS carries a tap-to-connect link into the phone's real browser; the email one-time code is removed and these tests pin that it stays removed.

type capturedMail struct{ msgs []mail.Message }

func (c *capturedMail) Send(_ context.Context, m mail.Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}

func portalTLSWith(st PortalTLSState) *portalTLSAgent {
	a := newPortalTLSAgent(nil, nil, nil, "")
	a.snap = st
	return a
}

var httpsOn = PortalTLSState{HTTPSActive: true, Canonical: "ab12cd34.gw.onegate.work", OneGateHostname: "ab12cd34.gw.onegate.work"}

type capturedSMS struct{ msgs []sms.Message }

func (c *capturedSMS) Send(_ context.Context, m sms.Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}

func smsFixture(t *testing.T) (*server, *capturedSMS) {
	t.Helper()
	cs := &capturedSMS{}
	return &server{
		lic:    licenceWithModules(t, lic.ModuleSMSOTP, lic.ModuleEmailOTP),
		tenID:  "22222222-2222-2222-2222-222222222222",
		applID: "55555555-5555-4555-8555-555555555555",
		sms:    cs,
		mail:   &capturedMail{},
		met:    metrics.New("test", prometheus.Labels{}),
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			return &tenantcfg.AuthMethods{SMS: &tenantcfg.AuthMethod{Enabled: true}, EmailAccount: &tenantcfg.AuthMethod{Enabled: true}}, nil
		},
		otpIssuer: func(_ context.Context, p otp.IssueParams) (*otp.Issued, error) {
			return &otp.Issued{ChallengeID: "chal-1", Code: "731946", ExpiresAt: time.Now().Add(otp.DefaultTTL)}, nil
		},
	}, cs
}

// The SMS carries the link while the portal is on HTTPS and the code alone on the fallback.
func TestTheSMSCodeCarriesTheHTTPSLinkOnlyWhileHTTPSIsActive(t *testing.T) {
	s, cs := smsFixture(t)
	issue := func() string {
		cs.msgs = nil
		// An older portald's portal_base (a guest-supplied Host) still decodes and is never used for the link.
		body, _ := json.Marshal(map[string]string{"channel": "sms", "destination": "+201001234567", "ip": "10.0.0.5", "mac": "aa:bb:cc:dd:ee:05", "portal_base": "http://192.168.77.1:8380"})
		rec := httptest.NewRecorder()
		s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
		if rec.Code != http.StatusOK || len(cs.msgs) != 1 {
			t.Fatalf("issue: %d %s (sms %d)", rec.Code, rec.Body.String(), len(cs.msgs))
		}
		if strings.Contains(rec.Body.String(), "temporary_access") {
			t.Fatalf("no issue may mention a temporary-internet window any more: %s", rec.Body.String())
		}
		return cs.msgs[0].Text
	}
	if got, want := issue(), "Wi-Fi code: 731946 (expires in 10m)"; got != want {
		t.Fatalf("fallback: %q, want %q", got, want)
	}
	s.portalTLS = portalTLSWith(httpsOn)
	if got, want := issue(), "Wi-Fi code: 731946 (expires in 10m) https://ab12cd34.gw.onegate.work/connect?c=chal-1&k=731946"; got != want {
		t.Fatalf("https: %q, want %q", got, want)
	}
}

// An email code is never issued, whatever the switches say: nothing is generated or sent.
func TestAnEmailCodeIsNoLongerIssued(t *testing.T) {
	s, cs := smsFixture(t)
	issued := false
	s.otpIssuer = func(context.Context, otp.IssueParams) (*otp.Issued, error) {
		issued = true
		return nil, nil
	}
	body, _ := json.Marshal(otpIssueReq{Channel: "email", Destination: "ann@example.com", IP: "10.0.0.5", MAC: "aa:bb:cc:dd:ee:05"})
	rec := httptest.NewRecorder()
	s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported channel") {
		t.Fatalf("an email code request must be refused as an unsupported channel: %d %s", rec.Code, rec.Body.String())
	}
	if issued || len(cs.msgs) != 0 || len(s.mail.(*capturedMail).msgs) != 0 {
		t.Fatal("nothing may be generated or sent for the removed channel")
	}
}

func TestOTPLinkBaseIsTheCanonicalHTTPSOriginOrNothing(t *testing.T) {
	if got := otpLinkBase(httpsOn); got != "https://ab12cd34.gw.onegate.work" {
		t.Fatalf("active: %q", got)
	}
	for _, st := range []PortalTLSState{{}, {Canonical: "ab12cd34.gw.onegate.work"}, {HTTPSActive: true}} {
		if got := otpLinkBase(st); got != "" {
			t.Errorf("%+v -> %q, want no link", st, got)
		}
	}
}

// A code is issued to a device or not at all (migration 0110): without the kernel-resolved MAC the issue is refused, nothing generated or sent.
func TestACodeIsNeverIssuedWithoutADevice(t *testing.T) {
	s, cs := smsFixture(t)
	for _, mac := range []string{"", "not-a-mac"} {
		body, _ := json.Marshal(otpIssueReq{Channel: "sms", Destination: "+201001234567", IP: "10.0.0.5", MAC: mac})
		rec := httptest.NewRecorder()
		s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "device identity required") {
			t.Fatalf("mac %q: %d %s", mac, rec.Code, rec.Body.String())
		}
	}
	if len(cs.msgs) != 0 {
		t.Fatal("nothing may be sent for a code that was not issued")
	}
}

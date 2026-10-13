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

// THE EMAIL CARRIES A TAP-TO-CONNECT LINK into the phone's real browser (portald otp_pending.go), so a client
// who left the captive browser to read the code never has to find it again.

type capturedMail struct{ msgs []mail.Message }

func (c *capturedMail) Send(_ context.Context, m mail.Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}

func emailFixture(t *testing.T) (*server, *capturedMail) {
	t.Helper()
	cm := &capturedMail{}
	s := &server{
		lic:    licenceWithModules(t, lic.ModuleEmailOTP),
		tenID:  "22222222-2222-2222-2222-222222222222",
		applID: "55555555-5555-4555-8555-555555555555",
		mail:   cm,
		met:    metrics.New("test", prometheus.Labels{}),
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			return &tenantcfg.AuthMethods{Email: &tenantcfg.AuthMethod{Enabled: true}}, nil
		},
		otpIssuer: func(_ context.Context, p otp.IssueParams) (*otp.Issued, error) {
			return &otp.Issued{ChallengeID: "chal-1", Code: "731946", ExpiresAt: time.Now().Add(otp.DefaultTTL)}, nil
		},
	}
	return s, cm
}

// portalTLSWith is an agent that reports st (no cycle runs).
func portalTLSWith(st PortalTLSState) *portalTLSAgent {
	a := newPortalTLSAgent(nil, nil, nil, "")
	a.snap = st
	return a
}

var httpsOn = PortalTLSState{HTTPSActive: true, Canonical: "ab12cd34.gw.onegate.work", OneGateHostname: "ab12cd34.gw.onegate.work"}

// THE LINK GOES TO THE CANONICAL HTTPS PORTAL, AND ONLY THERE (contract section 4). Whatever origin portald
// reports -- the device's own Host header -- is not where the link points.
func TestTheEmailCodeCarriesALinkToTheCanonicalHTTPSPortal(t *testing.T) {
	s, cm := emailFixture(t)
	s.portalTLS = portalTLSWith(httpsOn)
	for _, base := range []string{"", "http://192.168.77.1:8380", "http://evil.example", "javascript:alert(1)"} {
		cm.msgs = nil
		body, _ := json.Marshal(otpIssueReq{Channel: "email", Destination: "ann@example.com", IP: "10.0.0.5", MAC: "aa:bb:cc:dd:ee:05", PortalBase: base})
		rec := httptest.NewRecorder()
		s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
		if rec.Code != http.StatusOK || len(cm.msgs) != 1 {
			t.Fatalf("issue: %d %s (mails %d)", rec.Code, rec.Body.String(), len(cm.msgs))
		}
		text := cm.msgs[0].Text
		if !strings.Contains(text, "731946") || !strings.Contains(text, "Or tap to connect this device:\nhttps://ab12cd34.gw.onegate.work/connect?c=chal-1&k=731946") {
			t.Fatalf("base %q: the email must carry the code and the canonical HTTPS link:\n%s", base, text)
		}
		if strings.Contains(text, "192.168.77.1") || strings.Contains(text, "evil") || strings.Contains(text, "javascript") || strings.Contains(text, "http://") {
			t.Fatalf("base %q: the link must never be built from the origin portald reported:\n%s", base, text)
		}
	}
}

// ON THE HTTP FALLBACK THE MESSAGE IS THE CODE ALONE -- the exact wording the code-only email always had.
func TestOnTheHTTPFallbackTheEmailCarriesTheCodeOnly(t *testing.T) {
	s, cm := emailFixture(t)
	for name, agent := range map[string]*portalTLSAgent{
		"no agent": nil,
		"fallback": portalTLSWith(PortalTLSState{FallbackReason: ptReasonNoCertificate, OneGateHostname: "ab12cd34.gw.onegate.work"}),
		// Canonical known but the portal address is down: still no link.
		"address down": portalTLSWith(PortalTLSState{Canonical: "ab12cd34.gw.onegate.work", FallbackReason: ptReasonAddressNotActive}),
	} {
		s.portalTLS = agent
		cm.msgs = nil
		body, _ := json.Marshal(otpIssueReq{Channel: "email", Destination: "ann@example.com", IP: "10.0.0.5", MAC: "aa:bb:cc:dd:ee:05", PortalBase: "http://192.168.77.1:8380"})
		rec := httptest.NewRecorder()
		s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
		if rec.Code != http.StatusOK || len(cm.msgs) != 1 {
			t.Fatalf("%s: issue %d", name, rec.Code)
		}
		if want := "Your one-time code is: 731946\n\nIt expires in 10 minutes."; cm.msgs[0].Text != want {
			t.Fatalf("%s: got\n%q\nwant\n%q", name, cm.msgs[0].Text, want)
		}
	}
}

type capturedSMS struct{ msgs []sms.Message }

func (c *capturedSMS) Send(_ context.Context, m sms.Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}

// The SMS carries the same link while the portal is on HTTPS, and the code alone -- its unchanged wording -- on
// the fallback (contract section 4: "Email/SMS code message").
func TestTheSMSCodeCarriesTheHTTPSLinkOnlyWhileHTTPSIsActive(t *testing.T) {
	cs := &capturedSMS{}
	s := &server{
		lic:    licenceWithModules(t, lic.ModuleSMSOTP),
		tenID:  "22222222-2222-2222-2222-222222222222",
		applID: "55555555-5555-4555-8555-555555555555",
		sms:    cs,
		met:    metrics.New("test", prometheus.Labels{}),
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			return &tenantcfg.AuthMethods{SMS: &tenantcfg.AuthMethod{Enabled: true}}, nil
		},
		otpIssuer: func(_ context.Context, p otp.IssueParams) (*otp.Issued, error) {
			return &otp.Issued{ChallengeID: "chal-1", Code: "731946", ExpiresAt: time.Now().Add(otp.DefaultTTL)}, nil
		},
	}
	issue := func() string {
		cs.msgs = nil
		body, _ := json.Marshal(otpIssueReq{Channel: "sms", Destination: "+201001234567", IP: "10.0.0.5", MAC: "aa:bb:cc:dd:ee:05", PortalBase: "http://192.168.77.1:8380"})
		rec := httptest.NewRecorder()
		s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
		if rec.Code != http.StatusOK || len(cs.msgs) != 1 {
			t.Fatalf("issue: %d %s (sms %d)", rec.Code, rec.Body.String(), len(cs.msgs))
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

// A CODE IS ISSUED TO A DEVICE OR NOT AT ALL (migration 0110): without the kernel-resolved hardware address the
// challenge could not be bound, so the issue is refused -- nothing is generated and nothing is sent.
func TestACodeIsNeverIssuedWithoutADevice(t *testing.T) {
	s, cm := emailFixture(t)
	for _, mac := range []string{"", "not-a-mac"} {
		body, _ := json.Marshal(otpIssueReq{Channel: "email", Destination: "ann@example.com", IP: "10.0.0.5", MAC: mac})
		rec := httptest.NewRecorder()
		s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "device identity required") {
			t.Fatalf("mac %q: %d %s", mac, rec.Code, rec.Body.String())
		}
	}
	if len(cm.msgs) != 0 {
		t.Fatal("nothing may be sent for a code that was not issued")
	}
}

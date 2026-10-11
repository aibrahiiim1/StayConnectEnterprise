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

func TestTheEmailCodeCarriesATapToConnectLinkForTheOriginTheDeviceUsed(t *testing.T) {
	s, cm := emailFixture(t)
	body, _ := json.Marshal(otpIssueReq{Channel: "email", Destination: "ann@example.com", IP: "10.0.0.5", MAC: "aa:bb:cc:dd:ee:05", PortalBase: "http://192.168.77.1:8380"})
	rec := httptest.NewRecorder()
	s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
	if rec.Code != http.StatusOK || len(cm.msgs) != 1 {
		t.Fatalf("issue: %d %s (mails %d)", rec.Code, rec.Body.String(), len(cm.msgs))
	}
	text := cm.msgs[0].Text
	if !strings.Contains(text, "731946") || !strings.Contains(text, "http://192.168.77.1:8380/connect?c=chal-1&k=731946") {
		t.Fatalf("the email must carry the code and the link:\n%s", text)
	}
	// No origin, or one that is not a plain origin: the code alone, never a link built from something odd.
	for _, base := range []string{"", "javascript:alert(1)", "http://evil.example/x?y", "ftp://h", "http://user:pw@h"} {
		cm.msgs = nil
		body, _ = json.Marshal(otpIssueReq{Channel: "email", Destination: "ann@example.com", IP: "10.0.0.5", MAC: "aa:bb:cc:dd:ee:05", PortalBase: base})
		rec = httptest.NewRecorder()
		s.otpIssue(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/issue", bytes.NewReader(body)))
		if rec.Code != http.StatusOK || len(cm.msgs) != 1 {
			t.Fatalf("issue with base %q: %d", base, rec.Code)
		}
		if strings.Contains(cm.msgs[0].Text, "/connect?") || strings.Contains(cm.msgs[0].Text, "javascript") {
			t.Fatalf("base %q must yield no link:\n%s", base, cm.msgs[0].Text)
		}
	}
}

func TestOTPLinkBaseAcceptsOnlyAPlainOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"http://192.168.77.1:8380": "http://192.168.77.1:8380", "https://portal.example": "https://portal.example",
		"http://h/": "", "http://h?x=1": "", "http://h#f": "", "http://u@h": "", "ftp://h": "", "h:8380": "", "": "",
	} {
		if got := otpLinkBase(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
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

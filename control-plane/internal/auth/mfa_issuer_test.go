package auth

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

// A newly enrolled Central authenticator is labelled "OneGate Central"; the issuer is display only.
func TestCentralAuthenticatorEnrolmentIsBrandedOneGateCentral(t *testing.T) {
	secret, err := totp.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	raw := totp.URI(secret, MFAIssuer, "admin@central.test")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if label := strings.TrimPrefix(u.Path, "/"); label != "OneGate Central:admin@central.test" {
		t.Fatalf("label = %q", label)
	}
	if got := u.Query().Get("issuer"); got != "OneGate Central" {
		t.Fatalf("issuer parameter = %q", got)
	}
	if strings.Contains(strings.ToLower(raw), "stayconnect") {
		t.Fatalf("legacy branding in %s", raw)
	}
}

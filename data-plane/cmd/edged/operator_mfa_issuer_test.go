package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/totp"
)

// A newly enrolled authenticator is labelled with the product's name; the issuer is display only, so an
// authenticator enrolled under an earlier name keeps working unchanged.
func TestAuthenticatorEnrolmentIsBrandedOneGateAdminConsole(t *testing.T) {
	if mfaIssuer != "OneGate Admin Console" {
		t.Fatalf("issuer = %q", mfaIssuer)
	}
	secret, err := totp.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	raw := totp.URI(secret, mfaIssuer, "ops@site.test")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "otpauth" || u.Host != "totp" {
		t.Fatalf("not a TOTP URI: %s", raw)
	}
	if label := strings.TrimPrefix(u.Path, "/"); label != "OneGate Admin Console:ops@site.test" {
		t.Fatalf("label = %q", label)
	}
	if got := u.Query().Get("issuer"); got != "OneGate Admin Console" {
		t.Fatalf("issuer parameter = %q", got)
	}
	if strings.Contains(strings.ToLower(raw), "stayconnect") {
		t.Fatalf("legacy branding in %s", raw)
	}
	if u.Query().Get("secret") != totp.EncodeSecret(secret) {
		t.Fatal("the secret in the QR payload is not the enrolled secret")
	}
}

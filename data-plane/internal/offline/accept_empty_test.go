package offline

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

// AD-15: an empty identity value on either side is a refusal, never a skipped comparison.
func TestAcceptForRefusesEmptyIdentityValues(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mk := func(mut func(*Package)) *Package {
		p := &Package{PackageID: "p", ApplianceID: "appl-1", Serial: "SC-1", IdentityKeyFpr: "aaaa",
			ExpiresAt: time.Now().Add(time.Hour).Unix(), Nonce: "n"}
		mut(p)
		Sign(priv, p)
		return p
	}
	now := time.Now()
	if r := AcceptFor(pub, mk(func(*Package) {}), "appl-1", "SC-1", "aaaa", "", now); r != "" {
		t.Fatalf("a matching package is accepted, got %q", r)
	}
	cases := []struct {
		name                       string
		mut                        func(*Package)
		applianceID, serial, idFpr string
	}{
		{"empty local appliance id", func(*Package) {}, "", "SC-1", "aaaa"},
		{"empty package appliance id", func(p *Package) { p.ApplianceID = "" }, "appl-1", "SC-1", "aaaa"},
		{"empty local identity key", func(*Package) {}, "appl-1", "SC-1", ""},
		{"empty package identity key", func(p *Package) { p.IdentityKeyFpr = "" }, "appl-1", "SC-1", "aaaa"},
		{"empty local serial", func(*Package) {}, "appl-1", "", "aaaa"},
	}
	for _, c := range cases {
		if r := AcceptFor(pub, mk(c.mut), c.applianceID, c.serial, c.idFpr, "", now); r == "" {
			t.Errorf("%s must be refused", c.name)
		}
	}
}

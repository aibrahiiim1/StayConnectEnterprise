package activation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func newReq(t *testing.T) (*Request, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	r := &Request{
		SchemaVersion: SchemaVersion, RequestID: "req-1", Serial: "SC-TEST",
		PublicKey: base64.RawStdEncoding.EncodeToString(pub), Nonce: "nonce-1",
		CreatedAt: time.Now().Unix(),
	}
	SignRequest(priv, r)
	return r, priv
}

func newPkg(t *testing.T, vendor ed25519.PrivateKey, r *Request) *Package {
	t.Helper()
	pubRaw, _ := base64.RawStdEncoding.DecodeString(r.PublicKey)
	p := &Package{
		SchemaVersion: SchemaVersion, PackageID: "pkg-1", RequestID: r.RequestID, RequestNonce: r.Nonce,
		ApplianceID: "appliance-1", Serial: r.Serial,
		IdentityKeyFpr: KeyID(ed25519.PublicKey(pubRaw)),
		TenantID:       "tenant-1", SiteID: "site-1",
		Assignment: json.RawMessage(`{"tenant_id":"tenant-1"}`),
		ExpiresAt:  time.Now().Add(time.Hour).Unix(),
		Nonce:      "pkg-nonce",
	}
	SignPackage(vendor, p)
	return p
}

// A request is evidence only because it proves possession of the key it names. Without that check, anyone
// could submit another appliance's hardware facts with their own public key and be handed a package bound to
// a key they control.
func TestRequestMustProvePossession(t *testing.T) {
	r, _ := newReq(t)
	if !VerifyRequest(r) {
		t.Fatal("a correctly self-signed request must verify")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	swapped := *r
	swapped.PublicKey = base64.RawStdEncoding.EncodeToString(other)
	if VerifyRequest(&swapped) {
		t.Fatal("a request whose public key was swapped must NOT verify")
	}
	tampered := *r
	tampered.Serial = "SC-SOMEONE-ELSE"
	if VerifyRequest(&tampered) {
		t.Fatal("a request with an edited serial must NOT verify")
	}
	old := *r
	old.SchemaVersion = SchemaVersion + 1
	if VerifyRequest(&old) {
		t.Fatal("an unknown schema version must NOT verify")
	}
}

// Every way a package could be pointed at the wrong appliance, replayed, downgraded or edited.
func TestPackageAcceptanceFailsClosed(t *testing.T) {
	vendorPub, vendorPriv, _ := ed25519.GenerateKey(rand.Reader)
	r, _ := newReq(t)
	p := newPkg(t, vendorPriv, r)
	pubRaw, _ := base64.RawStdEncoding.DecodeString(r.PublicKey)
	fpr := KeyID(ed25519.PublicKey(pubRaw))
	now := time.Now()

	if reason := AcceptForFirstActivation(vendorPub, p, r.Serial, fpr, r.RequestID, r.Nonce, false, now); reason != "" {
		t.Fatalf("a good package must be accepted, got %q", reason)
	}

	cases := []struct {
		name                      string
		mutate                    func(*Package)
		serial, fpr, reqID, nonce string
		assigned                  bool
	}{
		{name: "wrong appliance identity key", serial: r.Serial, fpr: "someone-elses-key", reqID: r.RequestID, nonce: r.Nonce},
		{name: "appliance holds no key at all", serial: r.Serial, fpr: "", reqID: r.RequestID, nonce: r.Nonce},
		{name: "answers a different request", serial: r.Serial, fpr: fpr, reqID: "req-2", nonce: r.Nonce},
		{name: "stale or replayed nonce", serial: r.Serial, fpr: fpr, reqID: r.RequestID, nonce: "old-nonce"},
		{name: "serial mismatch", serial: "SC-OTHER", fpr: fpr, reqID: r.RequestID, nonce: r.Nonce},
		{name: "already assigned appliance", serial: r.Serial, fpr: fpr, reqID: r.RequestID, nonce: r.Nonce, assigned: true},
		{name: "expired", mutate: func(p *Package) { p.ExpiresAt = time.Now().Add(-time.Minute).Unix() },
			serial: r.Serial, fpr: fpr, reqID: r.RequestID, nonce: r.Nonce},
		{name: "tampered after signing", mutate: func(p *Package) { p.TenantID = "tenant-attacker" },
			serial: r.Serial, fpr: fpr, reqID: r.RequestID, nonce: r.Nonce},
		{name: "carries no signed assignment", mutate: func(p *Package) { p.Assignment = nil; SignPackage(vendorPriv, p) },
			serial: r.Serial, fpr: fpr, reqID: r.RequestID, nonce: r.Nonce},
		// SignPackage stamps the CURRENT version, so a re-signed package can never carry a foreign one.
		// Bumping it after signing is therefore the only way it can arrive wrong, and that is both a version
		// mismatch and a tamper -- refused either way.
		{name: "version bumped after signing", mutate: func(p *Package) { p.SchemaVersion = 99 },
			serial: r.Serial, fpr: fpr, reqID: r.RequestID, nonce: r.Nonce},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := newPkg(t, vendorPriv, r)
			if c.mutate != nil {
				c.mutate(q)
			}
			if reason := AcceptForFirstActivation(vendorPub, q, c.serial, c.fpr, c.reqID, c.nonce,
				c.assigned, now); reason == "" {
				t.Fatalf("%s must be refused, but it was accepted", c.name)
			}
		})
	}

	// A package signed by anything other than the vendor key is not a package.
	_, impostor, _ := ed25519.GenerateKey(rand.Reader)
	q := newPkg(t, impostor, r)
	if reason := AcceptForFirstActivation(vendorPub, q, r.Serial, fpr, r.RequestID, r.Nonce, false, now); reason == "" {
		t.Fatal("a package signed by the wrong key must be refused")
	}
}

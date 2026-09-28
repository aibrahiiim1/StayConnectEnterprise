package assignment

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// goldenDoc and goldenBytes are duplicated byte-for-byte in the
// control-plane and data-plane twins. If either twin's signing layout drifts,
// its copy of this test fails.
var goldenDoc = Document{
	AssignmentID: "a1", ApplianceID: "b2", IdentityKeyFpr: "c3", Serial: "d4",
	TenantID: "e5", SiteID: "f6", TenantName: "T", SiteName: "S",
	Version: 7, State: "assigned", IssuedAt: 100, ExpiresAt: 0, SignerKeyID: "k",
}

const goldenBytes = `{"assignment_id":"a1","appliance_id":"b2","identity_key_fingerprint":"c3","serial":"d4","tenant_id":"e5","site_id":"f6","tenant_name":"T","site_name":"S","version":7,"state":"assigned","issued_at":100,"expires_at":0,"signer_key_id":"k"}`

const goldenBytesWithType = `{"assignment_id":"a1","appliance_id":"b2","identity_key_fingerprint":"c3","serial":"d4","tenant_id":"e5","site_id":"f6","tenant_name":"T","site_name":"S","version":7,"state":"assigned","issued_at":100,"expires_at":0,"signer_key_id":"k","site_type":"CAFE"}`

func TestSigningBytesGoldenWithoutSiteType(t *testing.T) {
	d := goldenDoc
	if got := string(signingBytes(&d)); got != goldenBytes {
		t.Fatalf("signing bytes drifted:\n got %s\nwant %s", got, goldenBytes)
	}
}

func TestSigningBytesGoldenWithSiteType(t *testing.T) {
	d := goldenDoc
	d.SiteType = "CAFE"
	if got := string(signingBytes(&d)); got != goldenBytesWithType {
		t.Fatalf("signing bytes drifted:\n got %s\nwant %s", got, goldenBytesWithType)
	}
}

func TestUnknownFutureSiteTypeStillVerifies(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := goldenDoc
	d.SiteType = "SPACEPORT"
	Sign(priv, &d)
	if !Verify(pub, &d) {
		t.Fatal("a future site type must not break verification")
	}
}

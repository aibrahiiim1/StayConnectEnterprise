package updates

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestAcceptMatrix(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pkg := []byte("test package payload v2")
	m := &Manifest{UpdateID: "u1", Component: "test-component", Version: "2.0.0",
		MinSourceVersion: "1.0.0", Model: "sc-appliance", Channel: "stable",
		SHA256: SHA256Hex(pkg), Size: int64(len(pkg))}
	Sign(priv, m)

	if r := Accept(pub, m, pkg, "1.5.0", "sc-appliance"); r != "" {
		t.Fatalf("valid update rejected: %s", r)
	}
	// invalid signature (tamper)
	m2 := *m
	m2.Version = "9.9.9"
	if Accept(pub, &m2, pkg, "1.5.0", "sc-appliance") == "" {
		t.Fatal("tampered manifest accepted")
	}
	// invalid checksum
	if Accept(pub, m, []byte("different bytes"), "1.5.0", "sc-appliance") == "" {
		t.Fatal("bad checksum accepted")
	}
	// incompatible model
	if Accept(pub, m, pkg, "1.5.0", "other-model") == "" {
		t.Fatal("incompatible model accepted")
	}
	// source below minimum
	if Accept(pub, m, pkg, "0.9.0", "sc-appliance") == "" {
		t.Fatal("below-min source accepted")
	}
	// wrong signer
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)
	m3 := &Manifest{UpdateID: "u1", Component: "test-component", Version: "2.0.0", SHA256: SHA256Hex(pkg)}
	Sign(priv2, m3)
	if Accept(pub, m3, pkg, "1.5.0", "") == "" {
		t.Fatal("wrong-signer manifest accepted")
	}
}

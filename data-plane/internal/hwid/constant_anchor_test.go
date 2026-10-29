package hwid

import "testing"

// The literals below are copied into control-plane/internal/api (constantAnchorSerial, constantAnchorFingerprint),
// which refuses new registrations that carry them (AD-19). Change both together.
func TestConstantAnchorLiteralsMatchCentral(t *testing.T) {
	t.Logf("serial=%s fingerprint=%s", serialFor(ConstantAnchor), fingerprintFor(nil))
	if got := serialFor(ConstantAnchor); got != "SC-KC9P-ZWSS-DPYJ" {
		t.Fatalf("constant-anchor serial = %s", got)
	}
	if got := fingerprintFor(nil); got != "b3a440cce92f0dfd177b46ef874824a5" {
		t.Fatalf("empty-parts fingerprint = %s", got)
	}
}

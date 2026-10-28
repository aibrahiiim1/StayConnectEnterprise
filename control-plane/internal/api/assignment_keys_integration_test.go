package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
)

// Audit Phase 1 M3: a key-state change that cannot be re-signed into the registry is refused, not reported as done.
func TestKeyStateChangeNeedsTheRegistryRoot(t *testing.T) {
	f := newFixture(t)
	keyID := assignment.KeyID(f.base.AssignKey.Public().(ed25519.PublicKey))

	if err := KeyToVerifyOnly(f.ctx, f.base, nil, keyID, "rotate"); err == nil {
		t.Fatal("verify-only without the registry root must fail")
	}
	if _, err := KeyRevoke(f.ctx, f.base, nil, keyID, "compromise", true); err == nil {
		t.Fatal("revoke without the registry root must fail")
	}
	if n := f.count(`SELECT count(*) FROM assignment_signing_keys WHERE key_id=$1 AND state='active'`, keyID); n != 1 {
		t.Fatal("a refused change must leave the key state as it was")
	}

	_, root, _ := ed25519.GenerateKey(rand.Reader)
	before := f.count(`SELECT count(*) FROM assignment_registry`)
	if _, err := KeyRevoke(f.ctx, f.base, root, keyID, "compromise", true); err != nil {
		t.Fatalf("revoke with the registry root: %v", err)
	}
	if after := f.count(`SELECT count(*) FROM assignment_registry`); after != before+1 {
		t.Fatalf("revoke must publish a re-signed registry (%d -> %d)", before, after)
	}
	if n := f.count(`SELECT count(*) FROM assignment_registry WHERE is_current AND signed_envelope::text LIKE '%revoked%'`); n != 1 {
		t.Fatal("the current registry must carry the revoked state")
	}
}

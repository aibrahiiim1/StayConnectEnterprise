package assignment

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

// The signed assignment is durable CONFIGURATION, not an auth token. A hotel must
// keep serving guests through a Central outage, so passing expires_at must never
// de-authorise the appliance — it only marks the assignment stale.
func TestExpiryDoesNotUnassign(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()

	reg := &Registry{}
	reg.AddOrRotate(trusted(pub, KeyActive))

	d := mkDoc()
	d.ExpiresAt = now.Add(-24 * time.Hour).Unix() // long expired
	Sign(priv, d)

	if !IsExpired(d, now) {
		t.Fatal("document should be expired")
	}
	// An expired document is still ACCEPTED (signature/binding/version all valid).
	if r := AcceptForRegistry(reg, d, "app-1", "SN-1", "fpr-1", 0, now); r != "" {
		t.Fatalf("expired assignment was REJECTED (%s) — this would strand a hotel on a Central outage", r)
	}
	// And it still confers ownership.
	if !Grants(d.State) {
		t.Fatal("expired assigned document must still grant tenant/site")
	}
}

// Ownership may only be removed by an explicit, newer, signed document.
func TestOwnershipOnlyChangesExplicitly(t *testing.T) {
	if !Grants(StateAssigned) || !Grants(StateReassigned) {
		t.Fatal("assigned/reassigned must grant ownership")
	}
	for _, s := range []string{StateUnassigned, StateRevoked, StateDecommissioned} {
		if Grants(s) {
			t.Fatalf("%s must not grant ownership", s)
		}
		if !Clears(s) {
			t.Fatalf("%s must explicitly clear ownership", s)
		}
	}
}

// Store: adoption keeps the previous document, expiry marks stale but retains
// tenant/site, and a failed refresh never downgrades the assignment.
func TestStoreOfflineSemantics(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_ = pub

	v1 := mkDoc()
	v1.Version = 1
	v1.ExpiresAt = time.Now().Add(-time.Hour).Unix() // already expired
	Sign(priv, v1)
	if err := s.Adopt(v1); err != nil {
		t.Fatal(err)
	}

	// Expired -> stale, but tenant/site RETAINED.
	if got := s.Status(); got != StatusStale {
		t.Fatalf("want stale, got %s", got)
	}
	ten, site, state, ver := s.Resolved()
	if ten == "" || site == "" || state != StateAssigned || ver != 1 {
		t.Fatalf("expired assignment lost ownership: tenant=%q site=%q state=%q v=%d", ten, site, state, ver)
	}

	// A FAILED refresh must not clear anything; it records staleness only.
	s.NoteRefresh(false)
	ten2, site2, _, _ := s.Resolved()
	if ten2 != ten || site2 != site {
		t.Fatal("a failed refresh changed ownership — an unreachable Central must never unassign")
	}
	r, _ := s.Load()
	if r.StaleSince == "" || r.LastRefreshAttempt == "" {
		t.Fatal("stale_since / last_refresh_attempt not recorded")
	}

	// Adopting v2 keeps v1 as previous (rollback-safe) and clears staleness.
	v2 := mkDoc()
	v2.Version = 2
	v2.ExpiresAt = 0
	Sign(priv, v2)
	if err := s.Adopt(v2); err != nil {
		t.Fatal(err)
	}
	r2, _ := s.Load()
	if r2.Previous == nil || r2.Previous.Version != 1 {
		t.Fatal("previous assignment not retained")
	}
	if r2.Version != 2 || r2.StaleSince != "" || s.Status() != StatusCurrent {
		t.Fatalf("v2 not current: v=%d stale=%q status=%s", r2.Version, r2.StaleSince, s.Status())
	}

	// An explicit signed unassign DOES clear ownership.
	v3 := mkDoc()
	v3.Version = 3
	v3.State = StateUnassigned
	v3.TenantID, v3.SiteID = "", ""
	Sign(priv, v3)
	if err := s.Adopt(v3); err != nil {
		t.Fatal(err)
	}
	ten3, site3, state3, _ := s.Resolved()
	if ten3 != "" || site3 != "" || state3 != StateUnassigned {
		t.Fatal("explicit unassign did not clear ownership")
	}
}

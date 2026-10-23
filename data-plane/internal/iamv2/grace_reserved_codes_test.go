package iamv2

import "testing"

// Every reserved code is refused in Go (a DB-trigger-only refusal surfaced as a 500), no ordinary code is,
// and the set must not silently shrink.
func TestEveryReservedCatalogueCodeIsRefusedUniformly(t *testing.T) {
	reserved := []string{
		"__system_checkout_grace",      // D32 system grace package
		"__system_checkout_grace_plan", // the plan revision it pins
		"__sys_emergency_grace_pkg__",  // Emergency fail-safe catalogue, DB-trigger protected
		"__sys_emergency_grace_plan__",
	}
	for _, code := range reserved {
		if !reservedCommerceCode(code) {
			t.Errorf("reserved code %q is not refused: an operator publish would land on a system object", code)
		}
	}
	// The constants and the literals must agree.
	if !reservedCommerceCode(systemGraceCode) || !reservedCommerceCode(systemGracePlanCode) {
		t.Fatal("the D32 code constants are not in the reserved set")
	}
	for _, ok := range []string{"free-1h", "premium", "__almost_reserved", "system_checkout_grace", ""} {
		if reservedCommerceCode(ok) {
			t.Errorf("ordinary code %q was refused: the guard is over-broad and blocks legitimate catalogue work", ok)
		}
	}
}

package iamv2

import "testing"

// The reserved-code refusal must be UNIFORM across all four codes.
//
// It was not. The two D32 codes were refused in Go with a clear message; the two EMERGENCY codes were refused
// only by a database trigger, whose plain RAISE was collapsed by the transaction wrapper into a repository
// error and surfaced as HTTP 500 "publish failed". Both are refusals, but only one of them said so -- and a
// refusal that presents as an internal error is the shape an operator escalates as an outage.
//
// This test pins the property rather than the message: every reserved code is refused, no ordinary code is,
// and the set does not silently shrink if someone edits the switch.
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
	// The constants and the literals must agree; a rename of either that missed the other would leave the
	// object reachable while every test above still passed.
	if !reservedCommerceCode(systemGraceCode) || !reservedCommerceCode(systemGracePlanCode) {
		t.Fatal("the D32 code constants are not in the reserved set")
	}
	for _, ok := range []string{"free-1h", "premium", "__almost_reserved", "system_checkout_grace", ""} {
		if reservedCommerceCode(ok) {
			t.Errorf("ordinary code %q was refused: the guard is over-broad and blocks legitimate catalogue work", ok)
		}
	}
}

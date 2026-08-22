package iamv2

import "testing"

// The Service Plans screen must not present a system row as an operator plan.
//
// service_plans has no is_system column, so the listing filters by code — and it previously hardcoded two of
// the four reserved codes while the publish guard refused all four. The half that was wrong was the visible
// one: __sys_emergency_grace_plan__, created by migration 0010's bootstrap, appeared on the operator screen
// as though it were theirs, while publishing onto it was refused. Asserted against the guard so the listing
// and the guard cannot drift apart again.
func TestReservedCommerceCodes_CoverBothProvisioningSchemes(t *testing.T) {
	for _, code := range []string{
		"__system_checkout_grace", "__system_checkout_grace_plan", // Go-provisioned
		"__sys_emergency_grace_pkg__", "__sys_emergency_grace_plan__", // migration 0010 bootstrap
	} {
		if !reservedCommerceCode(code) {
			t.Fatalf("%q is a system row but is not reserved: it would be offered as an operator plan", code)
		}
	}
	if len(reservedCommerceCodes) != 4 {
		t.Fatalf("expected 4 reserved codes, got %d — the listing filter reads this list", len(reservedCommerceCodes))
	}
	if reservedCommerceCode("GOLD") {
		t.Fatal("an ordinary operator code must not be treated as reserved")
	}
}

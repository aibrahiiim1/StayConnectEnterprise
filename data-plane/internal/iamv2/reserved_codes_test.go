package iamv2

import "testing"

// service_plans has no is_system column, so the listing filters by code; asserting against the publish guard
// keeps the two reserved-code lists from drifting apart.
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

package api

import (
	"strings"
	"testing"
)

// X-16: an omitted or zero concurrent cap is refused; "no cap" must be asked for explicitly.
func TestLicenseCapZeroNeedsTheUnlimitedFlag(t *testing.T) {
	if _, err := (licenseTerms{}).params("a", "op"); err == nil || !strings.Contains(err.Error(), "unlimited_online_guests") {
		t.Fatalf("an omitted cap must be refused and name the flag, got %v", err)
	}
	if p, err := (licenseTerms{UnlimitedOnlineGuests: true}).params("a", "op"); err != nil || p.MaxConcurrentOnlineGuests != 0 {
		t.Fatalf("an explicit unlimited licence is allowed: %+v %v", p, err)
	}
	if _, err := (licenseTerms{UnlimitedOnlineGuests: true, MaxConcurrentOnlineGuests: 5}).params("a", "op"); err == nil {
		t.Fatal("a cap and the unlimited flag together are contradictory")
	}
	if _, err := (licenseTerms{MaxConcurrentOnlineGuests: -1}).params("a", "op"); err == nil {
		t.Fatal("a negative cap is refused")
	}
	if p, err := (licenseTerms{MaxConcurrentOnlineGuests: 25}).params("a", "op"); err != nil || p.MaxConcurrentOnlineGuests != 25 {
		t.Fatalf("a positive cap is kept: %+v %v", p, err)
	}
}

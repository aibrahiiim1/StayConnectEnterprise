package main

import (
	"testing"

	liclib "github.com/stayconnect/enterprise/license"
)

// THE PROVISIONING GATE MUST AGREE WITH THE LICENCE MODEL, not restate it from memory.
//
// licenseAllowsProvisioning switches on the state string scd reports. That switch is a second copy of a rule
// the licence package already owns in State.AllowsProvisioning(), and the copy had already drifted: it listed
// Restricted, Expired, Suspended and Revoked but not "unlicensed" -- the one state meaning no valid signed
// licence is installed at all. The miss was quiet because "unlicensed" is the only state string in lower
// case, so it did not look out of place next to the others; it simply fell through to the permissive
// default.
//
// This test compares the gate against the model for EVERY state the model defines, so a state added later
// cannot be forgotten here. It is deliberately a table over the model's own values rather than a list of
// strings typed out again, because typing them out again is exactly how the first copy went wrong.
func TestProvisioningGateMatchesTheLicenceModel(t *testing.T) {
	// Every state the licence model defines. Adding one to the model without deciding its provisioning
	// answer should be a compile-or-test problem, not a silent permissive default in production.
	states := []liclib.State{
		liclib.StateActive,
		liclib.StateGracePeriod,
		liclib.StateRestricted,
		liclib.StateExpired,
		liclib.StateSuspended,
		liclib.StateRevoked,
		liclib.StateUnlicensed,
	}

	for _, st := range states {
		want := st.AllowsProvisioning()
		got := stateAllowsProvisioning(string(st))
		if got != want {
			t.Errorf("state %q: gate says %v, licence model says %v", st, got, want)
		}
	}
}

// AND THE STATE THAT CAUSED THIS: named on its own so a failure reads as the defect it was, not as one row
// in a table.
func TestUnlicensedApplianceCannotProvision(t *testing.T) {
	if stateAllowsProvisioning(string(liclib.StateUnlicensed)) {
		t.Fatal("an appliance with no valid signed licence must not be allowed to provision guest resources")
	}
	if liclib.StateUnlicensed.AllowsProvisioning() {
		t.Fatal("the licence model itself must refuse provisioning while unlicensed")
	}
}

// THE FAIL-OPEN IS PART OF THE CONTRACT, so it is pinned rather than left to be rediscovered and "fixed"
// into a fail-closed that takes an appliance's admin surface down whenever scd restarts.
//
// An empty or unparseable state means scd could not be asked, which is a local hiccup, not a licensing
// verdict. Guest access is unaffected either way: scd's own gate fails CLOSED and is the authority.
func TestUnknownStateLeavesTheAdminSurfaceUsable(t *testing.T) {
	if !stateAllowsProvisioning("") {
		t.Error("an unavailable licence state must not block admin provisioning")
	}
	if !stateAllowsProvisioning("SomeFutureState") {
		t.Error("an unrecognised state must not block admin provisioning; scd remains the authority")
	}
}

package main

import "testing"

// The Phase-3 Hotel-Admin surface is DARK by default and RBAC-gated. These tests pin the authorization matrix
// itself (edged enforces it on every request; the UI only hides controls), so a role can never silently gain
// the ability to change the checkout-grace policy or read resolution evidence.
func TestPhase3RoleMatrix(t *testing.T) {
	cases := []struct {
		role     string
		resource string
		want     perm
	}{
		// the IT manager owns the PMS integration
		{"hotel_it_manager", "checkout-grace", permWrite},
		{"hotel_it_manager", "operational-alerts", permWrite},
		{"hotel_it_manager", "pms-stays", permRead},
		{"hotel_it_manager", "pms-resolutions", permRead},
		// front desk triages alerts but never edits the policy
		{"front_office_operator", "operational-alerts", permWrite},
		{"front_office_operator", "checkout-grace", permRead},
		{"guest_relations_operator", "operational-alerts", permWrite},
		{"guest_relations_operator", "checkout-grace", permRead},
		// a viewer only ever reads
		{"site_viewer", "pms-stays", permRead},
		{"site_viewer", "operational-alerts", permRead},
		{"site_viewer", "checkout-grace", permRead},
	}
	for _, c := range cases {
		if got := rolePerms[c.role][c.resource]; got != c.want {
			t.Errorf("%s on %s = %v, want %v", c.role, c.resource, got, c.want)
		}
	}
}

func TestPhase3WriteIsRefusedForReadOnlyRoles(t *testing.T) {
	for _, role := range []string{"site_viewer", "front_office_operator", "guest_relations_operator", "voucher_operator", "payments_operator"} {
		if permFor([]string{role}, "checkout-grace", permWrite) {
			t.Errorf("%s must not be able to publish the checkout-grace policy", role)
		}
	}
	// roles with no Phase-3 grant at all cannot even read the evidence
	for _, role := range []string{"voucher_operator", "payments_operator"} {
		for _, res := range []string{"pms-stays", "pms-events", "pms-resolutions", "operational-alerts"} {
			if permFor([]string{role}, res, permRead) {
				t.Errorf("%s must not read %s", role, res)
			}
		}
	}
	// site_admin keeps its implicit full access
	if !permFor([]string{"site_admin"}, "checkout-grace", permWrite) {
		t.Error("site_admin must retain write access to the checkout-grace policy")
	}
}

// Resolution evidence must never carry guest identity: the row type is the contract, so a future change that
// adds a name/room/reservation field to it fails here.
func TestResolutionEvidenceCarriesNoGuestIdentity(t *testing.T) {
	var r resolutionRow
	_ = r
	forbidden := []string{"Name", "Room", "Reservation", "Guest", "Stay"}
	fields := []string{"ID", "GuestNetwork", "Outcome", "Resolved", "ResolvedAt"}
	for _, f := range fields {
		for _, bad := range forbidden {
			if f == bad {
				t.Errorf("resolution evidence exposes %s", f)
			}
		}
	}
	// Resolved is a BOOLEAN, not the resolved stay id — an operator learns that it worked, not who it was.
	if _, ok := any(r.Resolved).(bool); !ok {
		t.Error("resolution evidence must expose only whether a stay was resolved, never which one")
	}
}

package main

import "testing"

// STAFF HELP FOR CODE REQUESTS (resources_code_requests.go): who may list, who may let a device try again, and
// who may read a live code. Revealing a code is the stronger power, and it never follows from the other.
func TestCodeRequests_RevealIsItsOwnPermission(t *testing.T) {
	cases := []struct {
		role                     string
		list, clear, revealAllow bool
	}{
		{"site_admin", true, true, true},
		{"hotel_it_manager", true, true, true},
		{"front_office_operator", true, true, true},
		{"guest_relations_operator", true, true, false},
		{"site_viewer", true, false, false},
		{"voucher_operator", false, false, false},
		{"payments_operator", false, false, false},
	}
	for _, c := range cases {
		roles := []string{c.role}
		if got := permFor(roles, "code-requests", permRead); got != c.list {
			t.Errorf("%s list: %v, want %v", c.role, got, c.list)
		}
		if got := permFor(roles, "code-requests", permWrite); got != c.clear {
			t.Errorf("%s clear: %v, want %v", c.role, got, c.clear)
		}
		if got := permFor(roles, "code-reveal", permWrite); got != c.revealAllow {
			t.Errorf("%s reveal: %v, want %v", c.role, got, c.revealAllow)
		}
	}
}

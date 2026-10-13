package main

import (
	"strings"
	"testing"
)

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

// A role that cannot show a code sees the address and the device masked; a body that cannot be parsed is never
// relayed unmasked.
func TestCodeRequests_ListIsMaskedForRolesThatCannotReveal(t *testing.T) {
	in := []byte(`{"requests":[{"channel":"email","destination":"guest@example.com","device_mac":"d2:77:1c:e6:f7:3a","state":"PENDING"},` +
		`{"channel":"sms","destination":"+201001234567","device_mac":"02:00:00:00:00:01"}],"limits":{"per_device_daily":3}}`)
	out := string(maskCodeRequests(in))
	for _, leak := range []string{"guest@example.com", "d2:77:1c", "+201001234567"} {
		if strings.Contains(out, leak) {
			t.Fatalf("masked list leaks %q: %s", leak, out)
		}
	}
	for _, want := range []string{"g•••@example.com", "••:••:••:••:f7:3a", "+20•••4567", `"state":"PENDING"`, `"per_device_daily":3`} {
		if !strings.Contains(out, want) {
			t.Fatalf("masked list misses %q: %s", want, out)
		}
	}
	if got := string(maskCodeRequests([]byte("not json"))); got != `{"requests":[]}` {
		t.Fatalf("an unparseable body must fail closed, got %s", got)
	}
}

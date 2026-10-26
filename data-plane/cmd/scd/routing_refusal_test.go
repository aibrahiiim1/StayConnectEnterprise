package main

import "testing"

const (
	policyNone      = "NONE"
	policyDedicated = "DEDICATED"
	policyShared    = "SHARED_STRICT"
)

// Routing refusals are decided before any interface is consulted.
func TestRoutingRefusalIsDecidedFromPolicyAndMappingAlone(t *testing.T) {
	for _, c := range []struct {
		policy string
		mapped int
		refuse bool
	}{
		{"", 0, true}, {"", 1, false}, {"", 2, true},
		{policyNone, 0, true}, {policyNone, 1, true},
		{policyDedicated, 1, false}, {policyDedicated, 0, true}, {policyDedicated, 2, true},
		{policyShared, 2, false}, {policyShared, 3, false}, {policyShared, 1, true}, {policyShared, 4, true},
		{"EVERYTHING", 2, true},
	} {
		if got := routingRefusal(c.policy, c.mapped) != ""; got != c.refuse {
			t.Errorf("policy %q with %d mapped: refused=%v, want %v", c.policy, c.mapped, got, c.refuse)
		}
	}
}

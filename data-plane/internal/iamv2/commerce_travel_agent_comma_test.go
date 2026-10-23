package iamv2

import "testing"

// A comma inside a travel-agent name is part of the name ("Sun Tours, Ltd"): it matches that agent (case and
// surrounding spaces aside) and not "Sun Tours" or "Ltd" alone.
func TestTravelAgentRuleKeepsACommaInsideOneName(t *testing.T) {
	rule := []EligibilityRule{{RuleTravelAgent, map[string]any{"travel_agents": []any{"Sun Tours, Ltd"}}}}
	for agent, want := range map[string]bool{
		"Sun Tours, Ltd":   true,
		"SUN TOURS, LTD ":  true,
		"Sun Tours":        false,
		"Ltd":              false,
		"Sun Tours,Ltd":    false,
		"Blue Sea, Travel": false,
	} {
		e := stayBase()
		e.TravelAgent = agent
		if ok, reason := EvaluatePackageEligible(rule, subjectWith(e)); ok != want {
			t.Errorf("agent %q: eligible = %v (want %v), reason %q", agent, ok, want, reason)
		}
	}
	if err := ValidateEligibilityRule(rule[0]); err != nil {
		t.Fatalf("a comma inside a travel-agent name must be a valid rule: %v", err)
	}
}

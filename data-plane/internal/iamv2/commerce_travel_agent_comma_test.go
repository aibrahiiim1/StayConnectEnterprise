package iamv2

import "testing"

// A TRAVEL-AGENT NAME IS ONE NAME, COMMAS AND ALL.
//
// The PMS names agents as the hotel wrote them, and "Sun Tours, Ltd" is a real shape. The rule has always stored
// a JSON list, so a comma inside one entry is part of that name: it must match that agent (case and surrounding
// spaces aside) and must not match an agent called only "Sun Tours" or "Ltd".
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

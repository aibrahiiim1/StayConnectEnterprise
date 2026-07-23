package iamv2

import (
	"regexp"
	"strings"
	"time"
)

// Publication-time validation of typed eligibility rules and grant tiers. Hotel Admin MUST call these
// before publishing an immutable package revision; runtime evaluation additionally fails closed. Every
// malformed/unknown-field/ambiguous rule is rejected here so bad config can never be published.

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var (
	validAuthMethods  = map[string]bool{"VOUCHER": true, "ACCOUNT": true, "OTP": true, "SOCIAL": true}
	validSubjectKinds = map[string]bool{"VOUCHER": true, "ACCOUNT": true, "PRINCIPAL": true}
	ruleAllowedFields = map[string]map[string]bool{
		RuleDateWindow:    {"from": true, "until": true},
		RuleAuthMethod:    {"methods": true},
		RuleSubjectKind:   {"kinds": true},
		RulePriorPurchase: {"requires_prior": true, "forbids_prior": true},
		RuleSiteNetwork:   {"guest_network_ids": true},
		// The PMS rule types. Phase 2 refused to publish these because nothing could evaluate them; Phase 3
		// supplies authoritative Stay evidence, so they are publishable — and are validated as strictly as
		// the others, because a property that writes a rule expects it to mean what it says.
		RuleStayStatus:   {"statuses": true},
		RuleStayLength:   {"min_nights": true, "max_nights": true},
		RuleRoomType:     {"room_types": true},
		RuleVIP:          {"is_vip": true},
		RuleTravelAgent:  {"travel_agents": true},
		RulePMSInterface: {"pms_interface_ids": true},
		RuleRatePlan:     {"rate_plans": true},
	}
)

// validatePMSRule checks the shape of a PMS rule at PUBLICATION time. Catching a malformed rule here is the
// difference between an operator seeing an error while they are looking at the form and a guest silently
// failing to qualify months later for a reason nobody can reconstruct.
func validatePMSRule(t string, v map[string]any) error {
	bad := func(m string) error { return &Error{Code: ErrInvalidInput, Msg: m} }
	switch t {
	case RuleStayStatus:
		return requireNonEmptyStrings(v, "statuses", bad)
	case RuleRoomType:
		return requireNonEmptyStrings(v, "room_types", bad)
	case RuleTravelAgent:
		return requireNonEmptyStrings(v, "travel_agents", bad)
	case RuleRatePlan:
		return requireNonEmptyStrings(v, "rate_plans", bad)
	case RulePMSInterface:
		return requireNonEmptyStrings(v, "pms_interface_ids", bad)
	case RuleVIP:
		if _, ok := v["is_vip"].(bool); !ok {
			return bad("VIP rule requires is_vip as a boolean")
		}
		return nil
	case RuleStayLength:
		minN, minPresent, minOK := parseIntField(v, "min_nights")
		maxN, maxPresent, maxOK := parseIntField(v, "max_nights")
		if !minPresent && !maxPresent {
			return bad("STAY_LENGTH requires min_nights or max_nights")
		}
		if (minPresent && !minOK) || (maxPresent && !maxOK) {
			return bad("STAY_LENGTH bounds must be non-negative whole numbers")
		}
		if minPresent && maxPresent && minN > maxN {
			return bad("STAY_LENGTH min_nights exceeds max_nights")
		}
		return nil
	}
	return nil
}

func requireNonEmptyStrings(v map[string]any, key string, bad func(string) error) error {
	if len(stringSet(v[key])) == 0 {
		return bad(key + " must be a non-empty list of strings")
	}
	return nil
}

func noUnknownFields(v map[string]any, allowed map[string]bool) error {
	for k := range v {
		if !allowed[k] {
			return &Error{Code: ErrInvalidInput, Msg: "unknown field: " + k}
		}
	}
	return nil
}

// normalizedEnumList validates a JSON string array against an allowed enum set, upper-cased and
// deduplicated. Empty list or any unknown/blank value is rejected.
func normalizedEnumList(a any, allowed map[string]bool) ([]string, error) {
	arr, ok := a.([]any)
	if !ok || len(arr) == 0 {
		return nil, &Error{Code: ErrInvalidInput, Msg: "expected a non-empty array"}
	}
	seen := map[string]bool{}
	var out []string
	for _, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, &Error{Code: ErrInvalidInput, Msg: "array values must be strings"}
		}
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" || !allowed[s] {
			return nil, &Error{Code: ErrInvalidInput, Msg: "unknown enum value: " + s}
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// normalizedUUIDList validates a JSON array of normalized UUIDs (lower-cased, deduplicated).
func normalizedUUIDList(a any) ([]string, error) {
	arr, ok := a.([]any)
	if !ok || len(arr) == 0 {
		return nil, &Error{Code: ErrInvalidInput, Msg: "expected a non-empty array"}
	}
	seen := map[string]bool{}
	var out []string
	for _, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, &Error{Code: ErrInvalidInput, Msg: "array values must be strings"}
		}
		s = strings.ToLower(strings.TrimSpace(s))
		if !uuidRe.MatchString(s) {
			return nil, &Error{Code: ErrInvalidInput, Msg: "invalid uuid"}
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// ValidateEligibilityRule strictly validates one typed rule for publication. Unknown rule type,
// capability-disabled PMS type, unknown fields, malformed values or ambiguous structure are rejected.
func ValidateEligibilityRule(r EligibilityRule) error {
	t := strings.ToUpper(strings.TrimSpace(r.Type))
	allowed, known := ruleAllowedFields[t]
	if !known {
		return &Error{Code: ErrInvalidInput, Msg: "unknown rule type " + t}
	}
	if r.Value == nil {
		return &Error{Code: ErrInvalidInput, Msg: "rule value required"}
	}
	if IsPMSRuleType(t) {
		if err := noUnknownFields(r.Value, allowed); err != nil {
			return err
		}
		return validatePMSRule(t, r.Value)
	}
	if err := noUnknownFields(r.Value, allowed); err != nil {
		return err
	}
	switch t {
	case RuleDateWindow:
		from, fromP, fromOK := parseTimeField(r.Value, "from")
		until, untilP, untilOK := parseTimeField(r.Value, "until")
		if !fromP && !untilP {
			return &Error{Code: ErrInvalidInput, Msg: "DATE_WINDOW needs at least one of from/until"}
		}
		if (fromP && !fromOK) || (untilP && !untilOK) {
			return &Error{Code: ErrInvalidInput, Msg: "DATE_WINDOW has a malformed timestamp"}
		}
		if fromP && untilP && !from.Before(until) {
			return &Error{Code: ErrInvalidInput, Msg: "DATE_WINDOW from must be < until"}
		}
	case RuleAuthMethod:
		if _, err := normalizedEnumList(r.Value["methods"], validAuthMethods); err != nil {
			return err
		}
	case RuleSubjectKind:
		if _, err := normalizedEnumList(r.Value["kinds"], validSubjectKinds); err != nil {
			return err
		}
	case RulePriorPurchase:
		req, reqHas := r.Value["requires_prior"]
		forb, forHas := r.Value["forbids_prior"]
		reqB, reqOK := req.(bool)
		forB, forOK := forb.(bool)
		if (reqHas && !reqOK) || (forHas && !forOK) {
			return &Error{Code: ErrInvalidInput, Msg: "PRIOR_PURCHASE fields must be booleans"}
		}
		if (reqOK && reqB) == (forOK && forB) {
			return &Error{Code: ErrInvalidInput, Msg: "PRIOR_PURCHASE requires exactly one of requires_prior/forbids_prior=true"}
		}
	case RuleSiteNetwork:
		if _, err := normalizedUUIDList(r.Value["guest_network_ids"]); err != nil {
			return err
		}
	}
	return nil
}

// ValidateGrantTier validates a tier for publication: an optional typed "match" (object with one
// recognized non-PMS condition) plus grant fields validated by BuildGrantSnapshot's bounds (with a
// dummy plan so the tier's own values are range-checked).
func ValidateGrantTier(t GrantTier) error {
	if t.Order < 0 {
		return &Error{Code: ErrInvalidInput, Msg: "tier_order must be >= 0"}
	}
	for k := range t.Value {
		if !knownGrantKeys[k] {
			return &Error{Code: ErrInvalidInput, Msg: "unknown grant key: " + k}
		}
	}
	if mv, has := t.Value["match"]; has {
		cond, ok := mv.(map[string]any)
		if !ok {
			return &Error{Code: ErrInvalidInput, Msg: "tier match must be an object"}
		}
		ctype, _ := cond["type"].(string)
		ct := strings.ToUpper(strings.TrimSpace(ctype))
		if ct == "" {
			return &Error{Code: ErrInvalidInput, Msg: "tier match requires a condition type"}
		}
		// re-use rule validation for the embedded condition (strip the "type" key into a rule)
		cv := map[string]any{}
		for k, val := range cond {
			if k == "type" {
				continue
			}
			cv[k] = val
		}
		if err := ValidateEligibilityRule(EligibilityRule{Type: ct, Value: cv}); err != nil {
			return err
		}
	}
	// range-check the tier's grant values via the shared builder against a permissive dummy plan.
	dummy := PlanRevisionRow{ID: "00000000-0000-0000-0000-000000000000", MaxConcurrentDevices: 1, TimeAccountingMode: "VALIDITY_WINDOW"}
	if _, err := BuildGrantSnapshot(t, dummy, PackageRevisionRow{ID: "00000000-0000-0000-0000-000000000000"}); err != nil {
		return err
	}
	return nil
}

var _ = time.Now

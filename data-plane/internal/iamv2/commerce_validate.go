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
	}
)

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
	if IsCapabilityDisabledRuleType(t) {
		return &Error{Code: ErrInvalidInput, Msg: "capability-disabled PMS rule type " + t + " cannot be published in Phase 2"}
	}
	allowed, known := ruleAllowedFields[t]
	if !known {
		return &Error{Code: ErrInvalidInput, Msg: "unknown rule type " + t}
	}
	if r.Value == nil {
		return &Error{Code: ErrInvalidInput, Msg: "rule value required"}
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
		if IsCapabilityDisabledRuleType(ct) {
			return &Error{Code: ErrInvalidInput, Msg: "tier match uses a capability-disabled PMS type"}
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

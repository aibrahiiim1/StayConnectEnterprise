package iamv2

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Publication-time validation of typed eligibility rules and grant tiers. Hotel Admin MUST call these
// before publishing an immutable package revision; runtime evaluation additionally fails closed. Every
// malformed/unknown-field/ambiguous rule is rejected here so bad config can never be published.

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var (
	validAuthMethods  = map[string]bool{"VOUCHER": true, "ACCOUNT": true, "OTP": true, "SOCIAL": true, "OPEN": true, "PMS": true}
	validSubjectKinds = map[string]bool{"VOUCHER": true, "ACCOUNT": true, "PRINCIPAL": true, "ANONYMOUS": true, "STAY": true}
	ruleAllowedFields = map[string]map[string]bool{
		RuleDateWindow:    {"from": true, "until": true},
		RuleAuthMethod:    {"methods": true},
		RuleSubjectKind:   {"kinds": true},
		RulePriorPurchase: {"requires_prior": true, "forbids_prior": true, "within_hours": true, "also_by_device": true},
		RuleSiteNetwork:   {"guest_network_ids": true},
		RuleClientGroup:   {"group_ids": true, "public": true},
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
		// Interface ids, not free text: a typo used to publish a rule that matched no stay, ever.
		if _, err := normalizedUUIDList(v["pms_interface_ids"]); err != nil {
			return bad("PMS_INTERFACE pms_interface_ids must be a non-empty list of PMS connection ids")
		}
		return nil
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
	case RuleClientGroup:
		pub, pubHas := r.Value["public"]
		if pubHas {
			if _, ok := pub.(bool); !ok {
				return &Error{Code: ErrInvalidInput, Msg: "CLIENT_GROUP public must be true or false"}
			}
		}
		if ids, has := r.Value["group_ids"]; has {
			if arr, ok := ids.([]any); !ok || len(arr) > 0 {
				if _, err := normalizedUUIDList(ids); err != nil {
					return &Error{Code: ErrInvalidInput, Msg: "CLIENT_GROUP group_ids must be a list of group ids"}
				}
			}
		}
		if pb, _ := pub.(bool); !pb {
			if _, err := normalizedUUIDList(r.Value["group_ids"]); err != nil {
				return &Error{Code: ErrInvalidInput, Msg: "CLIENT_GROUP needs at least one group, or public"}
			}
		}
	case RulePriorPurchase:
		if n, present, ok := parseIntField(r.Value, "within_hours"); present && (!ok || n < 1 || n > 8760) {
			return &Error{Code: ErrInvalidInput, Msg: "PRIOR_PURCHASE within_hours must be a whole number of hours from 1 to 8760"}
		}
		if raw, has := r.Value["also_by_device"]; has {
			if _, ok := raw.(bool); !ok {
				return &Error{Code: ErrInvalidInput, Msg: "PRIOR_PURCHASE also_by_device must be true or false"}
			}
		}
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

// VoucherBlocker names why a package revision can never be redeemed by a voucher, or "" when it can.
//
// A voucher subject carries no stay, no client group and its own auth method and kind, so some packages it is
// pinned to can never pass their own rules: the card is printed, handed to a guest and refused at the portal.
// Issue asks this first (O-26, O-4), so the operator hears it before printing, not the guest after.
func VoucherBlocker(dataAllocationMode string, rules []EligibilityRule) string {
	if strings.EqualFold(strings.TrimSpace(dataAllocationMode), AllocPerStayNight) {
		return "This package's data allowance is set per night of the stay, which only a guest who signed in " +
			"with their room has. A voucher has no stay, so vouchers cannot be issued for it."
	}
	for _, r := range rules {
		t := strings.ToUpper(strings.TrimSpace(r.Type))
		switch {
		case IsPMSRuleType(t):
			return "This package has a hotel condition (" + t + ") that needs a guest's stay. A voucher has no " +
				"stay, so a voucher for it could never be used."
		case t == RuleClientGroup:
			if pub, _ := r.Value["public"].(bool); !pub {
				return "This package is offered only to certain client groups. A voucher client is in no group, " +
					"so a voucher for it could never be used. Include public clients in its audience first."
			}
		case t == RuleAuthMethod:
			if !stringSet(r.Value["methods"])["VOUCHER"] {
				return "This package is limited to sign-in methods that do not include Voucher, so a voucher " +
					"for it could never be used."
			}
		case t == RuleSubjectKind:
			if !stringSet(r.Value["kinds"])["VOUCHER"] {
				return "This package is limited to kinds of client that do not include voucher clients, so a " +
					"voucher for it could never be used."
			}
		}
	}
	return ""
}

// GrantTiersEnforceableRefusal is the editor's refusal for grant tiers the enforcement plane does not honour
// (audit O-5, Product-Owner decision D-C), or "" when the tiers are acceptable.
//
// WHY REFUSE RATHER THAN ENFORCE. The stored tier model is (tier_order, grant_value): an ordered list of
// optional speed/quota/device overrides, chosen per client by a FIRST-MATCH condition -- not by usage. It has no
// threshold, so "speed steps applied in order" has no defined moment to step at, and a first-match list with an
// unconditional first tier never reaches the second at all. The overrides on the chosen tier are frozen into the
// quote and shown to the client, but shaping, device limits and quotas are enforced from the package's SERVICE
// PLAN revision. So the screen promised speeds the network never applied.
//
// Inventing a threshold model would be a product decision; refusing is not. A package carries exactly one grant
// tier, and that tier may hold only its optional match condition. Different speeds are different service plans.
// Packages already published keep their stored tiers (history is immutable, and an automatic republish such as
// a network replacement carries them unchanged); this applies to an operator saving a package.
func GrantTiersEnforceableRefusal(tiers []GrantTier) string {
	if len(tiers) > 1 {
		return "speed_steps_not_supported: a package has a single grant step. Speed steps were shown to clients " +
			"but never applied -- the speed, device limit and allowances a client gets come from the package's " +
			"service plan. Remove the extra steps; to sell different speeds, give each speed its own service plan " +
			"and package."
	}
	for _, t := range tiers {
		keys := make([]string, 0, len(t.Value))
		for k := range t.Value {
			if k != "match" {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			return "speed_steps_not_supported: the grant step sets " + strings.Join(keys, ", ") + ", which the " +
				"network does not apply -- the speed, device limit and allowances come from the package's service " +
				"plan. Clear the step's values and choose a service plan with the speeds you want."
		}
	}
	return ""
}

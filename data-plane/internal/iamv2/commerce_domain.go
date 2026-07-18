package iamv2

import (
	"sort"
	"strings"
	"time"
)

// ---- Phase-2 commerce domain: typed eligibility, ordered grant tiers, free-only money safety ----
//
// Everything here is PURE (no DB, no PMS). Rule/tier conditions use only the contract-approved,
// non-PMS typed dimensions available in Phase 2. PMS-dependent rule/tier types are recognized but
// CAPABILITY-DISABLED — they fail closed until Phase 3 provides authoritative Stay resolution. No
// executable scripts, arbitrary expressions or user SQL are ever evaluated.

// SubjectKind is the non-PMS subject class an auth-context resolved to.
type SubjectKind string

const (
	SubjectVoucher   SubjectKind = "VOUCHER"
	SubjectAccount   SubjectKind = "ACCOUNT"
	SubjectPrincipal SubjectKind = "PRINCIPAL" // OTP / SOCIAL
)

// EligibilitySubject is the set of NON-PMS facts a rule/tier condition may test in Phase 2.
type EligibilitySubject struct {
	Now                       time.Time
	AuthMethod                Method // VOUCHER | ACCOUNT | OTP | SOCIAL
	Kind                      SubjectKind
	GuestNetworkID            string
	HasPriorPurchaseOfPackage bool
}

// EligibilityRule is one typed rule attached to a package revision (rule_type + rule_value jsonb).
type EligibilityRule struct {
	Type  string
	Value map[string]any
}

// GrantTier is one ordered tier (tier_order + grant_value jsonb). grant_value may carry an optional
// typed "match" condition; a tier with no condition is unconditional.
type GrantTier struct {
	Order int
	Value map[string]any
}

// Contract-approved NON-PMS rule/condition types (Phase 2).
const (
	RuleDateWindow    = "DATE_WINDOW"    // {from, until} RFC3339 (either bound optional)
	RuleAuthMethod    = "AUTH_METHOD"    // {methods: ["ACCOUNT","VOUCHER","OTP","SOCIAL"]}
	RuleSubjectKind   = "SUBJECT_KIND"   // {kinds: ["ACCOUNT","VOUCHER","PRINCIPAL"]}
	RulePriorPurchase = "PRIOR_PURCHASE" // {requires_prior|forbids_prior: bool}
	RuleSiteNetwork   = "SITE_NETWORK"   // {guest_network_ids: [uuid,...]}
)

// pmsRuleTypes are recognized but capability-disabled in Phase 2 (no authoritative Stay data yet).
// A package/tier that depends on any of these is NOT eligible / does not match until Phase 3.
var pmsRuleTypes = map[string]bool{
	"STAY_STATUS": true, "STAY_LENGTH": true, "ROOM_TYPE": true, "VIP": true,
	"TRAVEL_AGENT": true, "PMS_INTERFACE": true, "RATE_PLAN": true,
}

// IsCapabilityDisabledRuleType reports whether a rule/condition type is a Phase-3 PMS type that must
// fail closed in Phase 2.
func IsCapabilityDisabledRuleType(t string) bool {
	return pmsRuleTypes[strings.ToUpper(strings.TrimSpace(t))]
}

// EvaluatePackageEligible returns whether ALL of a package revision's eligibility rules pass for the
// subject (AND semantics), and a non-sensitive reason when not. FAILS CLOSED: an unknown rule type, a
// capability-disabled PMS type, or a malformed rule value makes the package ineligible.
func EvaluatePackageEligible(rules []EligibilityRule, s EligibilitySubject) (bool, string) {
	for _, r := range rules {
		ok, reason := evalTypedCondition(r.Type, r.Value, s)
		if !ok {
			return false, reason
		}
	}
	return true, "eligible"
}

// FirstMatchTier returns the first tier (ascending tier_order) whose optional typed "match" condition
// passes; a tier with no condition matches unconditionally. Deterministic; capability-disabled
// conditions never match (fail closed). Returns ok=false when no tier matches.
func FirstMatchTier(tiers []GrantTier, s EligibilitySubject) (GrantTier, bool) {
	sorted := append([]GrantTier(nil), tiers...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })
	for _, t := range sorted {
		mv, has := t.Value["match"]
		if !has {
			return t, true // ONLY an absent match is unconditional
		}
		// A present match must be a JSON object carrying one recognized typed condition. Malformed,
		// empty, unknown or PMS-disabled match => the tier does NOT match (never reinterpreted as
		// unconditional).
		cond, ok := mv.(map[string]any)
		if !ok {
			continue
		}
		ctype, _ := cond["type"].(string)
		if strings.TrimSpace(ctype) == "" {
			continue
		}
		if ok, _ := evalTypedCondition(ctype, cond, s); ok {
			return t, true
		}
	}
	return GrantTier{}, false
}

// evalTypedCondition evaluates a single typed condition against the subject. Fail-closed on
// unknown/disabled/malformed.
func evalTypedCondition(ctype string, v map[string]any, s EligibilitySubject) (bool, string) {
	t := strings.ToUpper(strings.TrimSpace(ctype))
	if IsCapabilityDisabledRuleType(t) {
		return false, "capability_disabled_pms_rule"
	}
	switch t {
	case RuleDateWindow:
		from, fromPresent, fromOK := parseTimeField(v, "from")
		until, untilPresent, untilOK := parseTimeField(v, "until")
		if !fromPresent && !untilPresent {
			return false, "date_window_needs_a_bound" // at least one bound required
		}
		if (fromPresent && !fromOK) || (untilPresent && !untilOK) {
			return false, "malformed_date_window" // present-but-malformed never treated as omitted
		}
		if fromPresent && untilPresent && !from.Before(until) {
			return false, "date_window_from_ge_until"
		}
		if fromPresent && s.Now.Before(from) {
			return false, "before_window"
		}
		if untilPresent && !s.Now.Before(until) {
			return false, "after_window" // upper bound EXCLUSIVE
		}
		return true, ""
	case RuleAuthMethod:
		set := stringSet(v["methods"])
		if len(set) == 0 {
			return false, "malformed_auth_method_rule"
		}
		if set[strings.ToUpper(string(s.AuthMethod))] {
			return true, ""
		}
		return false, "auth_method_not_allowed"
	case RuleSubjectKind:
		set := stringSet(v["kinds"])
		if len(set) == 0 {
			return false, "malformed_subject_kind_rule"
		}
		if set[strings.ToUpper(string(s.Kind))] {
			return true, ""
		}
		return false, "subject_kind_not_allowed"
	case RulePriorPurchase:
		reqRaw, reqHas := v["requires_prior"]
		forRaw, forHas := v["forbids_prior"]
		reqB, reqOK := reqRaw.(bool)
		forB, forOK := forRaw.(bool)
		if (reqHas && !reqOK) || (forHas && !forOK) {
			return false, "malformed_prior_purchase" // wrong JSON types
		}
		reqOn := reqOK && reqB
		forOn := forOK && forB
		if reqOn == forOn { // neither true, or both true -> ambiguous/malformed
			return false, "prior_purchase_needs_exactly_one"
		}
		if reqOn && !s.HasPriorPurchaseOfPackage {
			return false, "requires_prior_purchase"
		}
		if forOn && s.HasPriorPurchaseOfPackage {
			return false, "forbids_prior_purchase"
		}
		return true, ""
	case RuleSiteNetwork:
		set := stringSet(v["guest_network_ids"])
		if len(set) == 0 {
			return false, "malformed_site_network_rule"
		}
		if set[strings.ToLower(strings.TrimSpace(s.GuestNetworkID))] {
			return true, ""
		}
		return false, "guest_network_not_allowed"
	default:
		return false, "unknown_rule_type" // fail closed
	}
}

// parseTimeField returns (time, present, valid). A field that is absent/empty is (_, false, _). A field
// that is present but not a valid RFC3339 string is (_, true, false) so the caller can fail closed
// rather than treat malformed input as an omitted optional bound.
func parseTimeField(v map[string]any, key string) (time.Time, bool, bool) {
	raw, has := v[key]
	if !has {
		return time.Time{}, false, false
	}
	sv, ok := raw.(string)
	if !ok || strings.TrimSpace(sv) == "" {
		return time.Time{}, true, false // present but wrong type/empty
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(sv))
	if err != nil {
		return time.Time{}, true, false
	}
	return t, true, true
}

// stringSet lowercases-and-uppercases-insensitively builds a membership set from a JSON array of
// strings (values are compared upper-cased for enum fields; caller lower-cases for id fields).
func stringSet(a any) map[string]bool {
	arr, ok := a.([]any)
	if !ok {
		return nil
	}
	out := make(map[string]bool, len(arr))
	for _, x := range arr {
		if sv, ok := x.(string); ok {
			out[strings.ToUpper(strings.TrimSpace(sv))] = true
			out[strings.ToLower(strings.TrimSpace(sv))] = true
		}
	}
	return out
}

// ---- money safety (free-only, Phase 2) ----

// MoneySpec is the pricing/settlement of a package revision as seen by the free-purchase gate.
type MoneySpec struct {
	PriceMinor        int64
	Currency          string // ISO-4217 alpha-3
	CurrencyExponent  int
	SettlementMethods []string
}

// IsFreePackage reports whether a package revision qualifies for the Phase-2 free-only path, with a
// deterministic reason when not. Requires price_minor == 0, settlement methods EXACTLY {NOT_REQUIRED},
// and an AUTHORITATIVE ISO-4217 currency whose supplied exponent matches (zero price is still
// money-typed).
func IsFreePackage(m MoneySpec) (bool, string) {
	if m.PriceMinor != 0 {
		return false, "not_free"
	}
	if len(m.SettlementMethods) != 1 || strings.ToUpper(strings.TrimSpace(m.SettlementMethods[0])) != "NOT_REQUIRED" {
		return false, "settlement_not_free"
	}
	if _, err := ValidateCurrency(m.Currency, m.CurrencyExponent); err != nil {
		return false, "invalid_currency"
	}
	return true, "free"
}

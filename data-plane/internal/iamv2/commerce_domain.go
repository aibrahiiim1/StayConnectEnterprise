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

// EligibilitySubject is what a rule or tier condition may test.
//
// Phase 2 could only carry non-PMS facts, so the seven PMS rule types below were recognised but
// capability-disabled: with no authoritative Stay data, evaluating them would have meant guessing, and a
// guess that says "eligible" hands out access nobody authorised. Phase 3 supplies that data, so the same
// rule types now evaluate for real — but ONLY when the evidence is present and coherent. StayEvidence being
// absent is not "no constraint"; it is "we cannot answer", and the answer to that is still no.
type EligibilitySubject struct {
	Now                       time.Time
	AuthMethod                Method // VOUCHER | ACCOUNT | OTP | SOCIAL | PMS
	Kind                      SubjectKind
	GuestNetworkID            string
	HasPriorPurchaseOfPackage bool

	// Stay is the authoritative Stay evidence for a PMS-authenticated subject. Nil for every other method,
	// which is what keeps a voucher guest from matching a room-type rule.
	Stay *StayEvidence
}

// StayEvidence is the server-pinned Stay state a PMS rule may test. Every field comes from the resolved Stay
// row under the pinned Interface Revision — never from a guest, and never from a request body.
type StayEvidence struct {
	StayID      string
	InterfaceID string
	Status      string // IN_HOUSE | POST_STAY_ACTIVE | ...
	RoomType    string
	RatePlan    string
	TravelAgent string
	VIP         *bool // nil means the PMS did not state it — not "false"
	Arrival     *time.Time
	Departure   *time.Time
	// EvidenceVersion is the occupancy-evidence version this snapshot was taken at. It is carried into the
	// Quote so a decision can be re-read later against the exact evidence that produced it.
	EvidenceVersion int64
}

// Nights is the stay length in nights, and whether it could be computed at all. A missing bound means the
// question cannot be answered, which fails closed rather than defaulting to zero.
func (e *StayEvidence) Nights() (int, bool) {
	if e == nil || e.Arrival == nil || e.Departure == nil {
		return 0, false
	}
	d := e.Departure.Sub(*e.Arrival)
	if d < 0 {
		return 0, false
	}
	return int(d.Hours() / 24), true
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

// The PMS rule types. Phase 2 recognised them and refused to evaluate them, because there was no
// authoritative Stay data and a guess in the permissive direction hands out access. Phase 3 evaluates them
// against server-pinned Stay evidence — and still refuses when that evidence is missing or incoherent.
const (
	RuleStayStatus   = "STAY_STATUS"   // {statuses: ["IN_HOUSE", ...]}
	RuleStayLength   = "STAY_LENGTH"   // {min_nights?, max_nights?}
	RuleRoomType     = "ROOM_TYPE"     // {room_types: [...]}
	RuleVIP          = "VIP"           // {is_vip: bool}
	RuleTravelAgent  = "TRAVEL_AGENT"  // {travel_agents: [...]}
	RulePMSInterface = "PMS_INTERFACE" // {pms_interface_ids: [uuid,...]}
	RuleRatePlan     = "RATE_PLAN"     // {rate_plans: [...]}
)

var pmsRuleTypes = map[string]bool{
	RuleStayStatus: true, RuleStayLength: true, RuleRoomType: true, RuleVIP: true,
	RuleTravelAgent: true, RulePMSInterface: true, RuleRatePlan: true,
}

// IsPMSRuleType reports whether a rule/condition type needs authoritative Stay evidence.
func IsPMSRuleType(t string) bool {
	return pmsRuleTypes[strings.ToUpper(strings.TrimSpace(t))]
}

// IsCapabilityDisabledRuleType reports whether a type cannot be evaluated for THIS subject. A PMS rule
// without Stay evidence is exactly that: recognised, but unanswerable, so it fails closed.
func IsCapabilityDisabledRuleType(t string) bool {
	return false
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
	if IsPMSRuleType(t) {
		if s.Stay == nil {
			// The rule is recognised and the subject cannot answer it. That is not "no constraint" — it is
			// "unknown", and a permissive reading of unknown is how a voucher guest matches a suite rate.
			return false, "pms_rule_without_stay_evidence"
		}
		if s.Stay.EvidenceVersion <= 0 {
			// Evidence exists but was never authoritatively versioned, so it cannot be reproduced later.
			return false, "pms_rule_without_versioned_evidence"
		}
		return evalPMSCondition(t, v, s.Stay)
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

// evalPMSCondition evaluates one PMS rule against server-pinned Stay evidence. Every branch fails closed on a
// malformed rule or on evidence the PMS did not state: a rule the property wrote must never be satisfied by
// the absence of the thing it tests.
func evalPMSCondition(t string, v map[string]any, e *StayEvidence) (bool, string) {
	switch t {
	case RuleStayStatus:
		set := stringSet(v["statuses"])
		if len(set) == 0 {
			return false, "malformed_stay_status_rule"
		}
		if e.Status == "" {
			return false, "stay_status_unknown"
		}
		if set[strings.ToUpper(e.Status)] {
			return true, ""
		}
		return false, "stay_status_not_matched"

	case RuleStayLength:
		nights, ok := e.Nights()
		if !ok {
			return false, "stay_length_unknown"
		}
		minN, minPresent, minOK := parseIntField(v, "min_nights")
		maxN, maxPresent, maxOK := parseIntField(v, "max_nights")
		if !minPresent && !maxPresent {
			return false, "stay_length_needs_a_bound"
		}
		if (minPresent && !minOK) || (maxPresent && !maxOK) {
			return false, "malformed_stay_length_rule"
		}
		if minPresent && maxPresent && minN > maxN {
			return false, "stay_length_min_gt_max"
		}
		if minPresent && nights < minN {
			return false, "stay_shorter_than_min"
		}
		if maxPresent && nights > maxN {
			return false, "stay_longer_than_max"
		}
		return true, ""

	case RuleRoomType:
		return matchStringField(stringSet(v["room_types"]), e.RoomType, "room_type")
	case RuleTravelAgent:
		return matchStringField(stringSet(v["travel_agents"]), e.TravelAgent, "travel_agent")
	case RuleRatePlan:
		return matchStringField(stringSet(v["rate_plans"]), e.RatePlan, "rate_plan")
	case RulePMSInterface:
		return matchStringField(stringSet(v["pms_interface_ids"]), e.InterfaceID, "pms_interface")

	case RuleVIP:
		want, present := v["is_vip"].(bool)
		if !present {
			return false, "malformed_vip_rule"
		}
		if e.VIP == nil {
			// The PMS did not state it. Treating "not stated" as false would silently satisfy every
			// {is_vip:false} rule for guests whose status the property simply has not recorded.
			return false, "vip_unknown"
		}
		if *e.VIP == want {
			return true, ""
		}
		return false, "vip_not_matched"
	}
	return false, "unknown_pms_rule"
}

// matchStringField is the shared shape of the set-membership PMS rules.
func matchStringField(set map[string]bool, value, label string) (bool, string) {
	if len(set) == 0 {
		return false, "malformed_" + label + "_rule"
	}
	if strings.TrimSpace(value) == "" {
		return false, label + "_unknown"
	}
	if set[strings.ToUpper(strings.TrimSpace(value))] {
		return true, ""
	}
	return false, label + "_not_matched"
}

// parseIntField reads an optional integer bound: (value, present, wellFormed). A present-but-malformed bound
// is never treated as omitted — that would turn a typo into a wider offer.
func parseIntField(v map[string]any, key string) (int, bool, bool) {
	raw, present := v[key]
	if !present || raw == nil {
		return 0, false, true
	}
	switch n := raw.(type) {
	case float64:
		if n != float64(int(n)) || n < 0 {
			return 0, true, false
		}
		return int(n), true, true
	case int:
		if n < 0 {
			return 0, true, false
		}
		return n, true, true
	case int64:
		if n < 0 {
			return 0, true, false
		}
		return int(n), true, true
	}
	return 0, true, false
}

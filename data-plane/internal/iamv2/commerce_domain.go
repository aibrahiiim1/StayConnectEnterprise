package iamv2

import (
	"math"
	"sort"
	"strings"
	"time"
)

type SubjectKind string

const (
	SubjectVoucher   SubjectKind = "VOUCHER"
	SubjectAccount   SubjectKind = "ACCOUNT"
	SubjectPrincipal SubjectKind = "PRINCIPAL" // OTP / SOCIAL
	// Room sign-in is its own kind; evaluating it as PRINCIPAL made SUBJECT_KIND rules written for
	// email/phone/social sign-ins match room guests.
	SubjectStay SubjectKind = "STAY"
)

// EligibilitySubject is what a rule or tier condition may test. PMS rules evaluate only when StayEvidence is
// present and coherent; absent evidence means "cannot answer", and the answer is no.
type EligibilitySubject struct {
	Now                       time.Time
	AuthMethod                Method // VOUCHER | ACCOUNT | OTP | SOCIAL | PMS
	Kind                      SubjectKind
	GuestNetworkID            string
	HasPriorPurchaseOfPackage bool
	// The effective Client Group pinned on the auth context ("" = Public); recorded, never re-derived here.
	ClientGroupID string

	// GuestNetworkLineage lists the client networks GuestNetworkID continues (transitively, from
	// iam_v2.guest_network_lineage). A replacement gives a network a new id while revisions are immutable and
	// name networks by id, so without lineage SITE_NETWORK rules stop matching. Server-derived, only widens
	// backward; empty is normal.
	GuestNetworkLineage []string

	// Authoritative Stay evidence for a PMS-authenticated subject; nil for every other method, so a voucher
	// guest cannot match a room-type rule.
	Stay *StayEvidence
}

// Server-pinned Stay state a PMS rule may test, from the resolved Stay row under the pinned Interface
// Revision; never from a guest or request body.
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
	// The occupancy-evidence version, carried into the Quote so a decision can be re-read against the evidence
	// that produced it.
	EvidenceVersion int64
}

// A missing bound means the question cannot be answered and fails closed rather than defaulting to zero.
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

type EligibilityRule struct {
	Type  string
	Value map[string]any
}

type GrantTier struct {
	Order int
	Value map[string]any
}

const (
	RuleDateWindow    = "DATE_WINDOW"    // {from, until} RFC3339 (either bound optional)
	RuleAuthMethod    = "AUTH_METHOD"    // {methods: ["ACCOUNT","VOUCHER","OTP","SOCIAL"]}
	RuleSubjectKind   = "SUBJECT_KIND"   // {kinds: ["ACCOUNT","VOUCHER","PRINCIPAL"]}
	RulePriorPurchase = "PRIOR_PURCHASE" // {requires_prior|forbids_prior: bool, within_hours?: int, also_by_device?: bool}
	// CLIENT_GROUP is the package audience: {group_ids: [uuid], public: bool}; passes for a listed group
	// member or, with public, a Client in no group.
	RuleClientGroup = "CLIENT_GROUP"
	RuleSiteNetwork = "SITE_NETWORK" // {guest_network_ids: [uuid,...]}
)

// PMS rule types evaluate against server-pinned Stay evidence and refuse when it is missing or incoherent.
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

func IsPMSRuleType(t string) bool {
	return pmsRuleTypes[strings.ToUpper(strings.TrimSpace(t))]
}

// A PMS rule without Stay evidence is recognised but unanswerable, so it fails closed.
func IsCapabilityDisabledRuleType(t string) bool {
	return false
}

// All rules must pass (AND). Fails closed: unknown rule type, capability-disabled PMS type, or malformed value
// makes the package ineligible.
func EvaluatePackageEligible(rules []EligibilityRule, s EligibilitySubject) (bool, string) {
	for _, r := range rules {
		ok, reason := evalTypedCondition(r.Type, r.Value, s)
		if !ok {
			return false, reason
		}
	}
	return true, "eligible"
}

// First tier by ascending tier_order whose optional "match" passes; no condition matches unconditionally;
// disabled conditions never match.
func FirstMatchTier(tiers []GrantTier, s EligibilitySubject) (GrantTier, bool) {
	sorted := append([]GrantTier(nil), tiers...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })
	for _, t := range sorted {
		mv, has := t.Value["match"]
		if !has {
			return t, true // ONLY an absent match is unconditional
		}
		// A present match must be an object with one recognised condition; anything else does not match and is
		// never reinterpreted as unconditional.
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

func evalTypedCondition(ctype string, v map[string]any, s EligibilitySubject) (bool, string) {
	t := strings.ToUpper(strings.TrimSpace(ctype))
	if IsPMSRuleType(t) {
		if s.Stay == nil {
			// Unanswerable is not "no constraint"; a permissive reading is how a voucher guest matches a suite
			// rate.
			return false, "pms_rule_without_stay_evidence"
		}
		if s.Stay.EvidenceVersion <= 0 {
			// Evidence that was never authoritatively versioned cannot be reproduced later.
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
	case RuleClientGroup:
		ids := stringSet(v["group_ids"])
		pub, _ := v["public"].(bool)
		if len(ids) == 0 && !pub {
			return false, "malformed_client_group_rule"
		}
		if s.ClientGroupID == "" {
			if pub {
				return true, ""
			}
			return false, "client_group_not_allowed"
		}
		if ids[strings.ToLower(strings.TrimSpace(s.ClientGroupID))] {
			return true, ""
		}
		return false, "client_group_not_allowed"
	case RuleSiteNetwork:
		set := stringSet(v["guest_network_ids"])
		if len(set) == 0 {
			return false, "malformed_site_network_rule"
		}
		if set[strings.ToLower(strings.TrimSpace(s.GuestNetworkID))] {
			return true, ""
		}
		// Or a client network this one continues: a replacement changes the network id for reasons unrelated
		// to who may buy what, and lineage only reaches backward to networks this one succeeded.
		for _, prior := range s.GuestNetworkLineage {
			if set[strings.ToLower(strings.TrimSpace(prior))] {
				return true, ""
			}
		}
		return false, "guest_network_not_allowed"
	default:
		return false, "unknown_rule_type" // fail closed
	}
}

type PriorPurchasePolicy struct {
	Present      bool
	WithinHours  int  // 0 = ever
	AlsoByDevice bool // the device's own history counts too (default true on a free package)
}

// Defaults: once per Client, ever; the device counts when the package is free, which is what blocks a second
// free allowance via another email on the same phone.
func PriorPurchasePolicyOf(rules []EligibilityRule, priceMinor int64) PriorPurchasePolicy {
	for _, r := range rules {
		if strings.ToUpper(strings.TrimSpace(r.Type)) != RulePriorPurchase {
			continue
		}
		p := PriorPurchasePolicy{Present: true, AlsoByDevice: priceMinor == 0}
		if n, present, ok := parseIntField(r.Value, "within_hours"); present && ok && n > 0 {
			p.WithinHours = n
		}
		if b, ok := r.Value["also_by_device"].(bool); ok {
			p.AlsoByDevice = b
		}
		return p
	}
	return PriorPurchasePolicy{}
}

// A voucher is its own allowance: the device switch exists to stop repeat FREE allowances, and applied to a
// voucher it refused a guest's second card on the same phone. The voucher's own history still counts.
func (p PriorPurchasePolicy) ForSubject(k SubjectKind) PriorPurchasePolicy {
	if k == SubjectVoucher {
		p.AlsoByDevice = false
	}
	return p
}

// Returns (time, present, valid); present-but-invalid lets the caller fail closed instead of treating it as an
// omitted bound.
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

type MoneySpec struct {
	PriceMinor        int64
	Currency          string // ISO-4217 alpha-3
	CurrencyExponent  int
	SettlementMethods []string
}

// Free means price_minor == 0, settlement methods exactly {NOT_REQUIRED}, and an authoritative ISO-4217
// currency whose exponent matches.
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

// Every branch fails closed on a malformed rule or evidence the PMS did not state; absence must never satisfy
// a rule.
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
			// Not stated is not false: treating it so would satisfy every {is_vip:false} rule for guests whose
			// status was never recorded.
			return false, "vip_unknown"
		}
		if *e.VIP == want {
			return true, ""
		}
		return false, "vip_not_matched"
	}
	return false, "unknown_pms_rule"
}

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

// parseIntField reads an optional non-negative whole number: (value, present, wellFormed). It must accept
// every numeric shape in the system (json.Number from edged and the repository, float64 from plain Unmarshal
// in scd, int/int64 from Postgres) so a stored rule means the same everywhere. A fractional or exponent value
// is refused, not rounded; a malformed bound is never treated as omitted.
func parseIntField(v map[string]any, key string) (int, bool, bool) {
	raw, present := v[key]
	if !present || raw == nil {
		return 0, false, true
	}
	// All integer shapes go through the one strict reader the grant snapshot uses.
	if n, ok := asInt64(raw); ok {
		if n < 0 || n > math.MaxInt32 {
			return 0, true, false
		}
		return int(n), true, true
	}
	if f, ok := raw.(float64); ok {
		if f != math.Trunc(f) || f < 0 || f > math.MaxInt32 {
			return 0, true, false
		}
		return int(f), true, true
	}
	return 0, true, false
}

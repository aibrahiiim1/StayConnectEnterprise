package iamv2

// CLIENT GROUPS: WHO A CLIENT IS, SEPARATED FROM WHAT THEY MAY HAVE.
//
// Contract: docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §3. A group is a site policy object
// with OR-ed membership rules. Membership is a PURE FUNCTION of a Client's verified identity factors, evaluated
// when scd creates the auth context and pinned on it, so eligibility reads a recorded decision.
//
// Nothing here can be satisfied by what a client typed: an EMAIL factor exists only after a code or a trusted
// issuer verified the mailbox, and the IdP claims live in attrs that only scd writes from a verified id_token.

import (
	"regexp"
	"sort"
	"strings"
)

// Rule types. IDP_GROUP is reserved by the contract and refused at publication until a provider emits groups.
const (
	GroupRuleEmailDomain     = "EMAIL_DOMAIN"      // {domains:[], include_subdomains?:bool}
	GroupRuleIDPTenant       = "IDP_TENANT"        // {provider, tenant_ids:[]}
	GroupRuleIDPHostedDomain = "IDP_HOSTED_DOMAIN" // {provider, domains:[]}
)

// ClientGroupRule is one membership rule.
type ClientGroupRule struct {
	ID    string
	Type  string
	Value map[string]any
}

// ClientGroup is a site's group with its rules.
type ClientGroup struct {
	ID       string
	Name     string
	Priority int
	Enabled  bool
	Rules    []ClientGroupRule
}

// GroupDecision is the effective group of a sign-in, with the evidence that decided it.
type GroupDecision struct {
	GroupID   string
	GroupName string
	// Evidence is what is pinned on the auth context: {group, rule, factor, matched:[group ids]}. It names
	// rule ids and factor kinds, never an address.
	Evidence map[string]any
}

var (
	reDomain = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	reGUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// ValidateClientGroupRule refuses a rule that could never match or that names a verification this product
// cannot perform. Shared by the Admin Console write path (edged) and the evaluator's fail-closed branch.
func ValidateClientGroupRule(t string, v map[string]any) error {
	bad := func(m string) error { return &Error{Code: ErrInvalidInput, Msg: m} }
	switch t {
	case GroupRuleEmailDomain:
		doms := stringList(v["domains"])
		if len(doms) == 0 {
			return bad("EMAIL_DOMAIN needs at least one domain")
		}
		for _, d := range doms {
			if !reDomain.MatchString(strings.ToLower(d)) {
				return bad("EMAIL_DOMAIN: " + d + " is not a domain name")
			}
		}
		if raw, ok := v["include_subdomains"]; ok {
			if _, isB := raw.(bool); !isB {
				return bad("EMAIL_DOMAIN: include_subdomains must be true or false")
			}
		}
		for k := range v {
			if k != "domains" && k != "include_subdomains" {
				return bad("EMAIL_DOMAIN: unknown field " + k)
			}
		}
		return nil
	case GroupRuleIDPTenant:
		p, _ := v["provider"].(string)
		if strings.ToLower(strings.TrimSpace(p)) != "microsoft" {
			return bad("IDP_TENANT: provider must be microsoft (the only provider that asserts a tenant)")
		}
		ids := stringList(v["tenant_ids"])
		if len(ids) == 0 {
			return bad("IDP_TENANT needs at least one tenant (directory) id")
		}
		for _, id := range ids {
			if !reGUID.MatchString(id) {
				return bad("IDP_TENANT: " + id + " is not a directory (tenant) id")
			}
		}
		for k := range v {
			if k != "provider" && k != "tenant_ids" {
				return bad("IDP_TENANT: unknown field " + k)
			}
		}
		return nil
	case GroupRuleIDPHostedDomain:
		p, _ := v["provider"].(string)
		if strings.ToLower(strings.TrimSpace(p)) != "google" {
			return bad("IDP_HOSTED_DOMAIN: provider must be google (Google Workspace hosted domain)")
		}
		doms := stringList(v["domains"])
		if len(doms) == 0 {
			return bad("IDP_HOSTED_DOMAIN needs at least one domain")
		}
		for _, d := range doms {
			if !reDomain.MatchString(strings.ToLower(d)) {
				return bad("IDP_HOSTED_DOMAIN: " + d + " is not a domain name")
			}
		}
		for k := range v {
			if k != "provider" && k != "domains" {
				return bad("IDP_HOSTED_DOMAIN: unknown field " + k)
			}
		}
		return nil
	case "IDP_GROUP":
		return bad("IDP_GROUP is reserved: no configured identity provider emits group claims yet")
	}
	return bad("unknown membership rule type " + t)
}

// EvaluateClientGroup decides the effective group for a set of verified factors. It returns ok=false when the
// Client is in no enabled group (the implicit Public group). A malformed rule never matches (fail closed).
func EvaluateClientGroup(groups []ClientGroup, factors []FactorClaim) (GroupDecision, bool) {
	type hit struct {
		g    ClientGroup
		rule ClientGroupRule
		f    FactorClaim
	}
	var hits []hit
	for _, g := range groups {
		if !g.Enabled {
			continue
		}
		for _, r := range g.Rules {
			if ValidateClientGroupRule(r.Type, r.Value) != nil {
				continue
			}
			for _, f := range factors {
				if ruleMatches(r, f) {
					hits = append(hits, hit{g, r, f})
					break
				}
			}
			if len(hits) > 0 && hits[len(hits)-1].g.ID == g.ID {
				break // one hit per group is enough
			}
		}
	}
	if len(hits) == 0 {
		return GroupDecision{}, false
	}
	// Deterministic: lowest priority, then name, then id.
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i].g, hits[j].g
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		return a.ID < b.ID
	})
	matched := make([]string, 0, len(hits))
	for _, h := range hits {
		matched = append(matched, h.g.ID)
	}
	w := hits[0]
	return GroupDecision{GroupID: w.g.ID, GroupName: w.g.Name, Evidence: map[string]any{
		"group": w.g.ID, "group_name": w.g.Name, "rule": w.rule.ID, "rule_type": w.rule.Type,
		"factor": w.f.Key(), "matched": matched,
	}}, true
}

func ruleMatches(r ClientGroupRule, f FactorClaim) bool {
	switch r.Type {
	case GroupRuleEmailDomain:
		if f.Type != "EMAIL" {
			return false
		}
		at := strings.LastIndexByte(f.Value, '@')
		if at < 0 {
			return false
		}
		dom := strings.ToLower(f.Value[at+1:])
		sub, _ := r.Value["include_subdomains"].(bool)
		for _, d := range stringList(r.Value["domains"]) {
			d = strings.ToLower(d)
			if dom == d || (sub && strings.HasSuffix(dom, "."+d)) {
				return true
			}
		}
		return false
	case GroupRuleIDPTenant:
		p, _ := r.Value["provider"].(string)
		if f.Type != "SOCIAL_SUBJECT" || !strings.EqualFold(f.Issuer, strings.TrimSpace(p)) {
			return false
		}
		tid := f.Attrs["tid"]
		if tid == "" {
			return false
		}
		for _, id := range stringList(r.Value["tenant_ids"]) {
			if strings.EqualFold(id, tid) {
				return true
			}
		}
		return false
	case GroupRuleIDPHostedDomain:
		p, _ := r.Value["provider"].(string)
		if f.Type != "SOCIAL_SUBJECT" || !strings.EqualFold(f.Issuer, strings.TrimSpace(p)) {
			return false
		}
		hd := strings.ToLower(f.Attrs["hd"])
		if hd == "" {
			return false
		}
		for _, d := range stringList(r.Value["domains"]) {
			if strings.ToLower(d) == hd {
				return true
			}
		}
		return false
	}
	return false
}

// stringList reads a JSON list of non-empty strings; anything else is an empty list.
func stringList(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		if ss, ok2 := v.([]string); ok2 {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

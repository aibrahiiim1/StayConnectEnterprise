package iamv2

import (
	"strings"
	"testing"
)

// THE IDENTITY, GROUP AND FREE-ALLOWANCE RULES OF docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md,
// pinned as behaviour.

func TestSocialFactorsKeyTheSubjectAndLinkOnlyATrustedVerifiedEmail(t *testing.T) {
	// Google: subject is the identity; the verified email is a second factor AND a legacy lookup.
	p, sec := SocialFactors("Google", "110234", "Alice@Example.com", true, map[string]string{"hd": "example.com"})
	if p.Type != "SOCIAL_SUBJECT" || p.Issuer != "google" || p.Value != "110234" {
		t.Fatalf("primary must be the provider subject: %+v", p)
	}
	if p.Attrs["hd"] != "example.com" || p.Attrs["email"] != "alice@example.com" || p.Attrs["source"] != "google" {
		t.Fatalf("verified claims must ride on the subject: %+v", p.Attrs)
	}
	var legacy, email int
	for _, f := range sec {
		switch {
		case f.Type == "SOCIAL_SUBJECT" && f.LookupOnly && f.Value == "alice@example.com":
			legacy++
		case f.Type == "EMAIL" && !f.LookupOnly && f.Value == "alice@example.com":
			email++
		default:
			t.Fatalf("unexpected secondary %+v", f)
		}
	}
	if legacy != 1 || email != 1 {
		t.Fatalf("google must carry one legacy lookup and one EMAIL factor, got legacy=%d email=%d", legacy, email)
	}
	// Facebook asserts no verification: the subject stands alone, even when an email came back.
	_, sec = SocialFactors("facebook", "fb-1", "bob@example.com", true, nil)
	for _, f := range sec {
		if f.Type == "EMAIL" {
			t.Fatalf("facebook must never produce an EMAIL factor: %+v", f)
		}
	}
	// An UNVERIFIED email never becomes a factor of any kind.
	_, sec = SocialFactors("google", "x", "carol@example.com", false, nil)
	if len(sec) != 0 {
		t.Fatalf("an unverified email must link nothing, got %+v", sec)
	}
}

func TestMaskIdentityNeverShowsTheWholeValue(t *testing.T) {
	cases := map[string]FactorClaim{
		"a•••@example.com": {Type: "EMAIL", Value: "alice@example.com"},
		"+20•••1234":       {Type: "PHONE", Value: "+201001231234"},
		"Google account":   {Type: "SOCIAL_SUBJECT", Issuer: "google", Value: "110234"},
		"b•••@example.com": {Type: "SOCIAL_SUBJECT", Issuer: "apple", Value: "s", Attrs: map[string]string{"email": "bob@example.com"}},
	}
	for want, f := range cases {
		if got := MaskIdentity(f); got != want {
			t.Errorf("%+v: got %q want %q", f, got, want)
		}
		if strings.Contains(MaskIdentity(f), "alice") || strings.Contains(MaskIdentity(f), "1001231234") {
			t.Errorf("mask leaked the value: %q", MaskIdentity(f))
		}
	}
}

func groups() []ClientGroup {
	return []ClientGroup{
		{ID: "g-partners", Name: "Partners", Priority: 50, Enabled: true, Rules: []ClientGroupRule{
			{ID: "r1", Type: GroupRuleEmailDomain, Value: map[string]any{"domains": []any{"partner.com"}}}}},
		{ID: "g-staff", Name: "Employees", Priority: 10, Enabled: true, Rules: []ClientGroupRule{
			{ID: "r2", Type: GroupRuleEmailDomain, Value: map[string]any{"domains": []any{"company.com", "company.ae"}, "include_subdomains": true}},
			{ID: "r3", Type: GroupRuleIDPTenant, Value: map[string]any{"provider": "microsoft", "tenant_ids": []any{"11111111-2222-3333-4444-555555555555"}}},
			{ID: "r4", Type: GroupRuleIDPHostedDomain, Value: map[string]any{"provider": "google", "domains": []any{"company.com"}}}}},
		{ID: "g-off", Name: "Disabled", Priority: 1, Enabled: false, Rules: []ClientGroupRule{
			{ID: "r5", Type: GroupRuleEmailDomain, Value: map[string]any{"domains": []any{"company.com"}}}}},
	}
}

func TestClientGroupMembershipIsAFunctionOfVerifiedFactors(t *testing.T) {
	email := func(v string) []FactorClaim { return []FactorClaim{{Type: "EMAIL", Value: v}} }
	if d, ok := EvaluateClientGroup(groups(), email("ann@company.com")); !ok || d.GroupID != "g-staff" {
		t.Fatalf("company.com email must be Employees, got %+v %v", d, ok)
	}
	if d, ok := EvaluateClientGroup(groups(), email("ann@mail.company.ae")); !ok || d.GroupID != "g-staff" {
		t.Fatalf("subdomain with include_subdomains must match, got %+v %v", d, ok)
	}
	if d, ok := EvaluateClientGroup(groups(), email("ann@mail.partner.com")); ok {
		t.Fatalf("subdomain WITHOUT include_subdomains must not match, got %+v", d)
	}
	if _, ok := EvaluateClientGroup(groups(), email("ann@gmail.com")); ok {
		t.Fatal("a public address is in no group")
	}
	// The disabled group never matches, even at priority 1.
	if d, _ := EvaluateClientGroup(groups(), email("ann@company.com")); d.GroupID == "g-off" {
		t.Fatal("a disabled group must never win")
	}
	// A Microsoft work account proves the tenant; a bare email in the partner domain does not outrank it.
	ms := []FactorClaim{
		{Type: "EMAIL", Value: "ann@partner.com"},
		{Type: "SOCIAL_SUBJECT", Issuer: "microsoft", Value: "tid:oid", Attrs: map[string]string{"tid": "11111111-2222-3333-4444-555555555555"}},
	}
	d, ok := EvaluateClientGroup(groups(), ms)
	if !ok || d.GroupID != "g-staff" {
		t.Fatalf("tenant match must win by priority, got %+v", d)
	}
	matched, _ := d.Evidence["matched"].([]string)
	if len(matched) != 2 {
		t.Fatalf("evidence must list every matched group, got %v", d.Evidence)
	}
	if d.Evidence["rule"] != "r3" || d.Evidence["factor"] != "SOCIAL_SUBJECT:microsoft" {
		t.Fatalf("evidence must name the deciding rule and factor kind: %v", d.Evidence)
	}
	for _, v := range d.Evidence {
		if s, isS := v.(string); isS && strings.Contains(s, "@") {
			t.Fatalf("evidence must never carry an address: %v", d.Evidence)
		}
	}
	// Google Workspace hosted domain.
	g := []FactorClaim{{Type: "SOCIAL_SUBJECT", Issuer: "google", Value: "1", Attrs: map[string]string{"hd": "company.com"}}}
	if d, ok := EvaluateClientGroup(groups(), g); !ok || d.GroupID != "g-staff" {
		t.Fatalf("hosted domain must match, got %+v", d)
	}
	// A claim the IdP did not assert cannot be satisfied by an email in the same domain.
	only := []FactorClaim{{Type: "EMAIL", Value: "x@company.com", Attrs: map[string]string{"source": "otp"}}}
	d, _ = EvaluateClientGroup(groups(), only)
	if d.Evidence["rule"] != "r2" {
		t.Fatalf("an OTP email satisfies EMAIL_DOMAIN only, got %v", d.Evidence)
	}
}

func TestClientGroupRulesAreValidatedAndIDPGroupIsReserved(t *testing.T) {
	ok := []struct {
		t string
		v map[string]any
	}{
		{GroupRuleEmailDomain, map[string]any{"domains": []any{"company.com"}}},
		{GroupRuleEmailDomain, map[string]any{"domains": []any{"company.co.uk"}, "include_subdomains": true}},
		{GroupRuleIDPTenant, map[string]any{"provider": "microsoft", "tenant_ids": []any{"11111111-2222-3333-4444-555555555555"}}},
		{GroupRuleIDPHostedDomain, map[string]any{"provider": "google", "domains": []any{"company.com"}}},
	}
	for _, c := range ok {
		if err := ValidateClientGroupRule(c.t, c.v); err != nil {
			t.Errorf("%s %v must be valid: %v", c.t, c.v, err)
		}
	}
	bad := []struct {
		t string
		v map[string]any
	}{
		{GroupRuleEmailDomain, map[string]any{"domains": []any{}}},
		{GroupRuleEmailDomain, map[string]any{"domains": []any{"not a domain"}}},
		{GroupRuleEmailDomain, map[string]any{"domains": []any{"company.com"}, "extra": 1}},
		{GroupRuleIDPTenant, map[string]any{"provider": "google", "tenant_ids": []any{"11111111-2222-3333-4444-555555555555"}}},
		{GroupRuleIDPTenant, map[string]any{"provider": "microsoft", "tenant_ids": []any{"nope"}}},
		{GroupRuleIDPHostedDomain, map[string]any{"provider": "microsoft", "domains": []any{"company.com"}}},
		{"IDP_GROUP", map[string]any{"provider": "microsoft", "groups": []any{"x"}}},
		{"ROOM_NUMBER", map[string]any{}},
	}
	for _, c := range bad {
		if err := ValidateClientGroupRule(c.t, c.v); err == nil {
			t.Errorf("%s %v must be refused", c.t, c.v)
		}
	}
	// A malformed stored rule never matches (fail closed), and does not poison the other rules.
	gs := []ClientGroup{{ID: "g", Name: "G", Priority: 1, Enabled: true, Rules: []ClientGroupRule{
		{ID: "r", Type: GroupRuleEmailDomain, Value: map[string]any{"domains": "company.com"}}}}}
	if _, ok := EvaluateClientGroup(gs, []FactorClaim{{Type: "EMAIL", Value: "a@company.com"}}); ok {
		t.Fatal("a malformed rule must match nobody")
	}
}

func TestClientGroupEligibilityRuleIsAudienceNotMethod(t *testing.T) {
	rules := []EligibilityRule{{Type: RuleClientGroup, Value: map[string]any{"group_ids": []any{"G-1"}}}}
	if ok, _ := EvaluatePackageEligible(rules, EligibilitySubject{ClientGroupID: "g-1", AuthMethod: MethodOTP}); !ok {
		t.Fatal("a member must pass regardless of method (ids compare case-insensitively)")
	}
	if ok, why := EvaluatePackageEligible(rules, EligibilitySubject{AuthMethod: MethodSocial}); ok || why != "client_group_not_allowed" {
		t.Fatalf("Public must be refused by a group-only audience, got %v %q", ok, why)
	}
	pub := []EligibilityRule{{Type: RuleClientGroup, Value: map[string]any{"public": true}}}
	if ok, _ := EvaluatePackageEligible(pub, EligibilitySubject{}); !ok {
		t.Fatal("public-only audience must accept a Client in no group")
	}
	if ok, _ := EvaluatePackageEligible(pub, EligibilitySubject{ClientGroupID: "g-1"}); ok {
		t.Fatal("public-only audience must refuse a group member")
	}
	both := []EligibilityRule{{Type: RuleClientGroup, Value: map[string]any{"group_ids": []any{"g-1"}, "public": true}}}
	if ok, _ := EvaluatePackageEligible(both, EligibilitySubject{ClientGroupID: "g-2"}); ok {
		t.Fatal("a member of another group is neither listed nor public")
	}
	empty := []EligibilityRule{{Type: RuleClientGroup, Value: map[string]any{}}}
	if ok, _ := EvaluatePackageEligible(empty, EligibilitySubject{}); ok {
		t.Fatal("an empty audience rule fails closed")
	}
	// Publication-time validation.
	if err := ValidateEligibilityRule(EligibilityRule{Type: RuleClientGroup, Value: map[string]any{"group_ids": []any{"11111111-2222-3333-4444-555555555555"}}}); err != nil {
		t.Fatalf("valid audience refused: %v", err)
	}
	if err := ValidateEligibilityRule(EligibilityRule{Type: RuleClientGroup, Value: map[string]any{"public": true}}); err != nil {
		t.Fatalf("public audience refused: %v", err)
	}
	if err := ValidateEligibilityRule(EligibilityRule{Type: RuleClientGroup, Value: map[string]any{"group_ids": []any{"nope"}}}); err == nil {
		t.Fatal("a non-uuid group id must be refused")
	}
	if err := ValidateEligibilityRule(EligibilityRule{Type: RuleClientGroup, Value: map[string]any{}}); err == nil {
		t.Fatal("an audience with no group and not public must be refused")
	}
}

func TestFreeAllowancePolicyReadsTheRuleAndDefaultsSafely(t *testing.T) {
	// No rule: no policy (history is not consulted).
	if p := PriorPurchasePolicyOf(nil, 0); p.Present {
		t.Fatal("no rule means no policy")
	}
	// Free package, bare rule: once ever, and the device counts.
	p := PriorPurchasePolicyOf([]EligibilityRule{{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true}}}, 0)
	if !p.Present || p.WithinHours != 0 || !p.AlsoByDevice {
		t.Fatalf("free default must be once-ever with the device counted, got %+v", p)
	}
	// Priced package, bare rule: the device does not count by default (history is the Client's).
	p = PriorPurchasePolicyOf([]EligibilityRule{{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true}}}, 500)
	if p.AlsoByDevice {
		t.Fatalf("a priced package must not count the device by default, got %+v", p)
	}
	// Recurring allowance and an explicit device switch.
	p = PriorPurchasePolicyOf([]EligibilityRule{{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true, "within_hours": float64(24), "also_by_device": false}}}, 0)
	if p.WithinHours != 24 || p.AlsoByDevice {
		t.Fatalf("explicit fields must be honoured, got %+v", p)
	}
	// Validation bounds.
	if err := ValidateEligibilityRule(EligibilityRule{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true, "within_hours": float64(24)}}); err != nil {
		t.Fatalf("24h must be valid: %v", err)
	}
	for _, h := range []any{float64(0), float64(9000), "soon", float64(1.5)} {
		if err := ValidateEligibilityRule(EligibilityRule{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true, "within_hours": h}}); err == nil {
			t.Errorf("within_hours %v must be refused", h)
		}
	}
	if err := ValidateEligibilityRule(EligibilityRule{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true, "also_by_device": "yes"}}); err == nil {
		t.Error("also_by_device must be a boolean")
	}
}

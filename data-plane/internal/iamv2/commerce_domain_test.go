package iamv2

import (
	"testing"
	"time"
)

// ---- C1: eligibility + tiers (pure) ----

func TestCommerceConfigFailClosed(t *testing.T) {
	if err := DefaultCommerceConfig().Validate(); err != nil {
		t.Fatalf("all-off must be valid: %v", err)
	}
	if err := (CommerceConfig{PortalEnabled: true}).Validate(); err == nil {
		t.Fatal("portal-on-while-master-off must be rejected")
	}
	// malformed boolean fails closed
	if _, err := LoadCommerceConfigFromEnv(func(k string) string {
		if k == EnvPhase2Master {
			return "yesish"
		}
		return ""
	}); err == nil {
		t.Fatal("malformed master flag must fail")
	}
	c, err := LoadCommerceConfigFromEnv(func(k string) string {
		switch k {
		case EnvPhase2Master, EnvPhase2Portal:
			return "true"
		}
		return ""
	})
	if err != nil || !c.PortalOn() || c.AdminOn() {
		t.Fatalf("master+portal on: %+v %v", c, err)
	}
}

func TestEligibilityTypedRules(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	base := EligibilitySubject{Now: now, AuthMethod: MethodAccount, Kind: SubjectAccount, GuestNetworkID: "GN-1"}

	cases := []struct {
		name  string
		rules []EligibilityRule
		subj  EligibilitySubject
		want  bool
	}{
		{"no rules -> eligible", nil, base, true},
		{"date window in", []EligibilityRule{{RuleDateWindow, map[string]any{"from": "2026-07-01T00:00:00Z", "until": "2026-08-01T00:00:00Z"}}}, base, true},
		{"date window before", []EligibilityRule{{RuleDateWindow, map[string]any{"from": "2026-08-01T00:00:00Z"}}}, base, false},
		{"date window at-until exclusive", []EligibilityRule{{RuleDateWindow, map[string]any{"until": "2026-07-18T12:00:00Z"}}}, base, false},
		{"auth method allowed", []EligibilityRule{{RuleAuthMethod, map[string]any{"methods": []any{"ACCOUNT", "VOUCHER"}}}}, base, true},
		{"auth method not allowed", []EligibilityRule{{RuleAuthMethod, map[string]any{"methods": []any{"VOUCHER"}}}}, base, false},
		{"subject kind allowed", []EligibilityRule{{RuleSubjectKind, map[string]any{"kinds": []any{"ACCOUNT"}}}}, base, true},
		{"site network allowed", []EligibilityRule{{RuleSiteNetwork, map[string]any{"guest_network_ids": []any{"gn-1"}}}}, base, true},
		{"site network denied", []EligibilityRule{{RuleSiteNetwork, map[string]any{"guest_network_ids": []any{"gn-2"}}}}, base, false},
		{"prior required, absent", []EligibilityRule{{RulePriorPurchase, map[string]any{"requires_prior": true}}}, base, false},
		{"prior forbidden, present", []EligibilityRule{{RulePriorPurchase, map[string]any{"forbids_prior": true}}}, EligibilitySubject{Now: now, AuthMethod: MethodAccount, Kind: SubjectAccount, HasPriorPurchaseOfPackage: true}, false},
		{"unknown rule type fails closed", []EligibilityRule{{"WILD", map[string]any{}}}, base, false},
		{"PMS rule capability-disabled fails closed", []EligibilityRule{{"ROOM_TYPE", map[string]any{"types": []any{"SUITE"}}}}, base, false},
		{"AND: one fails -> ineligible", []EligibilityRule{{RuleAuthMethod, map[string]any{"methods": []any{"ACCOUNT"}}}, {RuleSubjectKind, map[string]any{"kinds": []any{"VOUCHER"}}}}, base, false},
	}
	for _, c := range cases {
		got, reason := EvaluatePackageEligible(c.rules, c.subj)
		if got != c.want {
			t.Fatalf("%s: got %v (%s) want %v", c.name, got, reason, c.want)
		}
	}
}

func TestGrantTierFirstMatchDeterministic(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	acct := EligibilitySubject{Now: now, AuthMethod: MethodAccount, Kind: SubjectAccount}
	vouch := EligibilitySubject{Now: now, AuthMethod: MethodVoucher, Kind: SubjectVoucher}

	// tiers given OUT of order; first-match must honor tier_order ascending.
	tiers := []GrantTier{
		{Order: 20, Value: map[string]any{"down_kbps": 5000.0}},                                                                              // unconditional fallback
		{Order: 10, Value: map[string]any{"match": map[string]any{"type": RuleSubjectKind, "kinds": []any{"VOUCHER"}}, "down_kbps": 2000.0}}, // voucher-only
	}
	// account subject -> tier 10 doesn't match (voucher) -> falls to tier 20
	if got, ok := FirstMatchTier(tiers, acct); !ok || got.Order != 20 {
		t.Fatalf("account should match unconditional tier 20, got %+v ok=%v", got, ok)
	}
	// voucher subject -> tier 10 matches first
	if got, ok := FirstMatchTier(tiers, vouch); !ok || got.Order != 10 {
		t.Fatalf("voucher should match tier 10 first, got %+v ok=%v", got, ok)
	}
	// no unconditional + no match -> ok=false
	only := []GrantTier{{Order: 1, Value: map[string]any{"match": map[string]any{"type": RuleSubjectKind, "kinds": []any{"VOUCHER"}}}}}
	if _, ok := FirstMatchTier(only, acct); ok {
		t.Fatal("no matching tier must return ok=false")
	}
	// capability-disabled tier condition never matches
	pms := []GrantTier{{Order: 1, Value: map[string]any{"match": map[string]any{"type": "VIP"}}}, {Order: 2, Value: map[string]any{"down_kbps": 100.0}}}
	if got, ok := FirstMatchTier(pms, acct); !ok || got.Order != 2 {
		t.Fatalf("PMS tier must not match; fallback to 2, got %+v ok=%v", got, ok)
	}
}

// ---- money safety (free-only) ----

func TestIsFreePackage(t *testing.T) {
	ok, _ := IsFreePackage(MoneySpec{PriceMinor: 0, Currency: "USD", CurrencyExponent: 2, SettlementMethods: []string{"NOT_REQUIRED"}})
	if !ok {
		t.Fatal("zero-price NOT_REQUIRED USD must be free")
	}
	bad := []MoneySpec{
		{PriceMinor: 100, Currency: "USD", CurrencyExponent: 2, SettlementMethods: []string{"NOT_REQUIRED"}},          // priced
		{PriceMinor: 0, Currency: "USD", CurrencyExponent: 2, SettlementMethods: []string{"PMS_POSTING"}},             // settled
		{PriceMinor: 0, Currency: "USD", CurrencyExponent: 2, SettlementMethods: []string{"NOT_REQUIRED", "PREPAID"}}, // extra method
		{PriceMinor: 0, Currency: "US", CurrencyExponent: 2, SettlementMethods: []string{"NOT_REQUIRED"}},             // bad currency
		{PriceMinor: 0, Currency: "", CurrencyExponent: 2, SettlementMethods: []string{"NOT_REQUIRED"}},               // missing currency
	}
	for i, m := range bad {
		if ok, _ := IsFreePackage(m); ok {
			t.Fatalf("case %d must NOT be free: %+v", i, m)
		}
	}
}

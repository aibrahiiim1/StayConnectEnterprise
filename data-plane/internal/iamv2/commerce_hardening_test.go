package iamv2

import (
	"encoding/json"
	"testing"
	"time"
)

// item 2: eligibility genuinely fails closed + publication validation.
func TestEligibilityFailClosedEdges(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	s := EligibilitySubject{Now: now, AuthMethod: MethodAccount, Kind: SubjectAccount, GuestNetworkID: "gn-1"}
	deny := func(rt string, v map[string]any) {
		if ok, _ := EvaluatePackageEligible([]EligibilityRule{{rt, v}}, s); ok {
			t.Fatalf("%s %v must be ineligible", rt, v)
		}
	}
	// DATE_WINDOW: no bound, malformed, from>=until
	deny(RuleDateWindow, map[string]any{})
	deny(RuleDateWindow, map[string]any{"from": "not-a-time"})
	deny(RuleDateWindow, map[string]any{"from": "2026-08-01T00:00:00Z", "until": "2026-07-01T00:00:00Z"})
	// PRIOR_PURCHASE: neither, both, wrong type
	deny(RulePriorPurchase, map[string]any{})
	deny(RulePriorPurchase, map[string]any{"requires_prior": true, "forbids_prior": true})
	deny(RulePriorPurchase, map[string]any{"requires_prior": "yes"})
	// SITE_NETWORK: empty, non-string
	deny(RuleSiteNetwork, map[string]any{"guest_network_ids": []any{}})
	// AUTH_METHOD unknown enum
	deny(RuleAuthMethod, map[string]any{"methods": []any{"TELEPATHY"}})
}

func TestPublicationValidation(t *testing.T) {
	good := []EligibilityRule{
		{RuleDateWindow, map[string]any{"from": "2026-07-01T00:00:00Z", "until": "2026-08-01T00:00:00Z"}},
		{RuleAuthMethod, map[string]any{"methods": []any{"account", "VOUCHER"}}},
		{RulePriorPurchase, map[string]any{"forbids_prior": true}},
		{RuleSiteNetwork, map[string]any{"guest_network_ids": []any{"44444444-4444-4444-4444-444444444444"}}},
	}
	for _, r := range good {
		if err := ValidateEligibilityRule(r); err != nil {
			t.Fatalf("valid rule %s rejected: %v", r.Type, err)
		}
	}
	bad := []EligibilityRule{
		{RuleDateWindow, map[string]any{}}, // no bound
		{RuleDateWindow, map[string]any{"from": "2026-08-01T00:00:00Z", "until": "2026-07-01T00:00:00Z"}}, // from>=until
		{RuleDateWindow, map[string]any{"from": "2026-07-01T00:00:00Z", "extra": 1}},                      // unknown field
		{RulePriorPurchase, map[string]any{"requires_prior": true, "forbids_prior": true}},
		{RuleSiteNetwork, map[string]any{"guest_network_ids": []any{"not-a-uuid"}}},
		{"ROOM_TYPE", map[string]any{}}, // PMS capability-disabled
		{"WILD", map[string]any{}},      // unknown type
	}
	for i, r := range bad {
		if err := ValidateEligibilityRule(r); err == nil {
			t.Fatalf("bad rule %d (%s) must be rejected at publication", i, r.Type)
		}
	}
	// tier: match must be object with recognized condition; malformed not unconditional
	if err := ValidateGrantTier(GrantTier{Order: 1, Value: map[string]any{"match": "nope"}}); err == nil {
		t.Fatal("tier with non-object match must be rejected")
	}
	if err := ValidateGrantTier(GrantTier{Order: 1, Value: map[string]any{"match": map[string]any{"type": "VIP"}}}); err == nil {
		t.Fatal("tier match with PMS type must be rejected")
	}
	if err := ValidateGrantTier(GrantTier{Order: 1, Value: map[string]any{"bogus_key": 1}}); err == nil {
		t.Fatal("tier with unknown grant key must be rejected")
	}
}

// item 3: typed grant snapshot validation (integer-only, bounds, enums, disabled accounting).
func TestGrantSnapshotValidation(t *testing.T) {
	plan := PlanRevisionRow{ID: "p", DownKbps: 1000, MaxConcurrentDevices: 2, TimeAccountingMode: "VALIDITY_WINDOW"}
	pkg := PackageRevisionRow{ID: "k"}
	// valid override
	if _, err := BuildGrantSnapshot(GrantTier{Order: 1, Value: map[string]any{"down_kbps": json.Number("2000")}}, plan, pkg); err != nil {
		t.Fatalf("valid integer override rejected: %v", err)
	}
	bad := []map[string]any{
		{"down_kbps": json.Number("5.5")},            // JSON float
		{"down_kbps": json.Number("-1")},             // negative
		{"down_kbps": json.Number("99999999999")},    // out of bounds
		{"max_concurrent_devices": json.Number("0")}, // < 1
		{"time_accounting_mode": "AGGREGATE_ONLINE_TIME"},
		{"device_limit_policy": "NONSENSE"},
		{"unknown_key": json.Number("1")},
	}
	for i, v := range bad {
		if _, err := BuildGrantSnapshot(GrantTier{Order: 1, Value: v}, plan, pkg); err == nil {
			t.Fatalf("grant case %d must be rejected: %v", i, v)
		}
	}
	// canonical round-trip
	g, _ := BuildGrantSnapshot(GrantTier{Order: 3, Value: map[string]any{"down_kbps": json.Number("2000")}}, plan, pkg)
	g.EndMode = "MANUAL_END"
	rt, err := ParseGrantSnapshot(g.Canonical())
	if err != nil || rt.DownKbps != 2000 || rt.GrantTierOrder != 3 || rt.Version != GrantSnapshotVersion {
		t.Fatalf("canonical round-trip: %+v %v", rt, err)
	}
}

// item 4: authoritative ISO-4217 currency + exponent.
func TestCurrencyMetadata(t *testing.T) {
	for _, c := range []struct {
		code string
		exp  int
		ok   bool
	}{
		{"USD", 2, true}, {"usd", 2, true}, {"JPY", 0, true}, {"BHD", 3, true},
		{"USD", 0, false}, {"ZZZ", 2, false}, {"US", 2, false}, {"JPY", 2, false},
	} {
		_, err := ValidateCurrency(c.code, c.exp)
		if (err == nil) != c.ok {
			t.Fatalf("ValidateCurrency(%s,%d) ok=%v want %v", c.code, c.exp, err == nil, c.ok)
		}
	}
}

// item 8: duration policy resolution (supported vs capability-disabled).
func TestDurationPolicy(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if m, w, err := ResolveEndPolicy(map[string]any{"end_mode": "MANUAL_END"}, now); err != nil || m != "MANUAL_END" || w != nil {
		t.Fatalf("MANUAL_END: %s %v %v", m, w, err)
	}
	if m, w, err := ResolveEndPolicy(map[string]any{"end_mode": "VALIDITY_WINDOW", "duration_seconds": json.Number("3600")}, now); err != nil || m != "VALIDITY_WINDOW" || w == nil || !w.Equal(now.Add(time.Hour)) {
		t.Fatalf("VALIDITY_WINDOW: %s %v %v", m, w, err)
	}
	if m, w, err := ResolveEndPolicy(map[string]any{"end_mode": "FIXED_AT", "ends_at": "2026-07-19T00:00:00Z"}, now); err != nil || m != "FIXED_AT" || w == nil {
		t.Fatalf("FIXED_AT: %s %v %v", m, w, err)
	}
	for _, dp := range []map[string]any{
		{},                                   // empty
		{"end_mode": "AT_CHECKOUT"},          // PMS
		{"end_mode": "GRACE_AFTER_CHECKOUT"}, // PMS
		{"end_mode": "REST_OF_STAY"},         // PMS
		{"end_mode": "VALIDITY_WINDOW"},      // missing duration
		{"end_mode": "VALIDITY_WINDOW", "duration_seconds": json.Number("-5")},
		{"end_mode": "FIXED_AT", "ends_at": "2020-01-01T00:00:00Z"}, // past
		{"end_mode": "VALIDITY_WINDOW", "local_end_time": "23:00"},  // local-time boundary disabled
	} {
		if _, _, err := ResolveEndPolicy(dp, now); err == nil {
			t.Fatalf("duration policy %v must be rejected", dp)
		}
	}
}

package iamv2

import (
	"strings"
	"testing"
)

// O-2' / O-14: ONE card-currency rule, asked at listing, quote and confirm.
func TestCardApplicableIsTheOneCurrencyRule(t *testing.T) {
	site := SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{"GBP"}}
	if !site.CardApplicable("gbp") || site.CardApplicable("USD") || site.CardApplicable("") {
		t.Fatal("without conversion only the default account's currency is accepted")
	}
	// A provider-converts default account accepts what its provider converts, and nothing else.
	site.CardAccepts = func(cur string) bool { return cur == "USD" }
	if !site.CardApplicable(" usd ") || site.CardApplicable("XYZ") {
		t.Fatal("a converting account accepts exactly what the provider converts (upper-cased, trimmed)")
	}
}

func TestConfirmReChecksTheCardCurrency(t *testing.T) {
	q := OfferQuoteRow{PriceMinor: 500, Currency: "USD", CurrencyExponent: 2,
		GrantSnapshot: GrantSnapshot{Version: GrantSnapshotVersion, ServicePlanRevisionID: "p"}}
	ok := SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{"USD"}}
	if why := revalidateQuote(q, AcquireCard, ok); why != "" {
		t.Fatalf("a settleable quote must confirm, got %q", why)
	}
	// The default account changed to GBP (no conversion) after the quote: refused BEFORE a purchase exists.
	moved := SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{"GBP"}}
	if why := revalidateQuote(q, AcquireCard, moved); why != "card_currency_not_accepted" {
		t.Fatalf("confirm must re-check the currency, got %q", why)
	}
}

// O-4: a per-night allowance needs a stay; nothing on the non-room path has one.
func TestPerNightPackageIsNeverOfferedWithoutAStay(t *testing.T) {
	site := SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{"USD"}}
	for _, ac := range []AuthContextRow{
		{Subject: CommerceSubject{Kind: SubjectPrincipal, PrincipalID: "p"}},
		{Subject: CommerceSubject{Kind: SubjectAccount, AccountID: "a"}},
		{Subject: CommerceSubject{Kind: SubjectVoucher, VoucherID: "v"}},
		{Subject: CommerceSubject{Kind: SubjectAnonymous, AnonymousID: "x"}},
	} {
		free := PackageRevisionRow{PriceMinor: 0, Currency: "USD", CurrencyExponent: 2,
			SettlementMethods: []string{AcquireFree, AcquireVoucher}, DataAllocationMode: AllocPerStayNight}
		if m := applicableMethods(free, ac, site); len(m) != 0 {
			t.Errorf("%s: a per-night package was offered by %v", ac.Subject.Kind, m)
		}
		free.DataAllocationMode = AllocFixed
		if m := applicableMethods(free, ac, site); len(m) == 0 {
			t.Errorf("%s: the same package on a flat allowance must still be offered", ac.Subject.Kind)
		}
	}
}

// O-10: each voucher is its own allowance; the device-counted free limit binds everyone else.
func TestVoucherIsItsOwnAllowance(t *testing.T) {
	rules := []EligibilityRule{{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true}}}
	pol := PriorPurchasePolicyOf(rules, 0)
	if !pol.AlsoByDevice {
		t.Fatal("a free package's limit counts the device by default (contract section 5)")
	}
	if pol.ForSubject(SubjectVoucher).AlsoByDevice {
		t.Fatal("a voucher redemption must not be refused because the same phone redeemed another voucher")
	}
	for _, k := range []SubjectKind{SubjectPrincipal, SubjectAccount, SubjectAnonymous} {
		if !pol.ForSubject(k).AlsoByDevice {
			t.Fatalf("%s: the device limit must still apply", k)
		}
	}
}

// O-26 / O-4: a voucher that could never be redeemed is not issued.
func TestVoucherBlocker(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		rules []EligibilityRule
		block bool
	}{
		{"plain", AllocFixed, nil, false},
		{"per night", AllocPerStayNight, nil, true},
		{"group only", AllocFixed, []EligibilityRule{{Type: RuleClientGroup, Value: map[string]any{"group_ids": []any{"g"}}}}, true},
		{"group and public", AllocFixed, []EligibilityRule{{Type: RuleClientGroup, Value: map[string]any{"group_ids": []any{"g"}, "public": true}}}, false},
		{"hotel condition", AllocFixed, []EligibilityRule{{Type: RuleVIP, Value: map[string]any{"is_vip": true}}}, true},
		{"method excludes voucher", AllocFixed, []EligibilityRule{{Type: RuleAuthMethod, Value: map[string]any{"methods": []any{"OTP"}}}}, true},
		{"method includes voucher", AllocFixed, []EligibilityRule{{Type: RuleAuthMethod, Value: map[string]any{"methods": []any{"voucher"}}}}, false},
		{"kind excludes voucher", AllocFixed, []EligibilityRule{{Type: RuleSubjectKind, Value: map[string]any{"kinds": []any{"ACCOUNT"}}}}, true},
		{"free limit", AllocFixed, []EligibilityRule{{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true}}}, false},
	}
	for _, c := range cases {
		if got := VoucherBlocker(c.mode, c.rules) != ""; got != c.block {
			t.Errorf("%s: blocked=%v, want %v", c.name, got, c.block)
		}
	}
}

// O-19: room guests are their own kind, and a rule may name the room sign-in method.
func TestRuleVocabularyNamesRoomGuests(t *testing.T) {
	if err := ValidateEligibilityRule(EligibilityRule{Type: RuleAuthMethod, Value: map[string]any{"methods": []any{"PMS"}}}); err != nil {
		t.Fatalf("AUTH_METHOD PMS must be publishable: %v", err)
	}
	if err := ValidateEligibilityRule(EligibilityRule{Type: RuleSubjectKind, Value: map[string]any{"kinds": []any{"STAY"}}}); err != nil {
		t.Fatalf("SUBJECT_KIND STAY must be publishable: %v", err)
	}
	// SUBJECT_KIND PRINCIPAL no longer matches a room guest.
	room := EligibilitySubject{Kind: SubjectStay, AuthMethod: Method("PMS")}
	if ok, _ := EvaluatePackageEligible([]EligibilityRule{{Type: RuleSubjectKind, Value: map[string]any{"kinds": []any{"PRINCIPAL"}}}}, room); ok {
		t.Fatal("a PRINCIPAL-only rule matched a room guest")
	}
	err := ValidateEligibilityRule(EligibilityRule{Type: RulePMSInterface, Value: map[string]any{"pms_interface_ids": []any{"fias-main"}}})
	if err == nil || !strings.Contains(err.Error(), "PMS connection ids") {
		t.Fatalf("PMS_INTERFACE must take ids, got %v", err)
	}
}

func TestContradictoryPriorPurchase(t *testing.T) {
	both := []EligibilityRule{
		{Type: RulePriorPurchase, Value: map[string]any{"forbids_prior": true}},
		{Type: RulePriorPurchase, Value: map[string]any{"requires_prior": true}},
	}
	if contradictoryPriorPurchase(both) == "" {
		t.Fatal("free access once + requires an earlier purchase must be refused")
	}
	if contradictoryPriorPurchase(both[:1]) != "" || contradictoryPriorPurchase(both[1:]) != "" {
		t.Fatal("either rule alone is fine")
	}
}

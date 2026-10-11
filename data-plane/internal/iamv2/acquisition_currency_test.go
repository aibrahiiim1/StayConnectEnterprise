package iamv2

import "testing"

// A PACKAGE IS OFFERED BY CARD ONLY IN A CURRENCY AN ACTIVE ACCOUNT SETTLES. Found live on PRE-LIVE
// (2026-10-11): a GBP Stripe test account and a USD package -- Card was listed, chosen, and refused at confirm
// with PAYMENT_ACCOUNT_CURRENCY. The refusal was right; the offer was the defect.
func TestCardIsOfferedOnlyInASettleableCurrency(t *testing.T) {
	pkg := PackageRevisionRow{PriceMinor: 100, Currency: "USD", CurrencyExponent: 2, SettlementMethods: []string{AcquireVoucher, AcquireCard, AcquireRoomCharge}}
	ac := AuthContextRow{Subject: CommerceSubject{Kind: SubjectPrincipal, PrincipalID: "p1"}}
	for name, tc := range map[string]struct {
		site SiteMethods
		card bool
	}{
		"account settles the package's currency": {SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{"USD"}}, true},
		"case and spacing do not matter":         {SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{" usd "}}, true},
		"account settles another currency":       {SiteMethods{PaidAccess: true, Card: true, CardCurrencies: []string{"GBP"}}, false},
		"no account currency known":              {SiteMethods{PaidAccess: true, Card: true}, false},
		"card not effective":                     {SiteMethods{PaidAccess: true, Card: false, CardCurrencies: []string{"USD"}}, false},
	} {
		got := containsMethod(applicableMethods(pkg, ac, tc.site), AcquireCard)
		if got != tc.card {
			t.Errorf("%s: card offered=%v, want %v", name, got, tc.card)
		}
	}
	// A free package is unaffected: Free never needs an account.
	free := PackageRevisionRow{PriceMinor: 0, Currency: "USD", CurrencyExponent: 2, SettlementMethods: []string{AcquireFree}}
	if !containsMethod(applicableMethods(free, ac, SiteMethods{}), AcquireFree) {
		t.Fatal("a free package is offered without any payment account")
	}
}

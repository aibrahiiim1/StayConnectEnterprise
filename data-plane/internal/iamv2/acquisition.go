package iamv2

// INTERNET PACKAGE ACQUISITION.
//
// A package revision says how it may be acquired, in settlement_methods (migration 0095 constrains it):
//
//	NOT_REQUIRED   Free           price 0; no settlement
//	PREPAID        Voucher        a valid voucher backed by this revision, burned in the grant
//	ONLINE_PAYMENT Card payment   a provider-verified capture
//	PMS_POSTING    Room charge    an authoritative PMS PA=OK
//
// What a CLIENT is offered is the intersection of: the method being effective at this site (the four-gate
// module resolver, supplied to the engine as a MethodGate), the package listing the method, and the method
// being applicable to this client and package right now. Every gate is re-checked when the quote is created
// and again when it is confirmed; nothing the page showed earlier is trusted.

import (
	"context"
	"fmt"
	"strings"
)

// Acquisition methods, as stored in settlement_methods and settlements.method.
const (
	AcquireFree       = "NOT_REQUIRED"
	AcquireVoucher    = "PREPAID"
	AcquireCard       = "ONLINE_PAYMENT"
	AcquireRoomCharge = "PMS_POSTING"
)

// AcquisitionMethods is the stable order methods are presented in.
var AcquisitionMethods = []string{AcquireFree, AcquireVoucher, AcquireCard, AcquireRoomCharge}

// SubjectAnonymous is the subject of open package selection: a server-generated anonymous access subject.
const SubjectAnonymous SubjectKind = "ANONYMOUS"

// MethodOpen is the auth-context method of open package selection.
const MethodOpen Method = "OPEN"

// ValidateAcquisitionMethods is the domain form of the ipr_acquisition_methods CHECK: the same rule, with
// operator-readable messages. It is checked before publishing and again by the database.
func ValidateAcquisitionMethods(priceMinor int64, methods []string) error {
	if priceMinor < 0 {
		return fmt.Errorf("price cannot be negative")
	}
	if len(methods) == 0 {
		return fmt.Errorf("choose at least one way clients can acquire this package")
	}
	seen := map[string]bool{}
	for _, m := range methods {
		switch m {
		case AcquireFree, AcquireVoucher, AcquireCard, AcquireRoomCharge:
		default:
			return fmt.Errorf("unknown acquisition method %q", m)
		}
		if seen[m] {
			return fmt.Errorf("acquisition method %q listed twice", m)
		}
		seen[m] = true
	}
	if priceMinor == 0 && (seen[AcquireCard] || seen[AcquireRoomCharge]) {
		return fmt.Errorf("a free package is acquired as Free or by Voucher; card payment and room charge need a price")
	}
	if priceMinor > 0 && seen[AcquireFree] {
		return fmt.Errorf("a package with a price cannot also be free")
	}
	return nil
}

// SiteMethods is what the module resolver says is effective at this site right now (all four gates,
// including readiness). Free and Voucher are core and always available.
type SiteMethods struct {
	PaidAccess bool
	Card       bool
	RoomCharge bool
	// CardCurrencies holds the settlement currency of the site's DEFAULT active payment account -- the one a
	// checkout uses (p4_resolve_payment_account_v2). The client always pays in the package currency
	// (presentment); unless the account converts (below), a package in another currency would be chosen and
	// then refused by the payment identity gate (PAYMENT_ACCOUNT_CURRENCY) a step later -- what the Product
	// Owner hit on PRE-LIVE with a GBP test account and a USD package. Empty means "unknown", which offers
	// nothing by card.
	CardCurrencies []string
	// CardAccepts, when set, answers for a presentment currency the provider converts from: the default
	// account is marked provider-converts and its provider converts (migration 0112; approved policy
	// 2026-10-11 -- a provider-converts Stripe account settles in its own currency at the provider's rate).
	// nil = no conversion: only CardCurrencies.
	CardAccepts func(currency string) bool
}

// CardApplicable is THE card-currency applicability rule. A package priced in cur may be offered, quoted,
// chosen and confirmed by card only when the default account settles cur, or its provider converts cur.
// Every path asks this one function -- the non-room listing and quote (applicableMethods), the confirm
// (revalidateQuote) and the Room sign-in offer and choice (scd stayMethods) -- so no path offers what the
// payment gate would refuse after the purchase had been written (contract ONEGATE_MODULES_AND_ACQUISITION.md:
// every gate is re-checked when the quote is created and when it is confirmed).
func (s SiteMethods) CardApplicable(cur string) bool {
	cur = strings.ToUpper(strings.TrimSpace(cur))
	if cur == "" {
		return false
	}
	if s.CardAccepts != nil && s.CardAccepts(cur) {
		return true
	}
	for _, c := range s.CardCurrencies {
		if strings.EqualFold(strings.TrimSpace(c), cur) {
			return true
		}
	}
	return false
}

// MethodGate reports the site's effective acquisition modules. Nil means only the core (Free, Voucher).
type MethodGate func(ctx context.Context) SiteMethods

// WithMethodGate installs the site-effective method gate (scd wires it to the module resolver).
func WithMethodGate(g MethodGate) CommerceOption { return func(e *CommerceEngine) { e.methodGate = g } }

func (e *CommerceEngine) siteMethods(ctx context.Context) SiteMethods {
	if e.methodGate == nil {
		return SiteMethods{}
	}
	return e.methodGate(ctx)
}

func hasMethod(methods []string, m string) bool {
	for _, x := range methods {
		if strings.EqualFold(strings.TrimSpace(x), m) {
			return true
		}
	}
	return false
}

// applicableMethods is what a NON-PMS client may use to acquire pkg right now. Room charge is never
// applicable here: it needs a verified stay, which only the Room sign-in path carries. A voucher subject is
// offered only its own printed revision, by Voucher.
func applicableMethods(pkg PackageRevisionRow, ac AuthContextRow, site SiteMethods) []string {
	var out []string
	// A PER-NIGHT DATA ALLOWANCE NEEDS A STAY. The allowance is computed from the stay's nights at grant time
	// (staygrant). No client on this path carries a stay -- CreateQuote refuses a stay-bound context -- so such
	// a package is neither offered nor quoted here, not even to a voucher (whose issue is refused for it as
	// well). Granting it would hand out the plan's flat quota, or unlimited data, under a package sold per night.
	if pkg.DataAllocationMode == AllocPerStayNight {
		return nil
	}
	if ac.Subject.Kind == SubjectVoucher {
		return []string{AcquireVoucher}
	}
	if _, err := ValidateCurrency(pkg.Currency, pkg.CurrencyExponent); err != nil {
		return nil
	}
	if pkg.PriceMinor == 0 {
		if hasMethod(pkg.SettlementMethods, AcquireFree) {
			out = append(out, AcquireFree)
		}
		return out
	}
	if !site.PaidAccess {
		return nil
	}
	if site.Card && hasMethod(pkg.SettlementMethods, AcquireCard) && site.CardApplicable(pkg.Currency) {
		out = append(out, AcquireCard)
	}
	return out
}

func containsMethod(ms []string, m string) bool {
	for _, x := range ms {
		if x == m {
			return true
		}
	}
	return false
}

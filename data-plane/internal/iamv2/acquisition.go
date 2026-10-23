package iamv2

// Acquisition methods per package revision (settlement_methods):
//
//	NOT_REQUIRED   Free           price 0; no settlement
//	PREPAID        Voucher        a valid voucher backed by this revision, burned in the grant
//	ONLINE_PAYMENT Card payment   a provider-verified capture
//	PMS_POSTING    Room charge    an authoritative PMS PA=OK
//
// Every gate is re-checked at quote creation and again at confirm; nothing the page showed earlier is trusted.

import (
	"context"
	"fmt"
	"strings"
)

const (
	AcquireFree       = "NOT_REQUIRED"
	AcquireVoucher    = "PREPAID"
	AcquireCard       = "ONLINE_PAYMENT"
	AcquireRoomCharge = "PMS_POSTING"
)

var AcquisitionMethods = []string{AcquireFree, AcquireVoucher, AcquireCard, AcquireRoomCharge}

const SubjectAnonymous SubjectKind = "ANONYMOUS"

const MethodOpen Method = "OPEN"

// ValidateAcquisitionMethods mirrors the ipr_acquisition_methods CHECK with operator-readable messages.
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

// SiteMethods is what the module resolver says is effective at this site now. Free and Voucher are core and
// always available.
type SiteMethods struct {
	PaidAccess bool
	Card       bool
	RoomCharge bool
	// CardCurrencies holds the settlement currency of the DEFAULT active payment account. Empty means
	// unknown, which offers nothing by card; a package in another currency would otherwise be refused by the
	// payment identity gate only after being chosen.
	CardCurrencies []string
	// CardAccepts, when set, answers for presentment currencies the provider converts from. nil means no
	// conversion: only CardCurrencies.
	CardAccepts func(currency string) bool
}

// CardApplicable is the single card-currency rule (listing, quote, confirm and Room sign-in all use it): a
// package may be offered by card only when the default account settles cur or its provider converts cur.
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

// applicableMethods is what a non-PMS client may use to acquire pkg. Room charge is never applicable here
// (it needs a verified stay); a voucher subject is offered only Voucher.
func applicableMethods(pkg PackageRevisionRow, ac AuthContextRow, site SiteMethods) []string {
	var out []string
	// A per-night allowance needs a stay, which no client on this path carries; offering it would grant the
	// plan's flat quota or unlimited data under a package sold per night.
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

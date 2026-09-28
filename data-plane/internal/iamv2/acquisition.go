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
	if site.Card && hasMethod(pkg.SettlementMethods, AcquireCard) {
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

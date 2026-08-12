package posting

import "fmt"

// maxWireField bounds every value that reaches the wire. FIAS records are line-oriented and a PMS that
// truncates silently would post a charge against a room nobody meant. 32 matches the DB constraint 0011
// puts on rn and g_number.
const maxWireField = 32

// Gate is the fail-closed financial creation gate.
//
// It runs BEFORE any side effect whatsoever: before a posting row exists, before an outbox row exists,
// before a P# is allocated and before a single byte could reach a PMS. That ordering is the entire
// guarantee — a refusal here is not "the transmission failed", it is "the transmission never began", so
// there is nothing to reconcile, nothing to reverse and no protocol reference burned.
//
// Everything it checks is ALSO enforced in the database by migration 0011 and mg9. That duplication is
// intentional and is not belt-and-braces for its own sake: the database refusal is what makes the rule
// true for every writer forever, and this refusal is what makes it true BEFORE the side effects that the
// database cannot see (P# allocation and transmission) are reached.
type Gate struct{}

// Check validates pinned evidence against the snapshot read from the database. It returns the FIRST
// violation, ordered so the reason an operator sees is the most actionable one: onboarding before
// targeting, targeting before money.
func (Gate) Check(p Pinned, s Snapshot) error {
	// ---- interface state -------------------------------------------------------------------------
	if s.InterfaceLifecycleState != "ACTIVE" {
		return fail(ErrInterfaceInactive,
			fmt.Sprintf("pms interface lifecycle_state is %s", s.InterfaceLifecycleState))
	}

	// ---- Tier-2 onboarding: folio identity, then financial currency -------------------------------
	if s.FolioIdentityStrategy == "" || s.FolioIdentityStrategy == "UNSET" {
		return fail(ErrFolioStrategyUnset,
			"pinned interface revision has no folio identity strategy (property not onboarded)")
	}
	if s.InterfaceCurrency == "" || s.InterfaceExponent == nil {
		return fail(ErrInterfaceNoCurrency,
			"pinned interface revision has no financial base currency (property not financially onboarded)")
	}

	// ---- stale evidence --------------------------------------------------------------------------
	// The posting must be authorized by the revision that is CURRENT for the interface. Charging against a
	// superseded revision means charging under configuration the property has already replaced.
	if s.InterfaceCurrentRevision != "" && s.InterfaceCurrentRevision != p.PostingInterfaceRevisionID {
		return fail(ErrEvidenceStale,
			"pinned posting interface revision is not the interface's current revision")
	}
	if s.SettlementMappingRetired {
		return fail(ErrEvidenceStale, "pinned settlement mapping is retired")
	}
	if p.ExpectStayLifecycleVersion != nil && *p.ExpectStayLifecycleVersion != s.StayLifecycleVersion {
		return fail(ErrEvidenceStale,
			fmt.Sprintf("stay lifecycle version moved (expected %d, found %d)",
				*p.ExpectStayLifecycleVersion, s.StayLifecycleVersion))
	}
	if p.ExpectPurchaseState != "" && p.ExpectPurchaseState != s.PurchaseState {
		return fail(ErrEvidenceStale,
			fmt.Sprintf("purchase state moved (expected %s, found %s)", p.ExpectPurchaseState, s.PurchaseState))
	}

	// ---- stay eligibility ------------------------------------------------------------------------
	if s.StayStatus != "IN_HOUSE" {
		return fail(ErrStayNotInHouse, fmt.Sprintf("pinned stay status is %s", s.StayStatus))
	}
	if !s.StayPostingAllowed {
		return fail(ErrPostingNotAllowed, "pinned stay does not allow posting")
	}

	// ---- financial targeting evidence ------------------------------------------------------------
	if blank(p.RN) {
		return fail(ErrRNMissing, "no verified room number")
	}
	if blank(p.GNumber) {
		return fail(ErrGNumberMissing, "no verified guest number")
	}
	if wireUnsafe(p.RN) || len(p.RN) > maxWireField {
		return fail(ErrRNNotWireSafe, "room number is not transmissible as a FIAS field")
	}
	if wireUnsafe(p.GNumber) || len(p.GNumber) > maxWireField {
		return fail(ErrGNumberNotWireSafe, "guest number is not transmissible as a FIAS field")
	}
	// VERIFIED, not merely present: the values must be the ones the PINNED stay and folio actually carry.
	// This is what stops a stale RN from being transmitted after a room move, and it is re-run before every
	// attempt, so a change between authorization and transmission refuses instead of charging a stranger.
	if p.RN != s.StayRoomNumber {
		return fail(ErrEvidenceStale, "room number does not match the pinned stay's current room")
	}
	if p.GNumber != s.FolioExternalID {
		return fail(ErrEvidenceStale, "guest number does not match the pinned folio's external identifier")
	}
	if blank(p.PostingCode) || wireUnsafe(p.PostingCode) || len(p.PostingCode) > maxWireField {
		return fail(ErrWireFieldInvalid, "pinned posting code is not transmissible as a FIAS field")
	}

	// ---- money -----------------------------------------------------------------------------------
	if p.AmountMinor <= 0 {
		return fail(ErrAmountInvalid, "a charge must be a positive integer amount in minor units")
	}
	if blank(p.Currency) {
		return fail(ErrCurrencyMismatch, "posting states no currency")
	}
	// Exact equality, three ways, exponent included. Not "convertible to", not "compatible with". If any of
	// these differ the correct behaviour is to refuse, because the only alternative is to pick a rate — and
	// picking a rate is exactly the implicit FX this system is not allowed to perform.
	if p.Currency != s.InterfaceCurrency {
		return fail(ErrCurrencyMismatch,
			fmt.Sprintf("posting currency %s <> pinned interface currency %s (no implicit FX)",
				p.Currency, s.InterfaceCurrency))
	}
	if p.CurrencyExponent != *s.InterfaceExponent {
		return fail(ErrExponentMismatch,
			fmt.Sprintf("posting exponent %d <> pinned interface exponent %d",
				p.CurrencyExponent, *s.InterfaceExponent))
	}
	if err := matchCurrency("purchase", s.PurchaseCurrency, s.PurchaseExponent, s); err != nil {
		return err
	}
	if err := matchCurrency("package", s.PackageCurrency, s.PackageExponent, s); err != nil {
		return err
	}
	return nil
}

func matchCurrency(what, cur string, exp *int16, s Snapshot) error {
	if cur == "" || exp == nil {
		return fail(ErrCurrencyMismatch, what+" states no currency/exponent")
	}
	if cur != s.InterfaceCurrency {
		return fail(ErrCurrencyMismatch,
			fmt.Sprintf("%s currency %s <> pinned interface currency %s (no implicit FX)",
				what, cur, s.InterfaceCurrency))
	}
	if *exp != *s.InterfaceExponent {
		return fail(ErrExponentMismatch,
			fmt.Sprintf("%s exponent %d <> pinned interface exponent %d", what, *exp, *s.InterfaceExponent))
	}
	return nil
}

package posting

import "fmt"

// maxWireField bounds every value sent to the PMS; silent truncation would charge the wrong room.
// Matches the DB constraint on rn and g_number.
const maxWireField = 32

// Gate is the fail-closed financial creation gate. It runs before any side effect (posting row, outbox
// row, P# allocation, bytes on the wire), so a refusal means nothing began and nothing needs reconciling.
// The same rules are also enforced in the database, which covers every writer; this check covers the
// side effects the database cannot see.
type Gate struct{}

// Purpose separates "may new financial work be created?" from "may already-authorized work drain?";
// the lifecycle rules differ (a DRAINING interface must still drain).
type Purpose int

const (
	PurposeCreate Purpose = iota
	PurposeExecute
)

// lifecycleAllows: ACTIVE and AUTH_DISABLED (disables guest auth only) allow create and drain;
// DRAINING allows drain only; DECOMMISSIONED allows neither.
func lifecycleAllows(state string, p Purpose) error {
	switch state {
	case "ACTIVE", "AUTH_DISABLED":
		return nil
	case "DRAINING":
		if p == PurposeExecute {
			return nil
		}
		return fail(ErrInterfaceInactive, "interface is DRAINING; no new financial work may be created")
	case "DECOMMISSIONED":
		return fail(ErrInterfaceDecomm, "interface is DECOMMISSIONED")
	default:
		return fail(ErrInterfaceInactive, "interface lifecycle_state is "+state)
	}
}

// Check returns the first violation, ordered so the reason shown is the most actionable:
// onboarding before targeting, targeting before money.
func (g Gate) Check(p Pinned, s Snapshot) error { return g.CheckFor(PurposeCreate, p, s) }

// CheckFor differs between create and execute only in the lifecycle rule, so a posting cannot be
// created under one rule set and transmitted under a weaker one.
func (Gate) CheckFor(purpose Purpose, p Pinned, s Snapshot) error {
	if err := lifecycleAllows(s.InterfaceLifecycleState, purpose); err != nil {
		return err
	}

	if s.PostingTargetModel != "RESERVATION" {
		return fail(ErrPostingTargetUnset,
			"pinned interface revision has no posting target (property not financially onboarded)")
	}
	if s.InterfaceCurrency == "" || s.InterfaceExponent == nil {
		return fail(ErrInterfaceNoCurrency,
			"pinned interface revision has no financial base currency (property not financially onboarded)")
	}

	// Re-checked at execution so an interface that went stale after authorization stops the bytes.
	if s.FreshnessBlock != "" {
		return fail(ErrInterfaceNotFresh, "PMS runtime axis: "+s.FreshnessBlock)
	}

	// The posting must be authorized by the interface's current revision, not a superseded configuration.
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

	if s.StayStatus != "IN_HOUSE" {
		return fail(ErrStayNotInHouse, fmt.Sprintf("pinned stay status is %s", s.StayStatus))
	}
	if !s.StayPostingAllowed {
		return fail(ErrPostingNotAllowed, "pinned stay does not allow posting")
	}

	// G# is the posting identity: mandatory, wire-safe, and the pinned stay's reservation number, so a
	// room-only posting can never be built.
	if blank(p.GNumber) {
		return fail(ErrGNumberMissing, "no reservation number (G#)")
	}
	if wireUnsafe(p.GNumber) || len(p.GNumber) > maxWireField {
		return fail(ErrGNumberNotWireSafe, "reservation number is not transmissible as a FIAS field")
	}
	if p.GNumber != s.StayReservation {
		return fail(ErrReservationMismatch, "the posting's reservation is not the pinned stay's reservation")
	}
	// RN is the reservation's current room; at execution the attempt's RN must equal it.
	if blank(s.StayRoomNumber) {
		return fail(ErrRNMissing, "the reservation's current room cannot be resolved")
	}
	if wireUnsafe(s.StayRoomNumber) || len(s.StayRoomNumber) > maxWireField {
		return fail(ErrRNNotWireSafe, "room number is not transmissible as a FIAS field")
	}
	if purpose == PurposeExecute && p.RN != s.StayRoomNumber {
		return fail(ErrEvidenceStale, "the attempt's room is not the reservation's current room")
	}
	if blank(p.PostingCode) || wireUnsafe(p.PostingCode) || len(p.PostingCode) > maxWireField {
		return fail(ErrWireFieldInvalid, "pinned posting code is not transmissible as a FIAS field")
	}

	if p.AmountMinor <= 0 {
		return fail(ErrAmountInvalid, "a charge must be a positive integer amount in minor units")
	}
	if blank(p.Currency) {
		return fail(ErrCurrencyMismatch, "posting states no currency")
	}
	// Exact currency and exponent equality; any difference is refused because the alternative is implicit FX.
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
	// The wire bound is narrower than the currency model: the connector decides what it can carry.
	if s.ConnectorKind == "protel-fias" && p.CurrencyExponent != FIASCurrencyExponent {
		return fail(ErrFIASExponent,
			fmt.Sprintf("the protel-fias posting path is exponent %d by contract; posting exponent is %d",
				FIASCurrencyExponent, p.CurrencyExponent))
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

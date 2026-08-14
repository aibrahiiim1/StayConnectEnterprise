package iamv2

// The grant path for a purchase that was settled by MONEY rather than by being free.
//
// THIS IS NOT A SECOND ENTITLEMENT WRITER. It is a second ENTRY POINT into the one that already exists.
// The three statements that actually create access — TerminateLiveEntitlementForSubject, InsertEntitlement,
// MarkPurchaseGranted — are the same CommerceTx methods ConfirmFreePurchase calls, in the same order, under
// the same subject lock, inside the same WithTx. Nothing here writes an entitlement itself, and if the grant
// semantics ever change they change in one place for both paths.
//
// The difference between the two entry points is only WHERE the authorization comes from:
//
//	ConfirmFreePurchase   the quote is free, so consuming the quote and the auth context IS the
//	                      authorization, and the grant happens in the same breath as the purchase
//	GrantSettledPurchase  the money moved first. The purchase and its settlement already exist, and the
//	                      authorization is the durable fact that the settlement reached SETTLED
//
// EXACTLY-ONCE is a property of the database, not of this function's control flow. A purchase already
// GRANTED returns its existing entitlement without writing anything, and the whole body runs under the
// subject advisory lock, so a duplicate callback, a concurrent callback and a replay after restart all
// converge on one entitlement rather than racing to create a second.

import "context"

// SettledPurchaseGrant is the pinned evidence a settled purchase carries. Every field is read from durable
// rows the purchase already points at; none of it is supplied by a caller.
type SettledPurchaseGrant struct {
	PurchaseID        string
	PurchaseState     string
	SettlementStatus  string
	SettlementMethod  string
	PackageRevisionID string
	Subject           CommerceSubject
	GrantSnapshot     GrantSnapshot
	// ExistingEntitlementID is non-empty when this purchase has already granted. Its presence is what makes
	// a replay a no-op rather than a second grant.
	ExistingEntitlementID string
}

// SettledGrantResult reports what happened, so a caller can distinguish a first grant from a replay
// without inferring it.
type SettledGrantResult struct {
	EntitlementID  string
	Superseded     string
	AlreadyGranted bool
}

// GrantSettledPurchase is RETIRED as an implementation and kept only as a signpost.
//
// The paid grant moved into iam_v2.p4_grant_paid_entitlement (migration 0021) because of what the previous
// arrangement implied about privilege: performing the grant from Go required the caller to hold EXECUTE on
// p4_insert_entitlement, which takes thirteen caller-supplied parameters -- subject, package revision,
// policy snapshot, window. A restricted runtime holding that primitive could fabricate an entitlement for
// anyone, from evidence it invented. Moving the whole operation behind one definer function that
// re-resolves its own evidence is what removes that.
//
// There is still exactly ONE paid-grant implementation; it is now SQL rather than Go, and both the owner
// and the restricted runtime reach it the same way. The FREE path is unchanged and still lives in
// ConfirmFreePurchase, as it always did.
func (e *CommerceEngine) GrantSettledPurchase(ctx context.Context, tenantID, siteID, purchaseID string) (SettledGrantResult, error) {
	return SettledGrantResult{}, &Error{Code: ErrNotRedeemable,
		Msg: "the paid grant is performed by iam_v2.p4_grant_paid_entitlement; call it with the SETTLEMENT " +
			"rather than the purchase so the operation can re-resolve its own evidence"}
}

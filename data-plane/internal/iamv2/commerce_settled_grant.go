package iamv2

import "context"

// SettledPurchaseGrant is the pinned evidence a settled purchase carries, read from durable rows and never
// supplied by a caller.
type SettledPurchaseGrant struct {
	PurchaseID        string
	PurchaseState     string
	SettlementStatus  string
	SettlementMethod  string
	PackageRevisionID string
	Subject           CommerceSubject
	GrantSnapshot     GrantSnapshot
	// ExistingEntitlementID is non-empty when already granted, which makes a replay a no-op.
	ExistingEntitlementID string
}

type SettledGrantResult struct {
	EntitlementID  string
	Superseded     string
	AlreadyGranted bool
}

// GrantSettledPurchase is a signpost only. The paid grant lives behind iam_v2.p4_grant_paid_entitlement, which
// re-resolves its own evidence: doing it from Go would need EXECUTE on p4_insert_entitlement (caller-supplied
// parameters), letting a restricted runtime fabricate an entitlement for anyone.
func (e *CommerceEngine) GrantSettledPurchase(ctx context.Context, tenantID, siteID, purchaseID string) (SettledGrantResult, error) {
	return SettledGrantResult{}, &Error{Code: ErrNotRedeemable,
		Msg: "the paid grant is performed by iam_v2.p4_grant_paid_entitlement; call it with the SETTLEMENT " +
			"rather than the purchase so the operation can re-resolve its own evidence"}
}

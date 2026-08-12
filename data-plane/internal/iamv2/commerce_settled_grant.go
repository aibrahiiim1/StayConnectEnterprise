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

import (
	"context"
	"time"
)

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

// GrantSettledPurchase creates access for a purchase whose settlement has reached SETTLED.
//
// It refuses, fail-closed, when: the purchase is not in this tenant/site; the settlement is not SETTLED;
// the purchase is in a state that cannot be granted. It is safe to call repeatedly and concurrently.
func (e *CommerceEngine) GrantSettledPurchase(ctx context.Context, tenantID, siteID, purchaseID string) (SettledGrantResult, error) {
	var res SettledGrantResult
	err := e.repo.WithTx(ctx, func(tx CommerceTx) error {
		g, err := tx.LoadSettledPurchaseGrant(ctx, tenantID, siteID, purchaseID)
		if err != nil {
			return err
		}
		// The subject lock is taken BEFORE the already-granted check, so two concurrent callers cannot both
		// read "not granted" and then both grant.
		if err := tx.AcquireSubjectLock(ctx, tenantID, siteID, g.Subject); err != nil {
			return err
		}
		g, err = tx.LoadSettledPurchaseGrant(ctx, tenantID, siteID, purchaseID) // re-read under the lock
		if err != nil {
			return err
		}
		if g.ExistingEntitlementID != "" {
			res = SettledGrantResult{EntitlementID: g.ExistingEntitlementID, AlreadyGranted: true}
			return nil
		}
		// Money is the authorization. Anything short of SETTLED grants nothing -- including MANUAL_REVIEW,
		// which is precisely the state that says nobody knows yet.
		if g.SettlementStatus != "SETTLED" {
			return &Error{Code: ErrNotRedeemable, Msg: "settlement is not SETTLED"}
		}
		if g.PurchaseState == "GRANTED" {
			// GRANTED with no entitlement row is a contradiction; refuse rather than paper over it.
			return &Error{Code: ErrConflict, Msg: "purchase is GRANTED but has no entitlement"}
		}
		if g.PurchaseState != "AWAITING_SETTLEMENT" && g.PurchaseState != "PENDING" {
			return &Error{Code: ErrNotRedeemable, Msg: "purchase state cannot be granted"}
		}

		superseded, err := tx.TerminateLiveEntitlementForSubject(ctx, tenantID, siteID, g.Subject)
		if err != nil {
			return err
		}
		var window *time.Time
		if g.GrantSnapshot.WindowEndsAt != "" {
			if w, perr := time.Parse(time.RFC3339, g.GrantSnapshot.WindowEndsAt); perr == nil {
				window = &w
			}
		}
		eid, err := tx.InsertEntitlement(ctx, EntitlementSpec{
			TenantID: tenantID, SiteID: siteID, PurchaseID: purchaseID, Subject: g.Subject,
			ServicePlanRevID:   g.GrantSnapshot.ServicePlanRevisionID,
			PackageRevID:       g.PackageRevisionID,
			PolicySnapshot:     g.GrantSnapshot,
			TimeAccountingMode: g.GrantSnapshot.TimeAccountingMode,
			EndMode:            g.GrantSnapshot.EndMode,
			WindowEndsAt:       window,
			SupersedesID:       superseded,
		})
		if err != nil {
			return err
		}
		// GRANTED only once the entitlement exists, exactly as the free path orders it.
		if err := tx.MarkPurchaseGranted(ctx, purchaseID); err != nil {
			return err
		}
		res = SettledGrantResult{EntitlementID: eid, Superseded: superseded}
		return nil
	})
	if err != nil {
		if e, ok := err.(*Error); ok {
			return SettledGrantResult{}, e
		}
		// Keep the underlying repository message. It is deterministic infrastructure text, never guest data,
		// and collapsing every failure to one sentence makes a grant problem unactionable.
		return SettledGrantResult{}, &Error{Code: ErrRepo, Msg: "grant settled purchase: " + err.Error()}
	}
	return res, nil
}

package payment

import (
	"context"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// CommerceGranter adapts the existing Phase-2 CommerceEngine to this package's Granter interface.
//
// It is the ONLY implementation of Granter in the tree, and it has deliberately no logic: it forwards, and
// nothing else. If this adapter ever needed a decision of its own, that would be the moment a second
// entitlement writer was being born, and the right response would be to change the Phase-2 path instead.
//
// The dependency points payment -> iamv2, which is the honest direction: the payment runtime is a caller of
// commerce, not the other way round.
type CommerceGranter struct{ Engine *iamv2.CommerceEngine }

// GrantSettledPurchase forwards to the one authoritative grant path.
func (g CommerceGranter) GrantSettledPurchase(ctx context.Context, tenantID, siteID, purchaseID string) (GrantOutcome, error) {
	r, err := g.Engine.GrantSettledPurchase(ctx, tenantID, siteID, purchaseID)
	if err != nil {
		return GrantOutcome{}, err
	}
	return GrantOutcome{EntitlementID: r.EntitlementID, AlreadyGranted: r.AlreadyGranted}, nil
}

// compile-time proof that the adapter is a Granter
var _ Granter = CommerceGranter{}

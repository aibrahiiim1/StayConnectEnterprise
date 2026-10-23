package payment

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SQLGranter calls only iam_v2.p4_grant_paid_entitlement, which re-resolves the purchase, quote, subject and snapshot
// itself under lock and enforces ONLINE_PAYMENT + SETTLED + AWAITING_SETTLEMENT.
// No caller-supplied evidence, so a compromised runtime role cannot invent a grant.
type SQLGranter struct{ Pool *pgxpool.Pool }

// GrantSettledSettlement is idempotent: an already granted purchase returns its existing entitlement.
func (g SQLGranter) GrantSettledSettlement(ctx context.Context, tenantID, siteID, settlementID string) (GrantOutcome, error) {
	var out GrantOutcome
	var superseded *string
	err := g.Pool.QueryRow(ctx,
		`SELECT entitlement_id::text, already_granted, superseded::text
		   FROM iam_v2.p4_grant_paid_entitlement($1::uuid,$2::uuid,$3::uuid)`,
		tenantID, siteID, settlementID).Scan(&out.EntitlementID, &out.AlreadyGranted, &superseded)
	if err != nil {
		return GrantOutcome{}, classify(err)
	}
	if superseded != nil {
		out.Superseded = *superseded
	}
	return out, nil
}

var _ Granter = SQLGranter{}

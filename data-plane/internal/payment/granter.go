package payment

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SQLGranter is the ONLY implementation of Granter, and it calls exactly one thing:
// iam_v2.p4_grant_paid_entitlement (migration 0021).
//
// WHY THIS SHAPE. The previous implementation adapted the Go CommerceEngine, which performed the grant from
// three primitives. Holding EXECUTE on those primitives is equivalent to holding the ability to fabricate an
// entitlement: p4_insert_entitlement takes the subject, the package revision and the policy snapshot as
// parameters, so the CALLER supplied the evidence rather than the database deriving it. A compromised
// runtime role could therefore grant free access to anyone, from evidence it invented, without ever
// touching a table directly -- which is why "the role holds no DML" was true and almost meaningless.
//
// The high-level operation takes a tenant, a site and a SETTLEMENT. It re-resolves the purchase, the quote,
// the auth context, the subject and the grant snapshot itself, under lock, and enforces ONLINE_PAYMENT +
// SETTLED + AWAITING_SETTLEMENT. There is no parameter here through which anything can be substituted --
// which is what lets a restricted role complete a paid grant without being able to invent one.
type SQLGranter struct{ Pool *pgxpool.Pool }

// GrantSettledSettlement performs the paid grant for a settled online payment. It is idempotent: a purchase
// that already granted returns its existing entitlement without writing anything.
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

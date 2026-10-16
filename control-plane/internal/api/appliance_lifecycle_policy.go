package api

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/portaltls"
)

// Appliance authority termination — what each path does with the licence (per-appliance, always):
//
//	Retire (normal)     licence: revoked at once | credentials: two-phase (signed terminal doc, ack, revoke)
//	Retire (emergency)  licence: revoked at once | credentials: revoked immediately, identity revoked
//	Replace             licence: KEPT until the replacement is activated at the same site, then revoked;
//	                    the old appliance is retired exactly like a normal Retire (signed terminal doc, ack,
//	                    THEN credentials revoked)
//	Move, same customer licence: re-issued, terms unchanged, bound to the new site, next version; refused
//	                    (fail closed) when licensing is unavailable or the licence cannot be read
//	Move, new customer  REFUSED. Changing customer is Retire -> factory-reset -> register -> Activate.
//	Delete              only waiting or retired appliances; licence and certificates revoked defensively; a
//	                    retired identity key is remembered and can never register again
//
// Credentials (certificate) are revoked ONLY by a verified terminal ack (AckHandler) or an emergency retire.
// Licence operations (suspend / resume / revoke / set) NEVER touch the appliance lifecycle.
//
// THE PORTAL HOSTNAME ENDS WITH THE LICENCE, on every one of those paths, because they all come through here:
// the hostname is released (its public A record is deleted and the label is never handed out again), every
// valid portal certificate is queued for revocation, open certificate orders are cancelled and the hotel
// domain claim is removed (portaltls.ReleaseAppliance). Same transaction as the licence: both or neither.

// revokeApplianceBoundLicenses revokes every current (active/suspended) licence bound to this appliance and
// releases its portal hostname and certificates. Idempotent. Returns the revoked licence ids.
func (b *Base) revokeApplianceBoundLicenses(ctx context.Context, applianceID string) ([]string, error) {
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	revoked, err := b.revokeApplianceBoundLicensesTx(ctx, tx, applianceID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	b.PortalTLS.Nudge() // the worker deletes the A record and revokes now, not at its next pass
	return revoked, nil
}

// revokeApplianceBoundLicensesTx is revokeApplianceBoundLicenses inside the caller's transaction: retirement
// and replacement commit the licence revocation together with the signed terminal assignment. The caller
// nudges the portal-TLS worker after committing.
func (b *Base) revokeApplianceBoundLicensesTx(ctx context.Context, tx pgx.Tx, applianceID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
        UPDATE licenses SET status='revoked', revoked_at=now()
         WHERE $1::uuid = ANY(appliance_ids) AND status IN ('active','suspended')
        RETURNING id::text`, applianceID)
	if err != nil {
		return nil, err
	}
	revoked := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			revoked = append(revoked, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := portaltls.ReleaseAppliance(ctx, tx, applianceID, portaltls.ActorSystem); err != nil {
		return nil, err
	}
	return revoked, nil
}

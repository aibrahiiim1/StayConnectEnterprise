package api

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/portaltls"
)

// revokeApplianceBoundLicenses revokes every active/suspended licence bound to the appliance and releases its
// portal hostname and certificates in the same transaction. Idempotent. Returns the revoked licence ids.
// Appliance certificates are revoked only by a verified terminal ack or an emergency retire, never here.
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
	b.PortalTLS.Nudge()
	return revoked, nil
}

// revokeApplianceBoundLicensesTx runs inside the caller's transaction; the caller nudges the portal-TLS worker after commit.
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

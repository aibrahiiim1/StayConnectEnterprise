package api

import "context"

// Appliance authority termination — what each path does with the licence (per-appliance, always):
//
//	Retire (normal)     licence: revoked at once | credentials: two-phase (signed terminal doc, ack, revoke)
//	Retire (emergency)  licence: revoked at once | credentials: revoked immediately, identity revoked
//	Replace             licence: KEPT until the replacement is activated at the same site, then the old
//	                    appliance is retired (licence revoked, terminal doc signed, credentials revoked)
//	Move, same customer licence: re-issued, terms unchanged, bound to the new site
//	Move, new customer  licence: revoked (the new customer's licence is a new decision)
//	Delete              only waiting or retired appliances; licence and certificates revoked defensively
//
// Licence operations (suspend / resume / revoke / set) NEVER touch the appliance lifecycle.

// revokeApplianceBoundLicenses revokes every current (active/suspended) licence bound to this appliance.
// Idempotent. Returns the revoked licence ids.
func (b *Base) revokeApplianceBoundLicenses(ctx context.Context, applianceID string) ([]string, error) {
	rows, err := b.DB.Query(ctx, `
        UPDATE licenses SET status='revoked', revoked_at=now()
         WHERE $1::uuid = ANY(appliance_ids) AND status IN ('active','suspended')
        RETURNING id::text`, applianceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revoked := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			revoked = append(revoked, id)
		}
	}
	return revoked, rows.Err()
}

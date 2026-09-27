package api

import "context"

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

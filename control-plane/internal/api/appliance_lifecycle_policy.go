package api

import "context"

// Appliance lifecycle termination policy — the single authoritative contract for
// ending or changing an appliance's authority. Every terminal/lifecycle handler
// routes its license-termination decision through revokeApplianceBoundLicenses so
// no path can silently leave an orphaned active/suspended license behind.
//
// Per-action contract (what each path MUST do):
//
//	Delete (admin + tenant)  license: revoke bound  | cert/assignment: cascade-removed | NATS: denied (cert gone)
//	Decommission (terminal)  license: revoke bound  | cert/NATS: two-phase (ack) or immediate (emergency) | assignment: signed terminal doc
//	Revoke (terminal)        license: revoke bound  | cert/NATS: two-phase (ack) or immediate (emergency) | assignment: signed terminal doc
//	Emergency compromise     license: revoke bound  | cert/NATS: revoked IMMEDIATELY | assignment: signed terminal doc (unconfirmed)
//	Deactivate (reversible)  license: revoke bound  | cert/assignment: PRESERVED (may re-activate)
//	Replace                  license: KEEP (old box stays operational until the replacement is online) — revoke via the terminal path afterwards
//	Reassign (cross-tenant)  license: revoke bound old-tenant entitlement | assignment: re-signed for the new owner
//	Factory reset (local)    Central authority UNCHANGED — surfaced/reconciled via orphan reconcile + boot-hello, never treated as a Central delete
//
// Certificate/NATS termination is deliberately NOT folded in here: it differs by
// action (two-phase terminal delivery for terminal states, ON DELETE CASCADE for
// hard delete, preserved for reversible deactivate) and already lives in one place
// per mode (phase2ShutCredentials for the terminal path).

// revokeApplianceBoundLicenses revokes every active/suspended license BOUND to
// this appliance (its id present in licenses.appliance_ids). Site-wide licenses
// (empty appliance_ids) are intentionally left untouched — a site license stays
// valid while its site exists and may entitle other appliances. Idempotent:
// re-running revokes nothing already terminal. Returns the revoked license ids.
func (b *Base) revokeApplianceBoundLicenses(ctx context.Context, applianceID string) ([]string, error) {
	if b.Lic == nil {
		return nil, nil
	}
	rows, err := b.DB.Query(ctx,
		`SELECT id::text FROM licenses WHERE $1 = ANY(appliance_ids) AND status IN ('active','suspended')`,
		applianceID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	revoked := make([]string, 0, len(ids))
	for _, lid := range ids {
		if b.Lic.Revoke(ctx, lid) == nil {
			revoked = append(revoked, lid)
		}
	}
	return revoked, nil
}

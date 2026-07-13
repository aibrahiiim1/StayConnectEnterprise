package api

import (
	"context"
	"net/http"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// replacementWindow bounds how long an outgoing (replacement_pending) appliance
// may stay licensed while its replacement is being enrolled. It matches the
// replacement bootstrap-token lifetime so authority and enrolment expire together.
const replacementWindow = 72 * time.Hour

// completeReplacementIfPending is invoked when an appliance becomes Active. If the
// SAME site has an outgoing appliance flagged replacement_pending, the replacement
// is now complete, so the OLD appliance's authority is terminated through the
// centralized lifecycle policy: revoke its bound licenses, revoke its credentials
// (cert → mTLS + NATS denied), mark it decommissioned, and link the two rows.
// Idempotent and safe: a no-op when no replacement is pending. This is what
// guarantees the old box cannot remain licensed once its replacement is online.
func (b *Base) completeReplacementIfPending(ctx context.Context, r *http.Request, newID, siteID string) {
	if siteID == "" {
		return
	}
	var oldID, prevState string
	if err := b.DB.QueryRow(ctx, `
        SELECT id::text, COALESCE(lifecycle_state,'')
          FROM appliances
         WHERE site_id=$1 AND id <> $2 AND replacement_pending = true AND replaced_by IS NULL
         ORDER BY updated_at ASC LIMIT 1`, siteID, newID).Scan(&oldID, &prevState); err != nil {
		return // nothing pending — normal activation
	}

	licRevoked, _ := b.revokeApplianceBoundLicenses(ctx, oldID)
	_ = b.phase2ShutCredentials(ctx, r, oldID, "replaced")
	_, _ = b.DB.Exec(ctx, `
        UPDATE appliances SET lifecycle_state='decommissioned', status='retired',
               replacement_pending=false, replacement_deadline=NULL, replaced_by=$2::uuid, updated_at=now()
         WHERE id=$1`, oldID, newID)
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET replacement_of=$2::uuid, updated_at=now() WHERE id=$1`, newID, oldID)

	actor := "system"
	if s := auth.FromContext(r.Context()); s != nil {
		actor = emailOf(s)
	}
	recordLifecycle(ctx, b.DB, oldID, prevState, "decommissioned", actor, clientIPFromReq(r), "replacement completed by "+newID)
	audit.Op(ctx, b.DB, r, "appliance.replacement_completed", "appliance", oldID, map[string]any{
		"replaced_by": newID, "site_id": siteID, "licenses_revoked": len(licRevoked),
	})
	// A completed replacement clears any window-expiry alert it may have raised.
	_, _ = b.DB.Exec(ctx, `
        UPDATE appliance_security_alerts SET status='resolved', resolved=true, acknowledged_at=now()
         WHERE appliance_id=$1 AND kind='replacement_window_expired' AND resolved=false`, oldID)
}

// ReconcileReplacements raises a visible operational/security alert for any
// replacement that has NOT completed within its window, so an outgoing appliance
// can never stay licensed indefinitely without a decision. It deliberately does
// NOT auto-terminate the old appliance (service continuity + explicit audited
// operator decision required). Idempotent: it will not re-raise while an open
// alert already exists. Returns the number of alerts raised this pass.
func ReconcileReplacements(ctx context.Context, b *Base) (int64, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT a.id::text, COALESCE(a.serial,''), COALESCE(a.site_id::text,'')
          FROM appliances a
         WHERE a.replacement_pending = true AND a.replaced_by IS NULL
           AND a.replacement_deadline IS NOT NULL AND a.replacement_deadline < now()
           AND NOT EXISTS (
               SELECT 1 FROM appliance_security_alerts s
                WHERE s.appliance_id = a.id AND s.kind = 'replacement_window_expired' AND s.resolved = false)`)
	if err != nil {
		return 0, err
	}
	type row struct{ id, serial, site string }
	var todo []row
	for rows.Next() {
		var x row
		if rows.Scan(&x.id, &x.serial, &x.site) == nil {
			todo = append(todo, x)
		}
	}
	rows.Close()

	var n int64
	for _, x := range todo {
		_, _ = b.DB.Exec(ctx, `
            INSERT INTO appliance_security_alerts (appliance_id, serial, kind, detail, status)
            VALUES ($1,$2,'replacement_window_expired',
                    '{"reason":"replacement did not complete within the allowed window; the outgoing appliance is still licensed. Confirm the replacement, extend the window, or decommission the old appliance — an explicit operator decision is required.","severity":"operational"}','open')`,
			x.id, x.serial)
		audit.System(ctx, b.DB, "appliance.replacement_window_expired", "appliance", x.id,
			map[string]any{"site_id": x.site, "note": "window elapsed; operator decision required (not auto-terminated)"})
		n++
	}
	return n, nil
}

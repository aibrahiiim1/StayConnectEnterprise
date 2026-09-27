package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// replacementWindow bounds how long an outgoing (replacement_pending) appliance may stay licensed while its
// replacement registers itself and is activated at the same site.
const replacementWindow = 72 * time.Hour

// completeReplacementIfPending is invoked when an appliance is activated. If the SAME site has an outgoing
// appliance marked for replacement, the replacement is complete and the OLD appliance is retired through the
// normal, acknowledged two-phase terminal delivery (terminal_delivery.go) — exactly like Retire:
//
//   - its licence is revoked now (licence state never blocks the assignment channel or the ack);
//   - a signed DECOMMISSIONED assignment is minted and delivery is recorded as pending;
//   - its certificate and identity stay valid, so the old box can fetch that document over mTLS, adopt it
//     and send its signed acknowledgment. Only that ack (AckHandler) revokes its credentials and marks it
//     decommissioned. No ack within terminalTimeout -> terminal_delivery_failed + a security alert; the
//     operator may then retire it as an emergency, which revokes the credentials without waiting.
//
// Revoking the credentials here, before the ack, is the bug this replaced: the old box could then never fetch
// its retirement (strictMTLSSelf refuses a revoked certificate) and kept serving on its cached assignment.
// Idempotent: a no-op when no replacement is pending.
func (b *Base) completeReplacementIfPending(ctx context.Context, r *http.Request, newID, siteID string) {
	if siteID == "" {
		return
	}
	var oldID string
	err := b.DB.QueryRow(ctx, `
        SELECT id::text
          FROM appliances
         WHERE site_id=$1 AND id <> $2 AND replacement_pending = true AND replaced_by IS NULL
           AND lifecycle_state = 'assigned'
         ORDER BY updated_at ASC LIMIT 1`, siteID, newID).Scan(&oldID)
	if errors.Is(err, pgx.ErrNoRows) {
		return // nothing pending — a normal activation
	}
	if err != nil {
		audit.Op(ctx, b.DB, r, "appliance.replacement_check_failed", "appliance", newID,
			map[string]any{"site_id": siteID, "error": err.Error()})
		return
	}
	actor := "system"
	if s := auth.FromContext(r.Context()); s != nil {
		actor = emailOf(s)
	}
	reason := "replaced by " + newID

	// Link the two rows and close the replacement window first, so a retry never picks the old box again and
	// the window reconciler never alerts on a replacement that did complete.
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET replacement_pending=false, replacement_deadline=NULL,
        replaced_by=$2::uuid, updated_at=now() WHERE id=$1`, oldID, newID)
	_, _ = b.DB.Exec(ctx, `UPDATE appliances SET replacement_of=$2::uuid, updated_at=now() WHERE id=$1`, newID, oldID)
	_, _ = b.DB.Exec(ctx, `
        UPDATE appliance_security_alerts SET status='resolved', resolved=true, acknowledged_at=now()
         WHERE appliance_id=$1 AND kind='replacement_window_expired' AND resolved=false`, oldID)

	licRevoked, licErr := b.revokeApplianceBoundLicenses(ctx, oldID)
	ver, termErr := b.beginTerminalDelivery(ctx, r, oldID, assignment.StateDecommissioned, reason, false)
	if termErr != nil {
		// The replacement is live but the old box was not told. Visible, never silent: the operator retires
		// it (normal or emergency) from its page.
		audit.Op(ctx, b.DB, r, "appliance.replacement_terminal_unsigned", "appliance", oldID,
			map[string]any{"replaced_by": newID, "error": termErr.Error()})
	} else {
		recordLifecycle(ctx, b.DB, oldID, "assigned", "retiring", actor, clientIPFromReq(r), reason)
	}
	payload := map[string]any{"replaced_by": newID, "site_id": siteID, "licenses_revoked": len(licRevoked),
		"assignment_version": ver, "retirement": "awaiting_ack"}
	if licErr != nil {
		payload["license_revoke_error"] = licErr.Error()
	}
	audit.Op(ctx, b.DB, r, "appliance.replacement_completed", "appliance", oldID, payload)
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

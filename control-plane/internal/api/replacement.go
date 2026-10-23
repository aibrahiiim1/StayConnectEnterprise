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

// replacementWindow bounds how long an outgoing appliance may stay licensed until its replacement is activated at the same site.
const replacementWindow = 72 * time.Hour

// completeReplacementIfPending runs on activation. If the same site has an outgoing appliance marked for
// replacement, the old one is retired through the acknowledged two-phase terminal delivery, like Retire:
//
//   - its licence is revoked now;
//   - a signed DECOMMISSIONED assignment is minted and delivery recorded as pending;
//   - its certificate stays valid so the old box can fetch the document over mTLS; only its signed ack
//     (AckHandler) revokes the credentials. No ack within terminalTimeout raises terminal_delivery_failed
//     and an alert; the operator may then emergency-retire it.
//
// Credentials must not be revoked before the ack: strictMTLSSelf would refuse the retirement fetch and the box
// would keep serving on its cached assignment. Idempotent; a no-op when no replacement is pending.
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
		return
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

	// One transaction: link the rows, close the window, revoke the old licence and sign its terminal assignment. Never
	// a revoked licence without a retirement document (the box would keep serving on its cache). On failure nothing
	// is kept: the old appliance stays marked, the window reconciler alerts, and the failure is audited.
	var licRevoked []string
	var ver int64
	err = func() error {
		tx, err := b.DB.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		for _, s := range []struct {
			sql  string
			args []any
		}{
			{`UPDATE appliances SET replacement_pending=false, replacement_deadline=NULL,
                replaced_by=$2::uuid, updated_at=now() WHERE id=$1`, []any{oldID, newID}},
			{`UPDATE appliances SET replacement_of=$2::uuid, updated_at=now() WHERE id=$1`, []any{newID, oldID}},
			{`UPDATE appliance_security_alerts SET status='resolved', resolved=true, acknowledged_at=now()
               WHERE appliance_id=$1 AND kind='replacement_window_expired' AND resolved=false`, []any{oldID}},
		} {
			if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
				return err
			}
		}
		if licRevoked, err = b.revokeApplianceBoundLicensesTx(ctx, tx, oldID); err != nil {
			return err
		}
		if ver, err = b.beginTerminalDelivery(ctx, tx, oldID, assignment.StateDecommissioned, reason, false); err != nil {
			return err
		}
		recordLifecycle(ctx, tx, oldID, "assigned", "retiring", actor, clientIPFromReq(r), reason)
		return tx.Commit(ctx)
	}()
	if err != nil {
		audit.Op(ctx, b.DB, r, "appliance.replacement_terminal_unsigned", "appliance", oldID,
			map[string]any{"replaced_by": newID, "error": publicError(r, err),
				"operator_action": "the old appliance was NOT retired and keeps its licence; retire it from its page"})
		return
	}
	b.PortalTLS.Nudge()
	audit.Op(ctx, b.DB, r, "appliance.replacement_completed", "appliance", oldID, map[string]any{
		"replaced_by": newID, "site_id": siteID, "licenses_revoked": len(licRevoked),
		"assignment_version": ver, "retirement": "awaiting_ack"})
}

// ReconcileReplacements raises an alert for any replacement not completed within its window. It deliberately does
// not auto-terminate the old appliance (an explicit, audited operator decision is required). Idempotent while an
// alert is open; returns the number of alerts raised.
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

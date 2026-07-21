// Package enforce is the Phase-3 enforcement layer that connects the durable Entitlement model to what the
// edge actually does: the SHAPING PLAN netd applies (per-session rate limits derived from the Entitlement's
// pinned Service Plan revision) and the EXPIRY ENFORCEMENT acctd performs (ending access when a validity
// window elapses or a data quota is crossed).
//
// Two properties drive the design:
//
//   - The plan is DERIVED, never remembered. netd is told what the current durable state implies, so a Grace
//     conversion, a rebinding or a revocation is reflected without any separate "tell netd" bookkeeping that
//     could drift from the database.
//   - Expiry is recorded at the TRUE time it happened — the instant the window elapsed, or the sample time of
//     the accounting record that crossed the quota — not at the moment a sweep happened to notice. Access that
//     ended at 14:03 is recorded as ending at 14:03 even if the sweep runs at 14:07.
package enforce

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionShape is one live session's desired treatment at the edge.
type SessionShape struct {
	SessionID     string
	EntitlementID string
	DeviceID      string
	IP            string
	MAC           string
	DownKbps      int
	UpKbps        int
}

// Plan is what the edge should be enforcing right now for a site.
type Plan struct {
	// Shape is every live session that is entitled, with the rates its CURRENT entitlement's plan revision
	// specifies (a grace conversion therefore re-rates the session automatically).
	Shape []SessionShape
	// Tear is every session that must have no shaping and no forwarding: it has ended, or its entitlement is
	// no longer live. Ordering matters to the caller: tear down first, then shape.
	Tear []SessionShape
}

// Enforcer derives plans and applies expiry enforcement.
type Enforcer struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Enforcer { return &Enforcer{pool: pool} }

// PlanForSite derives the complete shaping plan for a site from durable state alone.
func (e *Enforcer) PlanForSite(ctx context.Context, tenant, site string) (Plan, error) {
	var p Plan
	rows, err := e.pool.Query(ctx, `SELECT s.id::text, s.entitlement_id::text, s.device_id::text,
			COALESCE(host(s.ip),''), COALESCE(s.mac::text,''),
			COALESCE(spr.down_kbps,0), COALESCE(spr.up_kbps,0),
			(s.state='active' AND s.ended IS NULL AND e.status='ACTIVE'
			 AND (e.window_ends_at IS NULL OR e.window_ends_at > now())) AS entitled
		FROM iam_v2.sessions s
		JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		WHERE s.tenant_id=$1 AND s.site_id=$2 AND (s.state='active' OR s.ended > now() - interval '1 hour')
		ORDER BY s.started`, tenant, site)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var sh SessionShape
		var entitled bool
		if err := rows.Scan(&sh.SessionID, &sh.EntitlementID, &sh.DeviceID, &sh.IP, &sh.MAC,
			&sh.DownKbps, &sh.UpKbps, &entitled); err != nil {
			return p, err
		}
		if entitled {
			p.Shape = append(p.Shape, sh)
		} else {
			// a torn-down session carries no rates: the edge must remove its shaping, not re-apply it slower.
			sh.DownKbps, sh.UpKbps = 0, 0
			p.Tear = append(p.Tear, sh)
		}
	}
	return p, rows.Err()
}

// Expiry is one enforced ending.
type Expiry struct {
	EntitlementID string
	Reason        string // TIME | DATA
	At            time.Time
	Sessions      int
	Devices       int
}

// EnforceExpiries ends every live Entitlement of the site whose validity window has elapsed or whose data
// quota has been crossed, AT THE TRUE TIME it happened, and revokes its access with it. It is idempotent: an
// Entitlement already terminated is skipped, and re-running the sweep changes nothing.
func (e *Enforcer) EnforceExpiries(ctx context.Context, tenant, site string) ([]Expiry, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Candidates: live entitlements whose window has elapsed, or whose attributed usage has reached the plan
	// quota. For a quota crossing, the effective time is the SAMPLE that crossed it — the moment the guest
	// actually used up the allowance, not the moment this sweep ran.
	rows, err := tx.Query(ctx, `SELECT e.id::text,
			CASE WHEN e.window_ends_at IS NOT NULL AND e.window_ends_at <= now() THEN 'TIME' ELSE 'DATA' END AS reason,
			CASE WHEN e.window_ends_at IS NOT NULL AND e.window_ends_at <= now() THEN e.window_ends_at
			     ELSE COALESCE(q.crossed_at, now()) END AS at
		FROM iam_v2.entitlements e
		LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		LEFT JOIN LATERAL (
			SELECT min(x.sampled_at) AS crossed_at FROM (
				SELECT ar.sampled_at,
				       sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
				FROM iam_v2.accounting_records ar
				JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
				  AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
				  AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
			) x WHERE spr.data_quota_bytes IS NOT NULL AND x.running >= spr.data_quota_bytes) q ON true
		WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.status IN ('ACTIVE','PENDING','SUSPENDED')
		  AND ( (e.window_ends_at IS NOT NULL AND e.window_ends_at <= now()) OR q.crossed_at IS NOT NULL )
		FOR UPDATE OF e`, tenant, site)
	if err != nil {
		return nil, err
	}
	var due []Expiry
	for rows.Next() {
		var x Expiry
		if err := rows.Scan(&x.EntitlementID, &x.Reason, &x.At); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range due {
		x := &due[i]
		// the controlled boundary termination records the ending at its TRUE time and invalidates any
		// lifecycle fact recorded for the period after it (TERMINATED is terminal).
		if _, err := tx.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1,$2,$3)`,
			x.EntitlementID, x.At, x.Reason); err != nil {
			return nil, err
		}
		d, s, err := revoke(ctx, tx, x.EntitlementID, x.At)
		if err != nil {
			return nil, err
		}
		x.Devices, x.Sessions = d, s
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return due, nil
}

// revoke closes the Entitlement's open authorization intervals and ends its live sessions at the same instant,
// so nothing keeps forwarding traffic for access that has ended.
func revoke(ctx context.Context, tx pgx.Tx, ent string, at time.Time) (int, int, error) {
	ct, err := tx.Exec(ctx, `WITH closed AS (
		UPDATE iam_v2.entitlement_device_authorizations a SET deauthorized_at = GREATEST($2::timestamptz, a.authorized_at)
		WHERE a.entitlement_id=$1 AND a.deauthorized_at IS NULL RETURNING a.entitlement_id, a.device_id)
	UPDATE iam_v2.entitlement_devices ed SET status='DISCONNECTED', disconnected_reason='ENTITLEMENT_ENDED'
	FROM closed WHERE ed.entitlement_id=closed.entitlement_id AND ed.device_id=closed.device_id`, ent, at)
	if err != nil {
		return 0, 0, err
	}
	ct2, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended',
			ended=GREATEST($2::timestamptz, started), end_reason='ENTITLEMENT_ENDED'
		WHERE entitlement_id=$1 AND state='active'`, ent, at)
	if err != nil {
		return 0, 0, err
	}
	return int(ct.RowsAffected()), int(ct2.RowsAffected()), nil
}

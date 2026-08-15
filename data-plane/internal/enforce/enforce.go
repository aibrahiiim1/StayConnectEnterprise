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

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// SessionShape is one live session's desired treatment at the edge.
type SessionShape struct {
	SessionID     string
	EntitlementID string
	DeviceID      string
	IP            string
	MAC           string
	// Bridge is where the session's traffic actually appears. Shaping is per-bridge, so a plan without it
	// could only be applied by guessing — and guessing the wrong bridge silently shapes nobody.
	Bridge   string
	DownKbps int
	UpKbps   int
	// WindowEndsAt is the entitlement's hard validity boundary, carried to the edge so the applier can bound
	// its kernel authorization lease by it. Nil means the entitlement states no wall-clock end.
	WindowEndsAt *time.Time
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
type Enforcer struct {
	pool *pgxpool.Pool
	// aggregateTimeMaxCharge, when positive, turns on the Phase-6 AGGREGATE_ONLINE_TIME accrual tick inside
	// the expiry sweep, bounded by this many seconds per tick. Zero -- the default, and the only value any
	// existing caller produces -- means the mode is DARK: no accrual runs, no watermark is written, and the
	// sweep behaves exactly as it did before Phase 6.
	aggregateTimeMaxCharge int
}

func New(pool *pgxpool.Pool) *Enforcer { return &Enforcer{pool: pool} }

// WithAggregateOnlineTime enables the Phase-6 accrual tick with the given per-tick charge bound.
//
// THE BOUND IS THE WHOLE SAFETY PROPERTY, so it is a required argument rather than a default: it is what
// stops an appliance that was switched off for six hours from billing six hours the moment it comes back.
// It should be a small multiple of the sweep interval -- large enough that an ordinary slow tick is charged
// in full, small enough that an outage is obviously an outage.
func (e *Enforcer) WithAggregateOnlineTime(maxChargeSeconds int) *Enforcer {
	e.aggregateTimeMaxCharge = maxChargeSeconds
	return e
}

// Pool exposes the database handle so a composition root can call the controlled Phase-3 operations directly
// without opening a second pool. The operations themselves remain the authority on what is allowed.
func (e *Enforcer) Pool() *pgxpool.Pool { return e.pool }

// PlanForSite derives the complete enforcement plan for a site from durable state alone.
//
// PENDING_ENFORCEMENT sessions are included as DESIRED. That state means "the grant is durable and this guest
// should be enforced, but the kernel has not confirmed it yet" — which is exactly the set the enforcement
// owner must act on. Excluding them would deadlock the lifecycle: the grant would wait forever for an ACTIVE
// that nothing was ever asked to produce. They are safe to include because being in the plan is not access:
// netd still admits a guest only after the accountable class is proven, and promotes the Session only after
// the nft gate is proven.
func (e *Enforcer) PlanForSite(ctx context.Context, tenant, site string) (Plan, error) {
	var p Plan
	rows, err := e.pool.Query(ctx, `SELECT s.id::text, s.entitlement_id::text, s.device_id::text,
			COALESCE(host(s.ip),''), COALESCE(s.mac::text,''), COALESCE(s.ingress_interface,''),
			COALESCE(spr.down_kbps,0), COALESCE(spr.up_kbps,0), e.window_ends_at,
			(s.state IN ('active','PENDING_ENFORCEMENT') AND s.ended IS NULL AND e.status='ACTIVE'
			 AND (e.window_ends_at IS NULL OR e.window_ends_at > now())) AS entitled
		FROM iam_v2.sessions s
		JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		WHERE s.tenant_id=$1 AND s.site_id=$2
		  AND (s.state IN ('active','PENDING_ENFORCEMENT') OR s.ended > now() - interval '1 hour')
		ORDER BY s.started`, tenant, site)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var sh SessionShape
		var entitled bool
		if err := rows.Scan(&sh.SessionID, &sh.EntitlementID, &sh.DeviceID, &sh.IP, &sh.MAC, &sh.Bridge,
			&sh.DownKbps, &sh.UpKbps, &sh.WindowEndsAt, &entitled); err != nil {
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

	// THE CANDIDATE READ DOES NOT LOCK, and that is deliberate rather than an omission.
	//
	// It used to take FOR UPDATE OF e, which PostgreSQL only allows to a role holding UPDATE on
	// iam_v2.entitlements -- the one privilege an accounting daemon must never have, because a role that can
	// write an entitlement row can end a guest's access, or hand it back, with no evidence anywhere. The
	// lock now lives where the write lives: p6_expire_entitlement takes the row lock itself, as the definer,
	// and is idempotent for an entitlement another sweep already ended. Two concurrent sweeps therefore
	// converge on one terminal transition without either of them holding write authority.
	//
	// Candidates: live entitlements that have reached a terminal condition -- the outer window elapsed, or
	// attributed usage crossed the plan quota -- WITH THE EARLIEST OF THE TWO WINNING.
	//
	// The earliest is the whole point, and the previous version got it wrong in a way that only shows when
	// both have happened. It asked "is the window elapsed?" first and answered TIME if so, without comparing
	// instants. So an entitlement whose quota was crossed at T1 and whose window expired later at T2, swept
	// after T2, was recorded as ending at T2 for the wrong reason -- and, worse, the aggregate tick was
	// handed T2 as its cap and could bill the guest for the T1..T2 stretch during which their access had
	// already ended.
	//
	// Now both instants are derived and LEAST decides, with the reason following the instant rather than the
	// other way round. The DATA crossing is still computed in exactly one place, by the same LATERAL: the
	// only change is which answer is preferred once both exist.
	rows, err := tx.Query(ctx, `SELECT id, reason, at FROM (
			SELECT e.id::text AS id,
				CASE WHEN win.at IS NOT NULL AND (dat.at IS NULL OR win.at <= dat.at) THEN 'TIME' ELSE 'DATA' END AS reason,
				LEAST(COALESCE(win.at, 'infinity'::timestamptz), COALESCE(dat.at, 'infinity'::timestamptz)) AS at
			FROM iam_v2.entitlements e
			LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
			LEFT JOIN LATERAL (
				SELECT e.window_ends_at AS at
				 WHERE e.window_ends_at IS NOT NULL AND e.window_ends_at <= now()) win ON true
			LEFT JOIN LATERAL (
				SELECT min(x.sampled_at) AS at FROM (
					SELECT ar.sampled_at,
					       sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
					FROM iam_v2.accounting_records ar
					JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
					  AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
					  AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
				) x WHERE spr.data_quota_bytes IS NOT NULL AND x.running >= spr.data_quota_bytes) dat ON true
			WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.status IN ('ACTIVE','PENDING','SUSPENDED')
			  AND (win.at IS NOT NULL OR dat.at IS NOT NULL)
		) c ORDER BY c.id`, tenant, site)
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

	// PHASE 6 (DARK unless the flag is on): accrue online time IN THIS TRANSACTION, and only now.
	//
	// THE ORDER IS THE CORRECTNESS. The candidate query above owns the DATA crossing -- it derives the exact
	// sample that crossed the quota, and that algorithm exists in exactly one place. Running the accrual
	// first meant the tick knew nothing about it, so an entitlement whose quota was crossed an hour ago went
	// on accruing online seconds for that hour: time charged for access that had already ended, and, if the
	// accrual reached the budget, an exhaustion instant later than the true ending recorded as though the
	// minutes had run out too.
	//
	// So the instants found above are passed IN, as per-entitlement caps. The tick treats a cap exactly like
	// the outer window -- a hard ceiling on what is billable -- and never computes a data crossing itself.
	var exhausted []Expiry
	if e.aggregateTimeMaxCharge > 0 {
		capIDs := make([]string, 0, len(due))
		capAt := make([]time.Time, 0, len(due))
		for _, x := range due {
			capIDs = append(capIDs, x.EntitlementID)
			capAt = append(capAt, x.At)
		}
		trows, err := tx.Query(ctx,
			`SELECT entitlement_id::text, exhausted_at
			   FROM iam_v2.p6_tick_online_time($1,$2,now(),$3,$4::uuid[],$5::timestamptz[])`,
			tenant, site, e.aggregateTimeMaxCharge, capIDs, capAt)
		if err != nil {
			return nil, err
		}
		for trows.Next() {
			var x Expiry
			if err := trows.Scan(&x.EntitlementID, &x.At); err != nil {
				trows.Close()
				return nil, err
			}
			// The contract's existing TIME reason: aggregate exhaustion is a time termination, and which
			// time rule ran out is recorded as evidence rather than as new terminal vocabulary.
			x.Reason = "TIME"
			exhausted = append(exhausted, x)
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return nil, err
		}
	}

	// MERGE, KEEPING THE EARLIEST INSTANT. An entitlement can reach two terminal conditions in one sweep --
	// its outer window elapsed AND its online-time budget ran out -- and section 6.1 says the FIRST reached
	// triggers ONE atomic terminal transition. Terminating twice is not merely untidy: the second would
	// re-date the ending, and the ending is what every downstream record is attributed against.
	for _, x := range exhausted {
		found := false
		for i := range due {
			if due[i].EntitlementID == x.EntitlementID {
				found = true
				if x.At.Before(due[i].At) {
					due[i].At = x.At
					due[i].Reason = x.Reason
				}
				break
			}
		}
		if !found {
			due = append(due, x)
		}
	}

	for i := range due {
		x := &due[i]

		// PHASE 6: WHICH time rule ran out, decided here and recorded by the writer below as evidence bound
		// to the transition. The terminal_reason set is untouched -- 'TIME' covers both -- so this is the
		// only place the difference between "the week ran out" and "the minutes ran out" is answerable.
		var cause *string
		if e.aggregateTimeMaxCharge > 0 && x.Reason == "TIME" {
			var mode string
			if err := tx.QueryRow(ctx,
				`SELECT COALESCE(time_accounting_mode,'VALIDITY_WINDOW') FROM iam_v2.entitlements WHERE id=$1`,
				x.EntitlementID).Scan(&mode); err != nil {
				return nil, err
			}
			c := "VALIDITY_WINDOW_ELAPSED"
			if mode == "AGGREGATE_ONLINE_TIME" {
				// An aggregate entitlement reported by the tick ran out of MINUTES; one that reached this
				// sweep through the window branch ran out of CALENDAR.
				c = "AGGREGATE_OUTER_WINDOW_EXPIRED"
				for _, ex := range exhausted {
					if ex.EntitlementID == x.EntitlementID && !ex.At.After(x.At) {
						c = "AGGREGATE_ONLINE_TIME_EXHAUSTED"
						break
					}
				}
			}
			cause = &c
		}

		// ONE AUDITED OPERATION, NOT FIVE STATEMENTS.
		//
		// The termination, its evidence and the revocation of devices and sessions used to be issued from
		// here as separate calls -- terminate_entitlement_at_boundary, p6_record_time_termination, and two
		// UPDATEs. That works only for an identity holding all of those privileges, which is precisely the
		// identity an accounting daemon must NOT have: a role that can call the termination primitive
		// directly can end any entitlement for any reason at any instant.
		//
		// p6_expire_entitlement is the sanctioned writer for this exact responsibility. It validates that
		// the reason is TIME or DATA and that the instant is not in the future, calls the SAME boundary
		// primitive every other terminal path calls, records the Phase-6 evidence when the sweep knows which
		// time rule ran out, and closes the authorization intervals and sessions under its own declared
		// scope. svc_acctd holds EXECUTE on it and on nothing it calls.
		if err := tx.QueryRow(ctx,
			`SELECT devices, sessions FROM iam_v2.p6_expire_entitlement($1,$2,$3,$4)`,
			x.EntitlementID, x.At, x.Reason, cause).Scan(&x.Devices, &x.Sessions); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return due, nil
}

// revoke closes the Entitlement's open authorization intervals and ends its live sessions at the same instant,
// so nothing keeps forwarding traffic for access that has ended.
func revoke(ctx context.Context, tx pgx.Tx, ent string, at time.Time) (int, int, error) {
	// Closing an authorization interval is a write to the device-authorization family.
	if err := writerguard.Open(ctx, tx, writerguard.CapDeviceAuth); err != nil {
		return 0, 0, err
	}
	ct, err := tx.Exec(ctx, `WITH closed AS (
		UPDATE iam_v2.entitlement_device_authorizations a SET deauthorized_at = GREATEST($2::timestamptz, a.authorized_at)
		WHERE a.entitlement_id=$1 AND a.deauthorized_at IS NULL RETURNING a.entitlement_id, a.device_id)
	UPDATE iam_v2.entitlement_devices ed SET status='DISCONNECTED', disconnected_reason='ENTITLEMENT_ENDED'
	FROM closed WHERE ed.entitlement_id=closed.entitlement_id AND ed.device_id=closed.device_id`, ent, at)
	if err != nil {
		return 0, 0, err
	}
	// PENDING_ENFORCEMENT is ended here too. A grant whose enforcement was still converging when its
	// entitlement expired is not exempt from the expiry: leaving it behind would let the enforcement owner
	// promote it to active on the very next pass — access created by a revocation.
	ct2, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended',
			ended=GREATEST($2::timestamptz, started), end_reason='ENTITLEMENT_ENDED'
		WHERE entitlement_id=$1 AND state IN ('active','PENDING_ENFORCEMENT')`, ent, at)
	if err != nil {
		return 0, 0, err
	}
	return int(ct.RowsAffected()), int(ct2.RowsAffected()), nil
}

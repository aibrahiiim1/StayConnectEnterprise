// Package enforce derives the shaping plan netd applies and performs the expiry enforcement acctd runs.
//
//   - The plan is derived from durable state, never remembered, so it cannot drift from the database.
//   - Expiry is recorded at the true time it happened (window end or the sample that crossed the quota), not
//     when a sweep noticed.
package enforce

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

type SessionShape struct {
	SessionID     string
	EntitlementID string
	DeviceID      string
	IP            string
	MAC           string
	// Bridge is required: shaping is per-bridge, and guessing the wrong one silently shapes nobody.
	Bridge   string
	DownKbps int
	UpKbps   int
	// WindowEndsAt bounds the applier's kernel authorization lease. Nil means no wall-clock end.
	WindowEndsAt *time.Time
	// EndReason is a bounded machine code set when the plan tears a session down for a reason the durable
	// record should name; empty for an ordinary end.
	EndReason string
	// SpeedAllocation is PER_DEVICE or SHARED.
	SpeedAllocation string
}

type Plan struct {
	// Shape is every entitled live session, rated by its current entitlement's plan revision.
	Shape []SessionShape
	// Tear is every session that must have no shaping or forwarding. The caller tears down first, then shapes.
	Tear []SessionShape
}

type Enforcer struct {
	pool *pgxpool.Pool
	// aggregateTimeMaxCharge > 0 enables the AGGREGATE_ONLINE_TIME accrual tick, bounded per tick. Zero is dark.
	aggregateTimeMaxCharge int
	// dataCrossing is the LATERAL deriving the DATA-quota crossing, chosen per sweep from the catalog.
	dataCrossing string
	// phase6Writers: the expiry writer, the crossing and online_time_exhausted_at arrive and roll back
	// together, so one probe decides all three.
	phase6Writers bool
	// exhaustedAt is the exhaustion instant or a typed NULL; a missing column would fail the whole statement.
	exhaustedAt string
	// incrementalCrossing: the data-crossing checkpoint exists, so p6_data_crossing need not re-sum every record.
	incrementalCrossing bool

	mu sync.Mutex
	// expiryFailures counts consecutive sweeps in which an entitlement could not be expired.
	expiryFailures map[string]int
	// probes caches catalog probes for catalogProbeTTL.
	probes probeCache
}

// Both forms must agree on when a guest ran out of data; the first is the implementation the expiry writer calls.
const dataCrossingPhase6 = `LEFT JOIN LATERAL (SELECT iam_v2.p6_data_crossing(e.id) AS at) dat ON true`

const dataCrossingLegacy = `LEFT JOIN LATERAL (
				SELECT min(x.sampled_at) AS at FROM (
					SELECT ar.sampled_at,
					       sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
					FROM iam_v2.accounting_records ar
					JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
					  AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
					  AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
				) x
				LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
				-- The entitlement's OWN frozen allowance wins when it has one (a PER_STAY_NIGHT grant); the
				-- pinned plan revision answers for everything else, which is every entitlement granted before
				-- that mode existed. Kept identical to p6_data_crossing above: these two must never disagree
				-- about how many bytes a guest was given.
				WHERE COALESCE(e.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
				  AND x.running >= COALESCE(e.data_quota_bytes, spr.data_quota_bytes)) dat ON true`

// resolveDataCrossing probes the catalog at most once a minute, not once at startup: a migration applied or
// rolled back under a running process would otherwise leave a stale flag that stops every expiry.
func (e *Enforcer) resolveDataCrossing(ctx context.Context, tx pgx.Tx) error {
	v, err := e.probes.get("data_crossing", func() ([]bool, error) {
		var present, incremental bool
		err := tx.QueryRow(ctx,
			`SELECT to_regprocedure('iam_v2.p6_data_crossing(uuid)') IS NOT NULL
			    AND to_regprocedure('iam_v2.p6_expire_entitlement(uuid)') IS NOT NULL,
			        to_regprocedure('iam_v2.p6_advance_data_crossing(uuid,uuid)') IS NOT NULL`).Scan(&present, &incremental)
		return []bool{present, incremental}, err
	})
	if err != nil {
		return err
	}
	present, incremental := v[0], v[1]
	e.phase6Writers = present
	e.incrementalCrossing = present && incremental
	if present {
		e.dataCrossing = dataCrossingPhase6
		e.exhaustedAt = "e.online_time_exhausted_at"
	} else {
		e.dataCrossing = dataCrossingLegacy
		e.exhaustedAt = "NULL::timestamptz"
	}
	return nil
}

func New(pool *pgxpool.Pool) *Enforcer { return &Enforcer{pool: pool} }

// WithAggregateOnlineTime requires the per-tick bound: it stops an appliance switched off for hours from
// billing them on return. Use a small multiple of the sweep interval.
func (e *Enforcer) WithAggregateOnlineTime(maxChargeSeconds int) *Enforcer {
	e.aggregateTimeMaxCharge = maxChargeSeconds
	return e
}

func (e *Enforcer) Pool() *pgxpool.Pool { return e.pool }

// PlanForSite derives the plan from durable state alone. PENDING_ENFORCEMENT sessions must be included as
// desired, or the grant would wait forever for an ACTIVE nothing was asked to produce; this is safe because
// netd admits a guest only after the accountable class and the nft gate are proven.
func (e *Enforcer) PlanForSite(ctx context.Context, tenant, site string) (Plan, error) {
	var p Plan
	rows, err := e.pool.Query(ctx, `SELECT s.id::text, s.entitlement_id::text, s.device_id::text,
			COALESCE(host(s.ip),''), COALESCE(s.mac::text,''), COALESCE(s.ingress_interface,''),
			COALESCE(spr.down_kbps,0), COALESCE(spr.up_kbps,0), e.window_ends_at,
			-- The column arrived with 0059. Reading it through a catalog-independent COALESCE would be
			-- cheaper than a migration gate and would also silently produce PER_DEVICE on a database that has
			-- the column set to SHARED, so it is read directly and the migration is a deployment prerequisite.
			COALESCE(spr.speed_allocation,'PER_DEVICE'),
			(s.state IN ('active','PENDING_ENFORCEMENT') AND s.ended IS NULL AND e.status='ACTIVE'
			 AND (e.window_ends_at IS NULL OR e.window_ends_at > now())) AS entitled
		FROM iam_v2.sessions s
		JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
		LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
		WHERE s.tenant_id=$1 AND s.site_id=$2
		  AND (s.state IN ('active','PENDING_ENFORCEMENT') OR s.ended > now() - interval '1 hour')
		-- TOTAL order, not merely by time: resolveAddressOwnership below keeps the LAST session it sees for
		-- an address, so two sessions sharing a started timestamp must still arrive in one fixed order.
		ORDER BY s.started, s.id`, tenant, site)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var sh SessionShape
		var entitled bool
		if err := rows.Scan(&sh.SessionID, &sh.EntitlementID, &sh.DeviceID, &sh.IP, &sh.MAC, &sh.Bridge,
			&sh.DownKbps, &sh.UpKbps, &sh.WindowEndsAt, &sh.SpeedAllocation, &entitled); err != nil {
			return p, err
		}
		if entitled {
			p.Shape = append(p.Shape, sh)
		} else {
			// Zero rates: the edge must remove shaping, not re-apply it slower.
			sh.DownKbps, sh.UpKbps = 0, 0
			p.Tear = append(p.Tear, sh)
		}
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	return resolveAddressOwnership(retireMovedDevices(p)), nil
}

// EndReasonDeviceMoved marks a session torn down because its device is now attached at a different address;
// only the network attachment ends, never the entitlement.
const EndReasonDeviceMoved = "SUPERSEDED_ON_DEVICE_MOVE"

// retireMovedDevices ends older attachments of a device that now appears at a newer address. Otherwise the
// applier keeps authorizing the abandoned address after DHCP reissues it to another device. This is narrower
// than the applier's DHCP check, which catches a device that left and never came back.
func retireMovedDevices(p Plan) Plan {
	// Newest entry per device, by the query's total (started, id) order.
	newest := map[string]int{}
	for i, sh := range p.Shape {
		if sh.DeviceID == "" || sh.IP == "" {
			continue
		}
		newest[sh.DeviceID] = i
	}
	keep := make([]SessionShape, 0, len(p.Shape))
	for i, sh := range p.Shape {
		if sh.DeviceID == "" || sh.IP == "" {
			keep = append(keep, sh)
			continue
		}
		latest, ok := newest[sh.DeviceID]
		if !ok || latest == i {
			keep = append(keep, sh)
			continue
		}
		if p.Shape[latest].IP == sh.IP && p.Shape[latest].Bridge == sh.Bridge {
			// Same address is an ownership question, decided by resolveAddressOwnership.
			keep = append(keep, sh)
			continue
		}
		sh.DownKbps, sh.UpKbps = 0, 0
		sh.EndReason = EndReasonDeviceMoved
		p.Tear = append(p.Tear, sh)
	}
	p.Shape = keep
	return p
}

// EndReasonSupersededOnAddress marks a session torn down because a newer one took its address; distinct from
// ENFORCEMENT_TORN_DOWN because nothing failed.
const EndReasonSupersededOnAddress = "SUPERSEDED_ON_ADDRESS"

// resolveAddressOwnership makes the plan coherent per (bridge, IP): netd refuses two entitled sessions on one
// endpoint and fails both closed, leaving the address unenforced. That happens when a device authenticates as
// two rooms (entitlements are unique per subject, not per device). The newest session owns the address and older
// ones are torn down as SUPERSEDED_ON_ADDRESS; the entitlement itself is never touched (ending one would be a
// commercial decision). The query's total (started, id) order makes the outcome identical on every pass.
func resolveAddressOwnership(p Plan) Plan {
	// The last entry seen for an address is the newest, so overwriting means "newest wins".
	owners := map[string]int{}
	addressed := 0
	for i, sh := range p.Shape {
		if sh.IP == "" {
			continue
		}
		addressed++
		owners[sh.Bridge+"|"+sh.IP] = i
	}
	if len(owners) == addressed {
		return p
	}
	keep := make(map[int]bool, len(owners))
	for _, idx := range owners {
		keep[idx] = true
	}
	shape := make([]SessionShape, 0, len(owners))
	for i, sh := range p.Shape {
		if sh.IP == "" || keep[i] {
			shape = append(shape, sh)
			continue
		}
		sh.DownKbps, sh.UpKbps = 0, 0
		sh.EndReason = EndReasonSupersededOnAddress
		p.Tear = append(p.Tear, sh)
	}
	p.Shape = shape
	return p
}

type Expiry struct {
	EntitlementID string
	Reason        string // TIME | DATA | SUSPENDED_OVER_BUDGET (withheld, not ended)
	At            time.Time
	Sessions      int
	Devices       int
}

// EnforceExpiries ends live Entitlements whose window elapsed or quota was crossed, at the true time, and
// revokes their access. It is idempotent.
func (e *Enforcer) EnforceExpiries(ctx context.Context, tenant, site string) ([]Expiry, error) {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Resolve the crossing per sweep: calling p6_data_crossing unconditionally fails the whole sweep on an older
	// or rolled-back schema, and then no entitlement expires at all.
	if err := e.resolveDataCrossing(ctx, tx); err != nil {
		return nil, err
	}

	// Advance the checkpoint incrementally; re-summing every accounting record each sweep grows without bound
	// and past the statement timeout silently stops expiry.
	if e.incrementalCrossing {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.p6_advance_data_crossing($1,$2)`, tenant, site); err != nil {
			return nil, err
		}
	}

	// The candidate read must not lock: FOR UPDATE needs UPDATE on entitlements, which an accounting daemon must
	// never hold. p6_expire_entitlement locks as the definer and is idempotent, so concurrent sweeps converge.
	//
	// The earliest of window end and quota crossing wins, with the reason following the instant; otherwise an
	// entitlement crossed at T1 and expired at T2 is recorded as TIME at T2 and the aggregate tick could bill
	// T1..T2.
	rows, err := tx.Query(ctx, `SELECT id, reason, at FROM (
			SELECT e.id::text AS id,
				CASE WHEN dat.at IS NOT NULL
				          AND (win.at IS NULL OR dat.at < win.at)
				          AND (`+e.exhaustedAt+` IS NULL OR dat.at <= `+e.exhaustedAt+`)
				     THEN 'DATA' ELSE 'TIME' END AS reason,
				LEAST(COALESCE(win.at, 'infinity'::timestamptz), COALESCE(dat.at, 'infinity'::timestamptz),
				      COALESCE(`+e.exhaustedAt+`, 'infinity'::timestamptz)) AS at
			FROM iam_v2.entitlements e
			LEFT JOIN LATERAL (
				SELECT e.window_ends_at AS at
				 WHERE e.window_ends_at IS NOT NULL AND e.window_ends_at <= now()) win ON true
			-- THE ONE DATA-CROSSING IMPLEMENTATION. The sanctioned expiry writer calls this same function to
			-- establish the condition for itself, so the sweep's candidate list and the writer's verdict
			-- cannot disagree about when -- or whether -- a guest ran out of data.
			`+e.dataCrossing+`
			WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.status IN ('ACTIVE','PENDING','SUSPENDED')
			  -- An entitlement whose online-time budget was stamped exhausted is a candidate too, even
			  -- though the tick normally reports it in the same transaction. If a process died between the
			  -- stamp and the termination, the stamp is durable and this is what finds it on the next sweep;
			  -- the writer re-derives the condition anyway, so naming it here can never end a healthy one.
			  AND (win.at IS NOT NULL OR dat.at IS NOT NULL OR `+e.exhaustedAt+` IS NOT NULL)
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

	// Accrue online time only after the candidate query: the instants found above are passed in as caps, so
	// time is never charged for access that had already ended. The tick never computes a data crossing itself.
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
			// Aggregate exhaustion is a TIME termination; which rule ran out is recorded as evidence.
			x.Reason = "TIME"
			exhausted = append(exhausted, x)
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return nil, err
		}
	}

	// Merge keeping the earliest instant: the first terminal condition reached triggers one transition, and a
	// second would re-date the ending that downstream records are attributed against.
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

	// Audit P4 M3: each entitlement is ended under its own savepoint, so one that fails every time (a bad row, a
	// constraint, a function bug) is skipped and retried next sweep instead of rolling back, and so stopping,
	// every other expiry on the site.
	ended := due[:0:0]
	failedNow := map[string]int{}
	for i := range due {
		x := due[i]
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		if perr := e.expireOne(ctx, sp, &x); perr != nil {
			if rerr := sp.Rollback(ctx); rerr != nil {
				return nil, rerr
			}
			n := e.priorExpiryFailures(x.EntitlementID) + 1
			failedNow[x.EntitlementID] = n
			slog.Error("enforce: an entitlement could not be expired; skipped, the sweep continues",
				"entitlement", x.EntitlementID, "consecutive_failures", n, "err", perr)
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return nil, err
		}
		ended = append(ended, x)
	}
	due = ended
	// Fail closed on what cannot be dated: an aggregate entitlement at its budget whose crossing instant is
	// unprovable is suspended, not terminated, so access stops without claiming an end time. It stays a
	// candidate and converges at the true instant if evidence appears.
	if e.aggregateTimeMaxCharge > 0 && e.phase6Writers {
		srows, err := tx.Query(ctx,
			`SELECT entitlement::text, devices, sessions FROM iam_v2.p6_suspend_over_budget($1,$2)`,
			tenant, site)
		if err != nil {
			return nil, err
		}
		for srows.Next() {
			var x Expiry
			if err := srows.Scan(&x.EntitlementID, &x.Devices, &x.Sessions); err != nil {
				srows.Close()
				return nil, err
			}
			// Own reason so a caller cannot mistake it for an ending.
			x.Reason, x.At = "SUSPENDED_OVER_BUDGET", time.Now().UTC()
			due = append(due, x)
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	// Only entitlements that failed in this sweep keep a count, so one that recovered or ended elsewhere clears.
	e.mu.Lock()
	e.expiryFailures = failedNow
	e.mu.Unlock()
	return due, nil
}

// expireOne ends one due entitlement inside tx (a savepoint). The writer takes only the entitlement id and
// re-derives the condition, reason and instant from authoritative state, so a sweep cannot end a healthy
// entitlement or misdate an ending; the database's answer wins over the sweep's proposal.
//
// Without the writer (older or rolled-back schema) it falls back to the controlled boundary termination: less
// evidence, but access must still end without depending on a migration.
func (e *Enforcer) expireOne(ctx context.Context, tx pgx.Tx, x *Expiry) error {
	if !e.phase6Writers {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1,$2,$3)`,
			x.EntitlementID, x.At, x.Reason); err != nil {
			return err
		}
		d, sN, err := revoke(ctx, tx, x.EntitlementID, x.At)
		if err != nil {
			return err
		}
		x.Devices, x.Sessions = d, sN
		return nil
	}
	var gotReason string
	var gotAt time.Time
	err := tx.QueryRow(ctx,
		`SELECT reason, at, devices, sessions FROM iam_v2.p6_expire_entitlement($1)`,
		x.EntitlementID).Scan(&gotReason, &gotAt, &x.Devices, &x.Sessions)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already terminated elsewhere; claim nothing.
		x.Devices, x.Sessions = 0, 0
		return nil
	}
	if err != nil {
		return err
	}
	x.Reason, x.At = gotReason, gotAt
	return nil
}

func (e *Enforcer) priorExpiryFailures(id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.expiryFailures[id]
}

// StuckExpiryThreshold is how many consecutive sweeps an entitlement may fail to expire before it is reported.
const StuckExpiryThreshold = 3

// StuckExpiries counts entitlements whose expiry failed in at least StuckExpiryThreshold consecutive sweeps,
// so the caller can surface them on health (audit P4 M3).
func (e *Enforcer) StuckExpiries() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, c := range e.expiryFailures {
		if c >= StuckExpiryThreshold {
			n++
		}
	}
	return n
}

// revoke closes open authorization intervals and ends live sessions at the same instant.
func revoke(ctx context.Context, tx pgx.Tx, ent string, at time.Time) (int, int, error) {
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
	// PENDING_ENFORCEMENT must end too, or the enforcement owner would promote it to active: access created by a revocation.
	ct2, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended',
			ended=GREATEST($2::timestamptz, started), end_reason='ENTITLEMENT_ENDED'
		WHERE entitlement_id=$1 AND state IN ('active','PENDING_ENFORCEMENT')`, ent, at)
	if err != nil {
		return 0, 0, err
	}
	return int(ct.RowsAffected()), int(ct2.RowsAffected()), nil
}

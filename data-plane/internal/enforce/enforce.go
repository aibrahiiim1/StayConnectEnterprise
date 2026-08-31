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
	"errors"
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
	// EndReason is why a TEAR entry must stop being enforced, as a bounded machine code. It is empty for a
	// session torn down for the ordinary reason (its access ended), and set when the plan is tearing a session
	// down for a reason the durable record should name.
	EndReason string
	// SpeedAllocation is PER_DEVICE or SHARED, from the entitlement's pinned plan revision.
	SpeedAllocation string
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
	// dataCrossing is the LATERAL that derives the DATA-quota crossing, chosen per sweep from the catalog --
	// the Phase-6 function when the schema has it, the pre-Phase-6 expression when it does not.
	dataCrossing string
	// phase6Writers records whether the Phase-6 expiry writer exists in this schema. It, the crossing and the
	// online_time_exhausted_at column arrive together (0036/0038/0041) and disappear together on a rollback,
	// so one probe decides all three.
	phase6Writers bool
	// exhaustedAt is the stamped online-time exhaustion instant, or a typed NULL on a schema without the
	// column. Referencing a column that does not exist fails the whole statement, so it is a fragment too.
	exhaustedAt string
}

// The two forms. They answer the same question; the first is the single sanctioned implementation the expiry
// writer also calls, so on a Phase-6 database the sweep's candidate list and the writer's verdict cannot
// disagree about when a guest ran out of data.
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
				JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
				WHERE spr.data_quota_bytes IS NOT NULL AND x.running >= spr.data_quota_bytes) dat ON true`

// resolveDataCrossing asks the catalog once per sweep. It is a single cheap lookup rather than a cached flag
// because a migration can be applied or rolled back under a running process, and a flag decided at startup
// would then be wrong for the rest of the process's life -- in the direction that stops every expiry.
func (e *Enforcer) resolveDataCrossing(ctx context.Context, tx pgx.Tx) error {
	var present bool
	if err := tx.QueryRow(ctx,
		`SELECT to_regprocedure('iam_v2.p6_data_crossing(uuid)') IS NOT NULL
		    AND to_regprocedure('iam_v2.p6_expire_entitlement(uuid)') IS NOT NULL`).Scan(&present); err != nil {
		return err
	}
	e.phase6Writers = present
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
			// a torn-down session carries no rates: the edge must remove its shaping, not re-apply it slower.
			sh.DownKbps, sh.UpKbps = 0, 0
			p.Tear = append(p.Tear, sh)
		}
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	return resolveAddressOwnership(retireMovedDevices(p)), nil
}

// EndReasonDeviceMoved is recorded on a session torn down because its own device is now attached at a
// different address. Nothing failed and the guest did nothing wrong: the entitlement, its purchase and its
// usage are untouched, and only the obsolete network attachment ends.
const EndReasonDeviceMoved = "SUPERSEDED_ON_DEVICE_MOVE"

// retireMovedDevices ends an attachment the device itself has abandoned.
//
// A DEVICE MOVES. DHCP hands it a new address after a lease lapses, after a reboot, after a day switched off.
// The durable Session it left behind still names the OLD address, and the applier keeps renewing kernel
// authorization for that address because durable state says the session is live. Nothing ever stops.
//
// That is not merely untidy, it is the whole exposure: DHCP reassigns the abandoned address to somebody else,
// and the new holder inherits authorization — and metering — belonging to a guest who is no longer there. It
// happened on the PRE-LIVE appliance: one iPhone authenticated on .139, was switched off for days, came back
// on .102, and .139 was reissued to a different device while the appliance went on authorizing it.
//
// So when the same device appears at a NEWER address, its older attachments end. The evidence is the device's
// own newer session, which is as authoritative as durable state gets — the device is demonstrably somewhere
// else. This is deliberately narrower than the applier's DHCP check, which catches the other case (the device
// left and never came back); the two together are what close the address.
//
// WHAT THIS DOES NOT TOUCH. The Entitlement, its Purchase, its quota and its accumulated usage all survive
// unchanged, because none of them is a property of an address. A device changing address costs the guest
// nothing and buys them nothing.
func retireMovedDevices(p Plan) Plan {
	// The newest entry per device, by the query's total (started, id) order.
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
			// Same device, same address: two sessions on ONE attachment. That is the address-ownership
			// question, not a move, and resolveAddressOwnership decides it with its own reason code.
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

// EndReasonSupersededOnAddress is recorded on a session torn down because a newer session took over its
// address. It is deliberately distinct from ENFORCEMENT_TORN_DOWN: nothing failed, and the entitlement behind
// it is untouched.
const EndReasonSupersededOnAddress = "SUPERSEDED_ON_ADDRESS"

// resolveAddressOwnership makes the plan COHERENT AT THE ADDRESS, which the applier requires and cannot fix.
//
// One (bridge, IP) is one network endpoint and one accountable tc class. Two entitled sessions claiming it is
// a state netd refuses outright — installing either would attribute the other's traffic to it — so it fails
// BOTH closed and reports a class conflict. That is the correct kernel answer and the wrong end state: the
// address stays unenforced for as long as both rows are live, and every later session on that address joins
// the pile.
//
// It is not hypothetical. A device that authenticates as one room and then as another holds two live
// entitlements — legitimately, because the uniqueness rules are per SUBJECT (one live entitlement per Stay,
// per account, per principal, per voucher) and a device is not a subject. Both sessions carry that device's
// single address, and the appliance can enforce neither.
//
// So the producer decides, rather than emitting a state the applier can only refuse: THE NEWEST SESSION OWNS
// THE ADDRESS. Older sessions on the same address are torn down and recorded as SUPERSEDED_ON_ADDRESS.
//
// What this deliberately does NOT do is touch the ENTITLEMENT. The older guest's entitlement stays ACTIVE and
// remains usable from another device or another address; only the network attachment is superseded, which is
// the only thing that was ever in conflict. Deciding that one of two live entitlements should END would be a
// commercial policy question, and this is not one — it is which row owns an IP.
//
// Ordering: the plan query orders by (started, id), a TOTAL order, so the last entitled session seen for an
// address is the newest one and the outcome is the same on every pass rather than whichever row the planner
// happened to return first.
func resolveAddressOwnership(p Plan) Plan {
	// The rows arrive in the query's total (started, id) order, so the LAST entry seen for an address is the
	// newest session on it. Overwriting as we go is therefore "the newest wins" without a second comparison.
	owners := map[string]int{}
	addressed := 0
	for i, sh := range p.Shape {
		if sh.IP == "" {
			continue // an unaddressed session cannot conflict for an address; the applier reports it separately
		}
		addressed++
		owners[sh.Bridge+"|"+sh.IP] = i
	}
	if len(owners) == addressed {
		return p // the common case: every address has exactly one claimant
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

// Expiry is one enforced ending.
type Expiry struct {
	EntitlementID string
	Reason        string // TIME | DATA | SUSPENDED_OVER_BUDGET (withheld, not ended)
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

	// THE DATA CROSSING HAS TO SURVIVE A DATABASE THAT HAS NEVER SEEN PHASE 6.
	//
	// iam_v2.p6_data_crossing arrives with migration 0038. This sweep is Phase-3/5 machinery that predates it
	// by two phases and runs on every appliance, so calling the function unconditionally made the whole of
	// expiry enforcement fail with "function iam_v2.p6_data_crossing(uuid) does not exist" the moment the
	// binary met an older schema -- which is exactly what a Phase-6 rollback produces, and what CI caught
	// against the Phase-3 migration set.
	//
	// That is worse than losing a Phase-6 feature: no entitlement expires at all, so every guest whose window
	// or quota has run out keeps their access. So the crossing is resolved per sweep against the catalog, and
	// when the function is absent the sweep falls back to the pre-Phase-6 expression it always used. Same
	// answer on a Phase-6 database, correct behaviour on one without it, and no version flag to keep in sync.
	if err := e.resolveDataCrossing(ctx, tx); err != nil {
		return nil, err
	}

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

		// ONE AUDITED OPERATION, AND IT DOES NOT TRUST THIS CALLER.
		//
		// The writer takes the entitlement id and nothing else. It establishes the terminal condition from
		// authoritative state -- the outer window, the shared data crossing, an exhausted online-time budget
		// -- takes the earliest, and terminates with THAT reason at THAT instant, recording the matching
		// Phase-6 evidence. A sweep cannot end a healthy entitlement, misdate an ending, or relabel one
		// condition as another, because it supplies none of those things.
		//
		// The reason and instant come BACK, so what this sweep reports is what the database decided rather
		// than what the sweep proposed. Where they differ, the database is right: it locked the row first.
		//
		// WITHOUT the Phase-6 writer -- a schema that predates 0041, or one it has been rolled back from --
		// the sweep takes the path it always took: the controlled boundary termination at the instant the
		// candidate query derived, then closing the authorizations and sessions. Less evidence is recorded,
		// because less exists to record; access still ends, which is the part that must never depend on a
		// migration being present.
		if !e.phase6Writers {
			if _, err := tx.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1,$2,$3)`,
				x.EntitlementID, x.At, x.Reason); err != nil {
				return nil, err
			}
			d, sN, err := revoke(ctx, tx, x.EntitlementID, x.At)
			if err != nil {
				return nil, err
			}
			x.Devices, x.Sessions = d, sN
			continue
		}
		var gotReason string
		var gotAt time.Time
		err := tx.QueryRow(ctx,
			`SELECT reason, at, devices, sessions FROM iam_v2.p6_expire_entitlement($1)`,
			x.EntitlementID).Scan(&gotReason, &gotAt, &x.Devices, &x.Sessions)
		if errors.Is(err, pgx.ErrNoRows) {
			// Already terminated -- by a concurrent sweep, or by another path entirely. Nothing was done and
			// nothing is claimed.
			x.Devices, x.Sessions = 0, 0
			continue
		}
		if err != nil {
			return nil, err
		}
		x.Reason, x.At = gotReason, gotAt
	}
	// FAIL CLOSED ON WHAT CANNOT BE DATED. An aggregate entitlement whose consumption has reached its budget
	// RIGHT NOW, and whose crossing instant no durable evidence can establish, must not keep carrying traffic
	// while the question stays open. It is suspended rather than terminated: SUSPENDED already means "not
	// entitled to forwarding, not ended", admission already refuses anything that is not ACTIVE, and the
	// shaping plan already excludes it -- so access stops without anyone claiming to know when it ended.
	//
	// It stays in the terminal-condition candidate set, so if evidence later establishes the true crossing it
	// converges through the ONE boundary path at the true instant.
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
			// Reported with its own reason so a caller cannot mistake it for an ending. It is not one, and
			// nothing durable says it is.
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

// Package checkout is the atomic Checkout conversion. In one Stay-first transaction it derives the immutable
// effective-checkout boundary from a verified durable Stay Event, sets the Stay CHECKED_OUT, terminates every
// pre-checkout Entitlement and, if the Stay held an active Entitlement at the boundary, creates one Grace
// Entitlement for the episode. Only devices authorized at the boundary are grandfathered. The audit row commits
// in the same transaction.
//
// Fail-closed: a committed checkout never leaves a pre-checkout Entitlement live. The Emergency catalog is
// read-only here (provisioned by iam_v2.bootstrap_emergency_grace). No money moves and no session is issued.
package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/grace"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

var (
	ErrStayNotFound = errors.New("checkout: stay not found in scope")
	// ErrInvalidBoundaryEvent: wrong scope, unprocessed/failed/manual, unpublished RESYNC, or older than the applied Stay state.
	ErrInvalidBoundaryEvent = errors.New("checkout: invalid boundary Stay Event")
	// ErrEmergencyCatalogUnavailable fails closed and rolls back the whole transaction.
	ErrEmergencyCatalogUnavailable = errors.New("checkout: emergency-grace catalog unavailable")
	// ErrConflictingCheckoutEvent: a duplicate checkout cited a different boundary Event than the original.
	ErrConflictingCheckoutEvent = errors.New("checkout: conflicting checkout event for an already-processed episode")
)

const (
	checkoutGracePolicyVersion  = "CHECKOUT_GRACE_V1"
	emergencyGracePolicyVersion = "EMERGENCY_GRACE_V1"
)

type Converter struct{ pool *pgxpool.Pool }

func NewConverter(pool *pgxpool.Pool) *Converter { return &Converter{pool: pool} }

// BoundarySource carries only the Stay Event identity; the Converter verifies the event in the database, so a
// caller cannot inject a bare timestamp or widen the trust window.
type BoundarySource struct {
	StayEventID string
}

// boundaryFutureSkew tolerates PMS/NTP drift; a later timestamp falls back to the server clock, recorded
// clock-suspect. Not caller-configurable.
const boundaryFutureSkew = 5 * time.Minute

// Result carries no guest credential or PII.
type Result struct {
	CheckedOut           bool
	AlreadyProcessed     bool
	GraceCreated         bool
	Trigger              grace.Trigger
	IsEmergency          bool
	ConfigInvalidAlert   bool
	BoundaryClockSuspect bool
	ManualReview         bool
	NewEntitlementID     string
	OldEntitlementID     string
	Reason               string
	BoundaryReason       string
	ConfigVersion        int64
	DevicesGrandfathered int
	SessionsRebound      int
	// DevicesRevoked / SessionsRevoked: closed at the boundary because not in the grace cohort.
	DevicesRevoked    int
	SessionsRevoked   int
	EntitlementsEnded int
}

// ConvertTx runs inside the caller's transaction (the Stay engine's), so the Checkout Event's application and its
// conversion roll back together. It opens no nested transaction.
func (c *Converter) ConvertTx(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, src BoundarySource) (Result, error) {
	return c.convertTx(ctx, tx, tenant, site, iface, stayID, src)
}

func (c *Converter) ConvertAtCheckout(ctx context.Context, tenant, site, iface, stayID string, src BoundarySource) (Result, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := c.convertTx(ctx, tx, tenant, site, iface, stayID, src)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return res, nil
}

func (c *Converter) convertTx(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, src BoundarySource) (Result, error) {
	// All four writer capabilities are opened up front.
	for _, cap := range []string{
		writerguard.CapStay, writerguard.CapCheckoutConversion,
		writerguard.CapCommerceIntent, writerguard.CapDeviceAuth,
	} {
		if err := writerguard.Open(ctx, tx, cap); err != nil {
			return Result{}, err
		}
	}
	// Lock the Stay first.
	var episode int
	var status string
	var effcoSet bool
	var lastAppliedEvent *string
	err := tx.QueryRow(ctx, `SELECT lifecycle_version, status, effective_checkout_at IS NOT NULL, last_applied_event_id::text
		FROM iam_v2.stays WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND id=$4
		FOR UPDATE`, tenant, site, iface, stayID).Scan(&episode, &status, &effcoSet, &lastAppliedEvent)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrStayNotFound
		}
		return Result{}, err
	}

	var res Result
	var boundary time.Time
	var evSeq *int64
	var evNorm *int
	switch status {
	case "IN_HOUSE":
		b, suspect, reason, seq, norm, evErr := deriveBoundary(ctx, tx, tenant, site, iface, stayID, lastAppliedEvent, src.StayEventID)
		if evErr != nil {
			return Result{}, evErr
		}
		evSeq, evNorm = seq, norm
		res.BoundaryClockSuspect = suspect
		res.BoundaryReason = reason
		if suspect {
			if err := tx.QueryRow(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=now()
				WHERE id=$1 AND status='IN_HOUSE' RETURNING effective_checkout_at`, stayID).Scan(&boundary); err != nil {
				return Result{}, err
			}
		} else {
			if err := tx.QueryRow(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=$2
				WHERE id=$1 AND status='IN_HOUSE' RETURNING effective_checkout_at`, stayID, b).Scan(&boundary); err != nil {
				return Result{}, err
			}
		}
		res.CheckedOut = true
	case "CHECKED_OUT":
		if err := tx.QueryRow(ctx, `UPDATE iam_v2.stays SET posting_allowed=false, effective_checkout_at=COALESCE(effective_checkout_at, now())
			WHERE id=$1 RETURNING effective_checkout_at`, stayID).Scan(&boundary); err != nil {
			return Result{}, err
		}
		res.BoundaryClockSuspect = !effcoSet
		res.BoundaryReason = "REENTRY_ESTABLISHED_BOUNDARY"
	default:
		res.Reason = "STAY_NOT_CHECKED_OUT"
		return res, nil
	}

	// Idempotency gate: the one-per-episode audit row marks the episode decided. A different Event for it is a
	// conflict, not a silent no-op.
	var origEvent *string
	if err := tx.QueryRow(ctx, `SELECT boundary_event_id::text FROM iam_v2.checkout_grace_audit
		WHERE tenant_id=$1 AND site_id=$2 AND stay_id=$3 AND lifecycle_version=$4`,
		tenant, site, stayID, episode).Scan(&origEvent); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	if origEvent != nil {
		if *origEvent != src.StayEventID {
			return Result{}, ErrConflictingCheckoutEvent
		}
		res.AlreadyProcessed = true
		res.Reason = "ALREADY_PROCESSED_THIS_EPISODE"
		return res, nil
	}

	// Lock the grace-config row first so the whole decision uses one snapshot a concurrent Admin publish cannot change.
	pol, gpkg, enabled, configVersion, err := lockGraceConfig(ctx, tx, tenant, site)
	if err != nil {
		return Result{}, err
	}
	res.ConfigVersion = configVersion

	// Eligibility is proven from the append-only state history and quota ledger at the boundary.
	elig, err := eligibleOriginalAtBoundary(ctx, tx, tenant, site, iface, stayID, boundary)
	if err != nil {
		return Result{}, err
	}
	res.OldEntitlementID = elig.entitlementID

	// Terminate every non-terminal pre-checkout Entitlement so none can later become usable.
	ended, err := terminateNonTerminalPreCheckout(ctx, tx, tenant, site, iface, stayID, boundary, elig.entitlementID)
	if err != nil {
		return Result{}, err
	}
	res.EntitlementsEnded = ended

	// More than one ACTIVE at the boundary needs manual review (never ORDER BY LIMIT 1); no grace is minted.
	if elig.multiActive {
		res.ManualReview = true
		res.Reason = "MULTIPLE_ACTIVE_AT_BOUNDARY_MANUAL_REVIEW"
		return res, writeAudit(ctx, tx, auditRow{tenant, site, iface, stayID, episode, "NO_GRACE", false, "NONE",
			nil, res.Reason, nil, src.StayEventID, evSeq, evNorm, res.BoundaryReason, configVersion, boundary, res.BoundaryClockSuspect})
	}

	// The configured package must match the policy exactly.
	cfgValid := gpkg != "" && validateConfiguredGraceExact(ctx, tx, tenant, site, gpkg, pol)
	cfgOK := grace.ValidatePolicy(pol, enabled, cfgValid)

	d := grace.DecideConversion(grace.ConversionRequest{
		HasActiveEntitlementAtCheckout: elig.eligible,
		AlreadyConvertedThisEpisode:    false,
		Configured:                     pol,
		ConfiguredValid:                cfgOK,
	})
	res.Reason = d.Reason
	res.IsEmergency = d.IsEmergency

	trigger := grace.Trigger("NO_GRACE")
	policyVersion := "NONE"
	var alertCode any
	var graceEntArg any

	if d.Create {
		var pkgRev, svcRev, tam string
		if d.IsEmergency {
			pkgRev, svcRev, tam, err = readEmergencyCatalog(ctx, tx, tenant, site)
			if err != nil {
				return Result{}, err
			}
			trigger = grace.TriggerEmergency
			policyVersion = emergencyGracePolicyVersion
			alertCode = "CHECKOUT_GRACE_CONFIG_INVALID"
			res.ConfigInvalidAlert = true
		} else {
			pkgRev = gpkg
			if err := tx.QueryRow(ctx, `SELECT ipr.service_plan_revision_id, spr.time_accounting_mode
				FROM iam_v2.internet_package_revisions ipr
				JOIN iam_v2.service_plan_revisions spr ON spr.tenant_id=ipr.tenant_id AND spr.site_id=ipr.site_id AND spr.id=ipr.service_plan_revision_id
				WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ipr.id=$3`, tenant, site, pkgRev).Scan(&svcRev, &tam); err != nil {
				return Result{}, err
			}
			trigger = grace.TriggerCheckoutGrace
			policyVersion = checkoutGracePolicyVersion
		}

		newEnt, err := c.createGrace(ctx, tx, createGraceArgs{
			tenant: tenant, site: site, iface: iface, stayID: stayID, episode: episode, pkgRev: pkgRev, svcRev: svcRev,
			tam: tam, trigger: trigger, policy: d.Policy, policyVersion: policyVersion, isEmergency: d.IsEmergency,
			boundary: boundary, oldEnt: nilIfEmpty(elig.entitlementID),
		})
		if err != nil {
			return Result{}, err
		}
		res.GraceCreated = true
		res.Trigger = trigger
		res.NewEntitlementID = newEnt
		graceEntArg = newEnt

		if elig.entitlementID != "" {
			// Only devices whose authorization interval contains the boundary are grandfathered.
			gf, sb, err := grandfatherBoundaryDevices(ctx, tx, tenant, site, elig.entitlementID, newEnt, boundary)
			if err != nil {
				return Result{}, err
			}
			res.DevicesGrandfathered = gf
			res.SessionsRebound = sb
		}
	}

	// Freeze the accounting evidence per pre-checkout Entitlement: samples can be ingested long after checkout,
	// and a late one is recorded as DELAYED instead of rewriting an audited decision.
	if err := freezeBoundaryWatermarks(ctx, tx, tenant, site, iface, stayID, boundary); err != nil {
		return Result{}, err
	}

	// No access may survive the boundary on a terminated Entitlement: open intervals close and bound sessions end.
	// The grace cohort is already rebound to the new Entitlement, so it keeps working without a logout.
	dr, sr, err := revokeAtBoundary(ctx, tx, tenant, site, iface, stayID, graceEntArg, boundary)
	if err != nil {
		return Result{}, err
	}
	res.DevicesRevoked = dr
	res.SessionsRevoked = sr

	return res, writeAudit(ctx, tx, auditRow{tenant, site, iface, stayID, episode, string(trigger), d.IsEmergency,
		policyVersion, alertCode, boundedReason(res.Reason), graceEntArg, src.StayEventID, evSeq, evNorm,
		res.BoundaryReason, configVersion, boundary, res.BoundaryClockSuspect})
}

// deriveBoundary returns (boundary, clockSuspect, reasonCode). Structural failures are ErrInvalidBoundaryEvent;
// timestamp problems (suspect, absent, implausibly future) fall back to the server clock, recorded suspect.
func deriveBoundary(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, lastAppliedEvent *string, eventID string) (time.Time, bool, string, *int64, *int, error) {
	if eventID == "" {
		return time.Time{}, false, "", nil, nil, ErrInvalidBoundaryEvent
	}
	var ts *time.Time
	var clkSuspect bool
	var seq int64
	var norm int
	var admission string
	var resyncGen int64
	// The source must be the APPLIED checkout (GO) event for this exact Stay/interface/scope.
	err := tx.QueryRow(ctx, `SELECT pms_timestamp_utc, clock_suspect, sequence_version, normalization_version, admission_kind, resync_generation
		FROM iam_v2.stay_events
		WHERE id=$1 AND tenant_id=$2 AND site_id=$3 AND pms_interface_id=$4 AND stay_id=$5
		  AND processing_status='APPLIED' AND event_type='GO'`,
		eventID, tenant, site, iface, stayID).Scan(&ts, &clkSuspect, &seq, &norm, &admission, &resyncGen)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, "", nil, nil, ErrInvalidBoundaryEvent
		}
		return time.Time{}, false, "", nil, nil, err
	}
	// A RESYNC event is usable only once its generation is published.
	if admission == "RESYNC" {
		var published int64
		if err := tx.QueryRow(ctx, `SELECT published_resync_generation FROM iam_v2.pms_interface_runtime
			WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`, tenant, site, iface).Scan(&published); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return time.Time{}, false, "", nil, nil, ErrInvalidBoundaryEvent
			}
			return time.Time{}, false, "", nil, nil, err
		}
		if resyncGen > published {
			return time.Time{}, false, "", nil, nil, ErrInvalidBoundaryEvent
		}
	}
	// Lineage is checked by event identity on every path, never by version (sequence_version and
	// last_applied_event_version are different domains). An absent pin cannot be verified: fail closed.
	if lastAppliedEvent == nil || *lastAppliedEvent != eventID {
		return time.Time{}, false, "", nil, nil, ErrInvalidBoundaryEvent
	}
	if clkSuspect {
		return time.Time{}, true, "EVENT_CLOCK_SUSPECT", &seq, &norm, nil
	}
	if ts == nil {
		return time.Time{}, true, "PMS_TIME_ABSENT", &seq, &norm, nil
	}
	var future bool
	if err := tx.QueryRow(ctx, `SELECT $1::timestamptz > now() + make_interval(secs => $2)`, *ts, boundaryFutureSkew.Seconds()).Scan(&future); err != nil {
		return time.Time{}, false, "", nil, nil, err
	}
	if future {
		return time.Time{}, true, "PMS_TIME_IMPLAUSIBLE_FUTURE", &seq, &norm, nil
	}
	return *ts, false, "TRUSTED_PMS_CHECKOUT_TS", &seq, &norm, nil
}

type eligibility struct {
	entitlementID string
	eligible      bool
	multiActive   bool
}

// eligibleOriginalAtBoundary finds the single non-grace Entitlement ACTIVE at the boundary; more than one sets multiActive.
func eligibleOriginalAtBoundary(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, boundary time.Time) (eligibility, error) {
	var e eligibility
	// State at boundary = latest transition with effective_at <= boundary; superseded_by IS NULL keeps a
	// corrected fact from answering.
	rows, err := tx.Query(ctx, `WITH latest AS (
		SELECT DISTINCT ON (t.entitlement_id) t.entitlement_id, t.to_state
		FROM iam_v2.entitlement_state_transitions t
		JOIN iam_v2.entitlements e ON e.id=t.entitlement_id
		WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.pms_interface_id=$3 AND e.stay_id=$4
		  AND e.end_mode <> 'GRACE_AFTER_CHECKOUT' AND t.effective_at <= $5 AND t.superseded_by IS NULL
		ORDER BY t.entitlement_id, t.effective_at DESC, t.seq DESC)
		SELECT entitlement_id::text FROM latest WHERE to_state='ACTIVE'`, tenant, site, iface, stayID, boundary)
	if err != nil {
		return e, err
	}
	var active []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return e, err
		}
		active = append(active, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return e, err
	}
	if len(active) > 1 {
		e.multiActive = true
		return e, nil
	}
	if len(active) == 1 {
		ok, err := validAtBoundary(ctx, tx, active[0], boundary)
		if err != nil {
			return e, err
		}
		if ok {
			e.entitlementID = active[0]
			e.eligible = true
		}
	}
	return e, nil
}

// validAtBoundary checks the validity window had not elapsed and the data quota was not exhausted at the boundary.
func validAtBoundary(ctx context.Context, tx pgx.Tx, entID string, boundary time.Time) (bool, error) {
	var windowOK bool
	var quota *int64
	// The entitlement's own frozen quota (PER_STAY_NIGHT) wins over the plan revision's; hence the LEFT JOIN.
	if err := tx.QueryRow(ctx, `SELECT (e.window_ends_at IS NULL OR e.window_ends_at > $2),
		       COALESCE(e.data_quota_bytes, spr.data_quota_bytes)
		FROM iam_v2.entitlements e
		LEFT JOIN iam_v2.service_plan_revisions spr ON spr.tenant_id=e.tenant_id AND spr.site_id=e.site_id AND spr.id=e.service_plan_revision_id
		WHERE e.id=$1`, entID, boundary).Scan(&windowOK, &quota); err != nil {
		return false, err
	}
	if !windowOK {
		return false, nil
	}
	if quota != nil {
		var used int64
		// Usage is attributed by binding interval, not the session's current pointer, so a rebound session's
		// pre-boundary samples stay here.
		if err := tx.QueryRow(ctx, `SELECT COALESCE(bytes_up,0) + COALESCE(bytes_down,0)
			FROM iam_v2.entitlement_usage_bytes($1,$2)`, entID, boundary).Scan(&used); err != nil {
			return false, err
		}
		if used >= *quota {
			return false, nil
		}
	}
	return true, nil
}

// terminateNonTerminalPreCheckout returns the count ended. The eligible original gets reason CONVERTED; the rest CHECKOUT.
func terminateNonTerminalPreCheckout(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, boundary time.Time, eligibleID string) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM iam_v2.entitlements
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND stay_id=$4
		  AND end_mode <> 'GRACE_AFTER_CHECKOUT' AND status IN ('PENDING','ACTIVE','SUSPENDED')
		FOR UPDATE`, tenant, site, iface, stayID)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		reason := "CHECKOUT"
		if id == eligibleID {
			reason = "CONVERTED"
		}
		// Not the ordinary append: the boundary is a past business time, so later lifecycle facts are invalidated
		// rather than the termination being clamped forward.
		if _, err := tx.Exec(ctx, `SELECT iam_v2.terminate_entitlement_at_boundary($1, $2, $3)`, id, boundary, reason); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// grandfatherBoundaryDevices copies only devices authorized at the boundary onto the grace Entitlement and
// rebinds only their boundary-valid live sessions.
func grandfatherBoundaryDevices(ctx context.Context, tx pgx.Tx, tenant, site, oldEnt, newEnt string, boundary time.Time) (int, int, error) {
	ct, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id, site_id, entitlement_id, device_id, status, grandfathered, first_authorized, last_authorized)
		SELECT ed.tenant_id, ed.site_id, $2, ed.device_id, 'AUTHORIZED', true, ed.first_authorized, ed.last_authorized
		FROM iam_v2.entitlement_devices ed
		WHERE ed.entitlement_id=$1 AND ed.status='AUTHORIZED'
		  AND EXISTS (SELECT 1 FROM iam_v2.entitlement_device_authorizations a
		              WHERE a.entitlement_id=$1 AND a.device_id=ed.device_id
		                AND a.authorized_at <= $3 AND (a.deauthorized_at IS NULL OR a.deauthorized_at > $3))`,
		oldEnt, newEnt, boundary)
	if err != nil {
		return 0, 0, err
	}
	gf := int(ct.RowsAffected())
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
		SELECT tenant_id, site_id, entitlement_id, device_id, 1, $2 FROM iam_v2.entitlement_devices WHERE entitlement_id=$1`, newEnt, boundary); err != nil {
		return 0, 0, err
	}
	// Rebind through the controlled operation so the binding history stays gapless; it attributes each
	// accounting sample to the Entitlement it was taken under.
	rows2, err := tx.Query(ctx, `SELECT s.id::text FROM iam_v2.sessions s
		WHERE s.entitlement_id=$1 AND s.state='active' AND s.started <= $3 AND (s.ended IS NULL OR s.ended > $3)
		  AND EXISTS (SELECT 1 FROM iam_v2.entitlement_devices ed WHERE ed.entitlement_id=$2 AND ed.device_id=s.device_id AND ed.grandfathered)
		FOR UPDATE OF s`, oldEnt, newEnt, boundary)
	if err != nil {
		return 0, 0, err
	}
	var sessions []string
	for rows2.Next() {
		var id string
		if err := rows2.Scan(&id); err != nil {
			rows2.Close()
			return 0, 0, err
		}
		sessions = append(sessions, id)
	}
	rows2.Close()
	if err := rows2.Err(); err != nil {
		return 0, 0, err
	}
	for _, sid := range sessions {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.rebind_session_entitlement($1,$2,$3)`, sid, newEnt, boundary); err != nil {
			return 0, 0, err
		}
	}
	return gf, len(sessions), nil
}

// freezeBoundaryWatermarks is idempotent: a re-run never moves an existing watermark.
func freezeBoundaryWatermarks(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, boundary time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_boundary_watermarks
		(tenant_id, site_id, entitlement_id, boundary_at, bytes_up, bytes_down, records_counted, latest_sampled_at)
		SELECT e.tenant_id, e.site_id, e.id, $5, u.bytes_up, u.bytes_down, u.records, u.latest_sampled_at
		FROM iam_v2.entitlements e, LATERAL iam_v2.entitlement_usage_bytes(e.id,$5) u
		WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.pms_interface_id=$3 AND e.stay_id=$4
		  AND e.end_mode <> 'GRACE_AFTER_CHECKOUT'
		ON CONFLICT (entitlement_id, boundary_at) DO NOTHING`, tenant, site, iface, stayID, boundary)
	return err
}

// revokeAtBoundary closes open intervals and ends sessions on pre-checkout Entitlements at the boundary. It runs
// on every outcome, because a terminated Entitlement must leave no live access, and skips the grace Entitlement.
func revokeAtBoundary(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, graceEnt any, boundary time.Time) (int, int, error) {
	ct, err := tx.Exec(ctx, `WITH victims AS (
		SELECT a.id FROM iam_v2.entitlement_device_authorizations a
		JOIN iam_v2.entitlements e ON e.id=a.entitlement_id
		WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.pms_interface_id=$3 AND e.stay_id=$4
		  AND e.end_mode <> 'GRACE_AFTER_CHECKOUT' AND ($5::uuid IS NULL OR e.id <> $5::uuid)
		  AND a.deauthorized_at IS NULL),
	closed AS (
		UPDATE iam_v2.entitlement_device_authorizations a SET deauthorized_at = GREATEST($6::timestamptz, a.authorized_at)
		WHERE a.id IN (SELECT id FROM victims) RETURNING a.entitlement_id, a.device_id)
	UPDATE iam_v2.entitlement_devices ed SET status='DISCONNECTED', disconnected_reason='CHECKOUT_BOUNDARY'
	FROM closed WHERE ed.entitlement_id=closed.entitlement_id AND ed.device_id=closed.device_id`,
		tenant, site, iface, stayID, graceEnt, boundary)
	if err != nil {
		return 0, 0, err
	}
	// GREATEST keeps a session opened after the boundary from recording negative online time.
	ct2, err := tx.Exec(ctx, `UPDATE iam_v2.sessions s
		SET state='ended', ended=GREATEST($6::timestamptz, s.started), end_reason='CHECKOUT_BOUNDARY'
		FROM iam_v2.entitlements e
		WHERE e.id=s.entitlement_id AND s.state='active'
		  AND e.tenant_id=$1 AND e.site_id=$2 AND e.pms_interface_id=$3 AND e.stay_id=$4
		  AND e.end_mode <> 'GRACE_AFTER_CHECKOUT' AND ($5::uuid IS NULL OR e.id <> $5::uuid)`,
		tenant, site, iface, stayID, graceEnt, boundary)
	if err != nil {
		return 0, 0, err
	}
	return int(ct.RowsAffected()), int(ct2.RowsAffected()), nil
}

type createGraceArgs struct {
	tenant, site, iface, stayID string
	episode                     int
	pkgRev, svcRev, tam         string
	trigger                     grace.Trigger
	policy                      grace.Policy
	policyVersion               string
	isEmergency                 bool
	boundary                    time.Time
	oldEnt                      any
}

func (c *Converter) createGrace(ctx context.Context, tx pgx.Tx, a createGraceArgs) (string, error) {
	var purchaseID string
	err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, trigger, amount_minor, state, checkout_episode)
		VALUES ($1,$2,$3,$4,$5,$6,0,'GRANTED',$7) RETURNING id`,
		a.tenant, a.site, a.pkgRev, a.iface, a.stayID, string(a.trigger), a.episode).Scan(&purchaseID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", err
		}
		return "", err
	}
	polJSON, err := json.Marshal(policySnapshot{Version: a.policyVersion, Policy: a.policy})
	if err != nil {
		return "", err
	}
	var newEnt string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, stay_id, pms_interface_id, purchase_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, window_ends_at, status, is_emergency_grace,
		 supersedes_entitlement_id, activated_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,'GRACE_AFTER_CHECKOUT',
		        $10::timestamptz + make_interval(secs => $11), 'ACTIVE', $12, $13, $10)
		RETURNING id`,
		a.tenant, a.site, a.stayID, a.iface, purchaseID, string(polJSON), a.svcRev, a.pkgRev, a.tam,
		a.boundary, a.policy.DurationSeconds, a.isEmergency, a.oldEnt).Scan(&newEnt); err != nil {
		return "", err
	}
	// The row is already ACTIVE; this only appends its initial history.
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1, 'ACTIVE', $2, 'GRACE_CONVERSION')`, newEnt, a.boundary); err != nil {
		return "", err
	}
	return newEnt, nil
}

type policySnapshot struct {
	Version string `json:"policy_version"`
	grace.Policy
}

type auditRow struct {
	tenant, site, iface, stayID string
	episode                     int
	trigger                     string
	isEmergency                 bool
	policyVersion               string
	alertCode                   any
	reasonCode                  string
	graceEntID                  any
	boundaryEventID             string
	boundaryEventSeq            *int64
	boundaryNormVer             *int
	boundaryReason              string
	configVersion               int64
	boundaryAt                  time.Time
	boundaryClockSuspect        bool
}

func writeAudit(ctx context.Context, tx pgx.Tx, a auditRow) error {
	_, err := tx.Exec(ctx, `INSERT INTO iam_v2.checkout_grace_audit
		(tenant_id, site_id, pms_interface_id, stay_id, lifecycle_version, trigger, is_emergency, policy_version,
		 alert_code, reason_code, grace_entitlement_id, boundary_event_id, boundary_event_seq, boundary_normalization_version,
		 boundary_reason_code, config_version, boundary_at, boundary_clock_suspect)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		a.tenant, a.site, a.iface, a.stayID, a.episode, a.trigger, a.isEmergency, a.policyVersion,
		a.alertCode, a.reasonCode, a.graceEntID, nilIfEmpty(a.boundaryEventID), a.boundaryEventSeq, a.boundaryNormVer,
		boundedReason(a.boundaryReason), a.configVersion, a.boundaryAt, a.boundaryClockSuspect)
	return err
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boundedReason(r string) string {
	if r == "" {
		return "UNSPECIFIED"
	}
	if len(r) > 64 {
		r = r[:64]
	}
	return r
}

// readEmergencyCatalog reads (never creates) the pre-provisioned catalog; absent or unhealthy fails closed with
// ErrEmergencyCatalogUnavailable.
func readEmergencyCatalog(ctx context.Context, tx pgx.Tx, tenant, site string) (string, string, string, error) {
	var health string
	if err := tx.QueryRow(ctx, `SELECT iam_v2.emergency_grace_health($1,$2)`, tenant, site).Scan(&health); err != nil {
		return "", "", "", err
	}
	if health != "OK" {
		return "", "", "", ErrEmergencyCatalogUnavailable
	}
	var pkgRev, svcRev, tam string
	if err := tx.QueryRow(ctx, `SELECT ipr.id::text, spr.id::text, spr.time_accounting_mode
		FROM iam_v2.internet_packages ip
		JOIN iam_v2.internet_package_revisions ipr ON ipr.tenant_id=ip.tenant_id AND ipr.site_id=ip.site_id AND ipr.id=ip.current_revision_id
		JOIN iam_v2.service_plan_revisions spr ON spr.tenant_id=ipr.tenant_id AND spr.site_id=ipr.site_id AND spr.id=ipr.service_plan_revision_id
		WHERE ip.tenant_id=$1 AND ip.site_id=$2 AND ip.code='__sys_emergency_grace_pkg__'`, tenant, site).Scan(&pkgRev, &svcRev, &tam); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", "", ErrEmergencyCatalogUnavailable
		}
		return "", "", "", err
	}
	return pkgRev, svcRev, tam, nil
}

// validateConfiguredGraceExact requires the package revision to match the typed site policy exactly.
func validateConfiguredGraceExact(ctx context.Context, tx pgx.Tx, tenant, site, pkgRev string, pol grace.Policy) bool {
	// Must be the same function Hotel-Admin publication uses; otherwise a policy that saved successfully could
	// silently fall back to Emergency Grace at every departure.
	var ok bool
	err := tx.QueryRow(ctx, `SELECT iam_v2.grace_package_matches_policy($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		tenant, site, pkgRev, pol.DurationSeconds, pol.DownKbps, pol.UpKbps, pol.DataQuotaBytes,
		pol.DeviceLimit, pol.DeviceLimitPolicy).Scan(&ok)
	return err == nil && ok
}

// lockGraceConfig locks the grace-config row so a concurrent Admin publish cannot give a mixed snapshot.
func lockGraceConfig(ctx context.Context, tx pgx.Tx, tenant, site string) (grace.Policy, string, bool, int64, error) {
	var (
		dur, down, up, devLim *int
		quota                 *int64
		policyKind            *string
		gpkg                  *string
		configVersion         int64
	)
	// Use p3_lock_grace_config, not SELECT ... FOR UPDATE: PostgreSQL needs UPDATE privilege to lock a row, and
	// runtime roles must not hold write privileges on the grace config (policy changes only through audited
	// publication). The definer function needs only EXECUTE and the lock is held in this transaction.
	err := tx.QueryRow(ctx, `SELECT grace_duration_seconds, grace_down_kbps, grace_up_kbps, grace_data_quota_bytes,
		grace_device_limit, grace_device_limit_policy, grace_package_revision_id, config_version
		FROM iam_v2.p3_lock_grace_config($1,$2)`, tenant, site).
		Scan(&dur, &down, &up, &quota, &devLim, &policyKind, &gpkg, &configVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return grace.Policy{}, "", false, 0, nil
		}
		return grace.Policy{}, "", false, 0, err
	}
	pkg := ""
	if gpkg != nil {
		pkg = *gpkg
	}
	enabled := dur != nil && down != nil && up != nil && quota != nil && devLim != nil && policyKind != nil
	if !enabled {
		return grace.Policy{}, pkg, false, configVersion, nil
	}
	return grace.Policy{
		DurationSeconds: *dur, DownKbps: *down, UpKbps: *up, DataQuotaBytes: *quota,
		DeviceLimit: *devLim, DeviceLimitPolicy: *policyKind,
	}, pkg, true, configVersion, nil
}

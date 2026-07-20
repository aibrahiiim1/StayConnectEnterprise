// Package checkout is the Increment-7 ATOMIC Checkout conversion: in ONE transaction it locks the Stay
// (global L1), applies the effective-checkout boundary, terminates the Stay's active Entitlement, and — when
// the Stay was entitled at the boundary — converts it into exactly ONE Checkout-Grace (or Emergency-Grace)
// Entitlement for the current lifecycle episode, rebinding the already-authorized devices and live sessions
// WITHOUT a logout/redirect. The grace Policy is PINNED at conversion (later Admin edits never rewrite an
// existing Guest's grace terms). One-conversion-per-episode + concurrent single-winner is guaranteed by the
// stay lock and, defensively, by the purchases.one_conversion_per_episode partial unique index.
//
// It moves no money (grace purchase amount = 0, state GRANTED, no settlement/PS/PA) and issues no session
// directly — it repoints existing session rows so enforcement keeps running without churn.
package checkout

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/grace"
)

// ErrStayNotFound is returned when the pinned Stay does not exist in the given tenant/site/interface scope.
var ErrStayNotFound = errors.New("checkout: stay not found in scope")

// Converter runs the atomic Checkout conversion against the site DB.
type Converter struct{ pool *pgxpool.Pool }

func NewConverter(pool *pgxpool.Pool) *Converter { return &Converter{pool: pool} }

// Result reports what the atomic conversion did. It carries no guest credential.
type Result struct {
	CheckedOut           bool          // this call flipped the Stay IN_HOUSE -> CHECKED_OUT
	AlreadyCheckedOut    bool          // the Stay was already CHECKED_OUT (idempotent re-entry)
	GraceCreated         bool          // a grace Entitlement was created this call
	Trigger              grace.Trigger // CHECKOUT_GRACE or EMERGENCY_GRACE (when GraceCreated)
	IsEmergency          bool
	ConfigInvalidAlert   bool // raise CHECKOUT_GRACE_CONFIG_INVALID
	NewEntitlementID     string
	OldEntitlementID     string
	Reason               string
	DevicesGrandfathered int
	SessionsRebound      int
}

// ConvertAtCheckout performs the whole atomic Checkout conversion for one Stay. It is idempotent: a duplicate
// checkout (Stay already CHECKED_OUT, episode already converted) creates no second grace. It never guesses the
// episode — the lifecycle_version is read from the locked Stay row.
func (c *Converter) ConvertAtCheckout(ctx context.Context, tenant, site, iface, stayID string) (Result, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	res, err := c.convertTx(ctx, tx, tenant, site, iface, stayID)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return res, nil
}

func (c *Converter) convertTx(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string) (Result, error) {
	// L1: lock the Stay row first. lifecycle_version is the authoritative episode; never supplied by a caller.
	var episode int
	var status string
	var effcoSet bool
	err := tx.QueryRow(ctx, `SELECT lifecycle_version, status, effective_checkout_at IS NOT NULL
		FROM iam_v2.stays
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND id=$4
		FOR UPDATE`, tenant, site, iface, stayID).Scan(&episode, &status, &effcoSet)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrStayNotFound
		}
		return Result{}, err
	}

	var res Result
	// Establish the CHECKED_OUT state + a stable effective-checkout boundary, order-independent of the Stay
	// engine's own CHECKOUT flip. boundary is the server clock at the real checkout, reused for the grace window.
	switch status {
	case "IN_HOUSE":
		if err := tx.QueryRow(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now()
			WHERE id=$1 AND status='IN_HOUSE' RETURNING true`, stayID).Scan(&res.CheckedOut); err != nil {
			return Result{}, err
		}
	case "CHECKED_OUT":
		res.AlreadyCheckedOut = true
		if !effcoSet { // defensive: a checked-out Stay must carry its boundary
			if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET effective_checkout_at=now() WHERE id=$1 AND effective_checkout_at IS NULL`, stayID); err != nil {
				return Result{}, err
			}
		}
	default:
		// RESERVED / other — not checked out; nothing to convert (the lifecycle guard owns the transition rules).
		res.Reason = "STAY_NOT_CHECKED_OUT"
		return res, nil
	}

	// Load the pinned grace config: typed authoritative scalars + the pinned grace package revision.
	pol, gpkg, enabled := loadGraceConfig(ctx, tx, tenant, site)
	cfgValid := grace.ValidatePolicy(pol, enabled, gpkg != "")

	// The Stay's active Entitlement at the boundary (origin/price irrelevant). Locked for the rebind.
	var oldEnt string
	err = tx.QueryRow(ctx, `SELECT id FROM iam_v2.entitlements
		WHERE tenant_id=$1 AND site_id=$2 AND stay_id=$3 AND status IN ('ACTIVE','PENDING','SUSPENDED')
		FOR UPDATE`, tenant, site, stayID).Scan(&oldEnt)
	hasActive := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	res.OldEntitlementID = oldEnt

	// already converted this episode? (defensive against the unique index; keeps idempotency cheap)
	var convd int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.purchases
		WHERE stay_id=$1 AND checkout_episode=$2 AND trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE')`,
		stayID, episode).Scan(&convd); err != nil {
		return Result{}, err
	}

	d := grace.DecideConversion(grace.ConversionRequest{
		HasActiveEntitlementAtCheckout: hasActive,
		AlreadyConvertedThisEpisode:    convd > 0,
		Configured:                     pol,
		ConfiguredValid:                cfgValid,
	})
	res.Reason = d.Reason
	res.ConfigInvalidAlert = d.ConfigInvalidAlert
	res.IsEmergency = d.IsEmergency
	if !d.Create {
		return res, nil
	}

	// A grace Entitlement/Purchase needs a pinned grace package revision for its NOT-NULL FKs. If none is
	// pinned we FAIL CLOSED: the checkout stands, no phantom entitlement is minted, and the Admin is alerted.
	if gpkg == "" {
		res.Reason = "NO_GRACE_PACKAGE_FAIL_CLOSED"
		res.ConfigInvalidAlert = true
		return res, nil
	}
	// derive the service-plan revision + time-accounting mode from the pinned grace package revision.
	var svc, tam string
	if err := tx.QueryRow(ctx, `SELECT ipr.service_plan_revision_id, spr.time_accounting_mode
		FROM iam_v2.internet_package_revisions ipr
		JOIN iam_v2.service_plan_revisions spr
		  ON spr.tenant_id=ipr.tenant_id AND spr.site_id=ipr.site_id AND spr.id=ipr.service_plan_revision_id
		WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ipr.id=$3`, tenant, site, gpkg).Scan(&svc, &tam); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			res.Reason = "GRACE_PACKAGE_INVALID_FAIL_CLOSED"
			res.ConfigInvalidAlert = true
			return res, nil
		}
		return Result{}, err
	}

	// Terminate the original Entitlement FIRST so the ent_live_stay single-live-per-Stay index frees up for the
	// new grace Entitlement (both would otherwise be live for the same Stay).
	var oldEntArg any
	if hasActive {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements
			SET status='TERMINATED', terminal_reason='CONVERTED',
			    terminated_at=(SELECT effective_checkout_at FROM iam_v2.stays WHERE id=$2)
			WHERE id=$1`, oldEnt, stayID); err != nil {
			return Result{}, err
		}
		oldEntArg = oldEnt
	}

	trigger := grace.TriggerCheckoutGrace
	if d.IsEmergency {
		trigger = grace.TriggerEmergency
	}

	// One grace Purchase per episode. The one_conversion_per_episode partial unique index makes a second
	// concurrent conversion for the same (stay, episode) fail — a defensive backstop to the Stay lock.
	var purchaseID string
	err = tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, trigger, amount_minor, state, checkout_episode)
		VALUES ($1,$2,$3,$4,$5,$6,0,'GRANTED',$7) RETURNING id`,
		tenant, site, gpkg, iface, stayID, string(trigger), episode).Scan(&purchaseID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation — concurrent episode conversion won
			res.Reason = "CONCURRENT_CONVERSION_LOST"
			return Result{}, err // roll back our tx; the winner's checkout+conversion stands
		}
		return Result{}, err
	}

	// d.Policy is the PINNED policy for this conversion (the configured policy, or the built-in Emergency
	// fallback). Use it — never the raw loaded config, which is zero-valued on the Emergency path.
	pinned := d.Policy
	polJSON, err := json.Marshal(pinned)
	if err != nil {
		return Result{}, err
	}
	// The grace Entitlement: pinned policy snapshot, window from the checkout boundary, superseding the old one,
	// counters starting at zero from the boundary. GRACE_AFTER_CHECKOUT end mode.
	var newEnt string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, stay_id, pms_interface_id, purchase_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, window_ends_at, status, is_emergency_grace,
		 supersedes_entitlement_id, activated_at)
		SELECT $1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,'GRACE_AFTER_CHECKOUT',
		       s.effective_checkout_at + make_interval(secs => $10), 'ACTIVE', $11, $12,
		       s.effective_checkout_at
		FROM iam_v2.stays s WHERE s.id=$3
		RETURNING id`,
		tenant, site, stayID, iface, purchaseID, string(polJSON), svc, gpkg, tam,
		pinned.DurationSeconds, d.IsEmergency, oldEntArg).Scan(&newEnt); err != nil {
		return Result{}, err
	}
	res.GraceCreated = true
	res.Trigger = trigger
	res.NewEntitlementID = newEnt

	if hasActive {
		// grandfather the devices authorized at the boundary onto the grace Entitlement (no new device admitted).
		ct, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
			(tenant_id, site_id, entitlement_id, device_id, status, grandfathered, first_authorized, last_authorized)
			SELECT tenant_id, site_id, $2, device_id, 'AUTHORIZED', true, first_authorized, last_authorized
			FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND status='AUTHORIZED'`, oldEnt, newEnt)
		if err != nil {
			return Result{}, err
		}
		res.DevicesGrandfathered = int(ct.RowsAffected())

		// rebind the live sessions onto the grace Entitlement WITHOUT ending them (no logout / redirect / churn).
		ct2, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET entitlement_id=$2
			WHERE entitlement_id=$1 AND state='active'`, oldEnt, newEnt)
		if err != nil {
			return Result{}, err
		}
		res.SessionsRebound = int(ct2.RowsAffected())
	}
	return res, nil
}

// loadGraceConfig reads the site's PINNED grace policy: the authoritative typed scalars (migration 0010) and the
// pinned grace package revision. enabled is true only when the typed policy is fully populated (all-or-none).
func loadGraceConfig(ctx context.Context, tx pgx.Tx, tenant, site string) (grace.Policy, string, bool) {
	var (
		dur, down, up, devLim *int
		quota                 *int64
		policyKind            *string
		gpkg                  *string
	)
	err := tx.QueryRow(ctx, `SELECT grace_duration_seconds, grace_down_kbps, grace_up_kbps, grace_data_quota_bytes,
		grace_device_limit, grace_device_limit_policy, grace_package_revision_id
		FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`, tenant, site).
		Scan(&dur, &down, &up, &quota, &devLim, &policyKind, &gpkg)
	if err != nil {
		return grace.Policy{}, "", false // no config row → unconfigured → Emergency fallback path
	}
	pkg := ""
	if gpkg != nil {
		pkg = *gpkg
	}
	enabled := dur != nil && down != nil && up != nil && quota != nil && devLim != nil && policyKind != nil
	if !enabled {
		return grace.Policy{}, pkg, false
	}
	return grace.Policy{
		DurationSeconds:   *dur,
		DownKbps:          *down,
		UpKbps:            *up,
		DataQuotaBytes:    *quota,
		DeviceLimit:       *devLim,
		DeviceLimitPolicy: *policyKind,
	}, pkg, true
}

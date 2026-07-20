// Package checkout is the Increment-7 ATOMIC Checkout conversion. In ONE transaction it locks the Stay
// (global L1), establishes the IMMUTABLE effective-checkout boundary from the trusted normalized PMS checkout
// timestamp (or a conservative clock-suspect fallback), sets the Stay CHECKED_OUT with posting_allowed=false,
// TERMINATES the Stay's boundary-active Entitlement, and — for an eligible Stay — creates exactly ONE
// Checkout-Grace (or versioned Emergency-Grace) Entitlement for the current lifecycle episode, grandfathering
// only the devices/sessions that were authorized AT the boundary and rebinding them WITHOUT a logout. It
// commits a durable, append-only, one-per-episode audit/alert row in the SAME transaction.
//
// Fail-closed ordering: a COMMITTED checkout NEVER leaves the original pre-checkout Entitlement live — the
// termination happens before (and in the same tx as) grace creation, so any pre-commit failure rolls back the
// whole operation. It moves no money (grace amount 0, state GRANTED, settlement NOT_REQUIRED, no PS/PA) and
// issues no session directly.
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
)

// ErrStayNotFound is returned when the pinned Stay does not exist in the given tenant/site/interface scope.
var ErrStayNotFound = errors.New("checkout: stay not found in scope")

// Pinned, explicit policy versions. The Emergency policy is INDEPENDENT of any Hotel-Admin configuration and
// carries its own version so a conversion's terms are auditable and reproducible.
const (
	checkoutGracePolicyVersion  = "CHECKOUT_GRACE_V1"
	emergencyGracePolicyVersion = "EMERGENCY_GRACE_V1"

	// canonical, system-owned, NON-configurable Emergency-Grace identities (reserved codes, provisioned once
	// per site). NOT a per-request/fake package — a real immutable Package/Revision that preserves Purchase
	// traceability without depending on (possibly corrupt) Hotel-Admin config.
	sysEmergencyPlanCode = "__sys_emergency_grace_plan__"
	sysEmergencyPkgCode  = "__sys_emergency_grace_pkg__"
)

// Converter runs the atomic Checkout conversion against the site DB.
type Converter struct{ pool *pgxpool.Pool }

func NewConverter(pool *pgxpool.Pool) *Converter { return &Converter{pool: pool} }

// Boundary carries the trusted normalized PMS checkout timestamp and its provenance, derived by the Stay
// engine from the durable Stay Event. When Trusted is false (PMS time unavailable / stale / clock-suspect /
// normalization-invalid) the converter applies the conservative server-clock fallback and records it as
// clock-suspect with the bounded UntrustedReason. A zero TrustedAt with Trusted=true is treated as untrusted.
type Boundary struct {
	TrustedAt       time.Time
	Trusted         bool
	UntrustedReason string // bounded machine code (^[A-Z][A-Z0-9_]{0,63}$) when !Trusted
}

// Result reports what the atomic conversion did. It carries no guest credential and no PII.
type Result struct {
	CheckedOut           bool // this call flipped the Stay IN_HOUSE -> CHECKED_OUT
	AlreadyProcessed     bool // the episode was already converted/audited (idempotent re-entry)
	GraceCreated         bool
	Trigger              grace.Trigger
	IsEmergency          bool
	ConfigInvalidAlert   bool
	BoundaryClockSuspect bool
	NewEntitlementID     string
	OldEntitlementID     string
	Reason               string
	DevicesGrandfathered int
	SessionsRebound      int
}

// ConvertAtCheckout performs the whole atomic Checkout conversion for one Stay at the trusted boundary. It is
// idempotent per lifecycle episode: a duplicate/delayed checkout preserves the established boundary and creates
// no second grace/audit.
func (c *Converter) ConvertAtCheckout(ctx context.Context, tenant, site, iface, stayID string, b Boundary) (Result, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	res, err := c.convertTx(ctx, tx, tenant, site, iface, stayID, b)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return res, nil
}

func (c *Converter) convertTx(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, b Boundary) (Result, error) {
	// L1: lock the Stay first. lifecycle_version is the authoritative episode; never supplied by a caller.
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
	var boundary time.Time
	// (item 1/4/8) establish the CHECKED_OUT state + posting_allowed=false + the IMMUTABLE boundary. A trusted
	// PMS timestamp is used verbatim; otherwise the conservative server-clock fallback is recorded clock-suspect.
	// An already-established boundary is NEVER overwritten (duplicate/delayed events preserve it).
	switch status {
	case "IN_HOUSE":
		if b.Trusted && !b.TrustedAt.IsZero() {
			if err := tx.QueryRow(ctx, `UPDATE iam_v2.stays
				SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=$2
				WHERE id=$1 AND status='IN_HOUSE' RETURNING effective_checkout_at`, stayID, b.TrustedAt).Scan(&boundary); err != nil {
				return Result{}, err
			}
		} else {
			res.BoundaryClockSuspect = true
			if err := tx.QueryRow(ctx, `UPDATE iam_v2.stays
				SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=now()
				WHERE id=$1 AND status='IN_HOUSE' RETURNING effective_checkout_at`, stayID).Scan(&boundary); err != nil {
				return Result{}, err
			}
		}
		res.CheckedOut = true
	case "CHECKED_OUT":
		// reuse the ESTABLISHED boundary; never move it. Assert posting_allowed=false defensively.
		if err := tx.QueryRow(ctx, `UPDATE iam_v2.stays
			SET posting_allowed=false, effective_checkout_at=COALESCE(effective_checkout_at, now())
			WHERE id=$1 RETURNING effective_checkout_at`, stayID).Scan(&boundary); err != nil {
			return Result{}, err
		}
		res.BoundaryClockSuspect = !effcoSet // only the defensive now() path is suspect
	default:
		res.Reason = "STAY_NOT_CHECKED_OUT"
		return res, nil
	}

	// idempotency gate: the one-per-episode audit row is the durable "already decided" marker.
	var audited int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.checkout_grace_audit
		WHERE tenant_id=$1 AND site_id=$2 AND stay_id=$3 AND lifecycle_version=$4`,
		tenant, site, stayID, episode).Scan(&audited); err != nil {
		return Result{}, err
	}
	if audited > 0 {
		res.AlreadyProcessed = true
		res.Reason = "ALREADY_PROCESSED_THIS_EPISODE"
		return res, nil
	}

	// (item 3) eligibility AT the boundary: the Stay's Entitlement that was ACTIVE + valid at effective_checkout.
	// PENDING/SUSPENDED are never silently eligible. An Entitlement that was active at the boundary but expired
	// AFTER it is still eligible; one created after the boundary or exhausted at/before it is not.
	var oldEnt string
	var oldLive bool
	err = tx.QueryRow(ctx, `SELECT id, (status='ACTIVE')
		FROM iam_v2.entitlements
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND stay_id=$4
		  AND activated_at IS NOT NULL AND activated_at <= $5
		  AND (window_ends_at IS NULL OR window_ends_at > $5)
		  AND ( status='ACTIVE'
		     OR (status='TERMINATED' AND terminated_at IS NOT NULL AND terminated_at > $5
		         AND NOT (terminal_reason IN ('DATA','TIME','HARD_EXPIRY') AND terminated_at <= $5)) )
		ORDER BY activated_at DESC
		LIMIT 1
		FOR UPDATE`, tenant, site, iface, stayID, boundary).Scan(&oldEnt, &oldLive)
	hasActive := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	res.OldEntitlementID = oldEnt

	// (item 1) terminate the boundary-active original FIRST, at the boundary, so a committed checkout can never
	// leave it live — regardless of what happens with grace creation below.
	if hasActive && oldLive {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements
			SET status='TERMINATED', terminal_reason='CONVERTED', terminated_at=$2
			WHERE id=$1 AND status IN ('ACTIVE','PENDING','SUSPENDED')`, oldEnt, boundary); err != nil {
			return Result{}, err
		}
	}

	// grace config (typed authoritative scalars + pinned package) and the decision.
	pol, gpkg, enabled := loadGraceConfig(ctx, tx, tenant, site)
	// (item 6) fully revalidate the CONFIGURED grace package server-side; an invalid/unavailable one routes the
	// otherwise-eligible Guest to the versioned Emergency path.
	cfgPkgValid := gpkg != "" && validateConfiguredGracePackage(ctx, tx, tenant, site, gpkg)
	cfgValid := grace.ValidatePolicy(pol, enabled, cfgPkgValid)

	d := grace.DecideConversion(grace.ConversionRequest{
		HasActiveEntitlementAtCheckout: hasActive,
		AlreadyConvertedThisEpisode:    false, // gated by the audit row above
		Configured:                     pol,
		ConfiguredValid:                cfgValid,
	})
	res.Reason = d.Reason
	res.IsEmergency = d.IsEmergency

	trigger := grace.Trigger("NO_GRACE")
	policyVersion := "NONE"
	var alertCode any // NULL unless emergency
	var graceEntArg any

	if d.Create {
		// resolve the package + service-plan the grace Entitlement/Purchase hang off, preserving traceability.
		var pkgRev, svcRev, tam string
		if d.IsEmergency {
			// (item 2) canonical, system-owned Emergency-Grace package/revision (provisioned once per site).
			pkgRev, svcRev, tam, err = ensureEmergencyGracePackage(ctx, tx, tenant, site)
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
				JOIN iam_v2.service_plan_revisions spr
				  ON spr.tenant_id=ipr.tenant_id AND spr.site_id=ipr.site_id AND spr.id=ipr.service_plan_revision_id
				WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ipr.id=$3`, tenant, site, pkgRev).Scan(&svcRev, &tam); err != nil {
				return Result{}, err
			}
			trigger = grace.TriggerCheckoutGrace
			policyVersion = checkoutGracePolicyVersion
		}

		newEnt, err := c.createGrace(ctx, tx, createGraceArgs{
			tenant: tenant, site: site, iface: iface, stayID: stayID, episode: episode,
			pkgRev: pkgRev, svcRev: svcRev, tam: tam, trigger: trigger, policy: d.Policy, policyVersion: policyVersion,
			isEmergency: d.IsEmergency, boundary: boundary, oldEnt: oldEntOrNil(hasActive, oldEnt),
		})
		if err != nil {
			return Result{}, err
		}
		res.GraceCreated = true
		res.Trigger = trigger
		res.NewEntitlementID = newEnt
		graceEntArg = newEnt

		if hasActive {
			// (item 5) grandfather ONLY the devices authorized at/before the boundary; a device first authorized
			// AFTER the boundary is a new post-checkout device and is not grandfathered.
			ct, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
				(tenant_id, site_id, entitlement_id, device_id, status, grandfathered, first_authorized, last_authorized)
				SELECT tenant_id, site_id, $2, device_id, 'AUTHORIZED', true, first_authorized, last_authorized
				FROM iam_v2.entitlement_devices
				WHERE entitlement_id=$1 AND status='AUTHORIZED' AND first_authorized IS NOT NULL AND first_authorized <= $3`,
				oldEnt, newEnt, boundary)
			if err != nil {
				return Result{}, err
			}
			res.DevicesGrandfathered = int(ct.RowsAffected())

			// rebind ONLY the live sessions that started at/before the boundary on a grandfathered device.
			ct2, err := tx.Exec(ctx, `UPDATE iam_v2.sessions s SET entitlement_id=$2
				WHERE s.entitlement_id=$1 AND s.state='active' AND s.started <= $3
				  AND EXISTS (SELECT 1 FROM iam_v2.entitlement_devices ed
				              WHERE ed.entitlement_id=$2 AND ed.device_id=s.device_id AND ed.grandfathered)`,
				oldEnt, newEnt, boundary)
			if err != nil {
				return Result{}, err
			}
			res.SessionsRebound = int(ct2.RowsAffected())
		}
	}

	// (item 7) durable, append-only, one-per-episode audit/alert — SAME transaction as the conversion.
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.checkout_grace_audit
		(tenant_id, site_id, pms_interface_id, stay_id, lifecycle_version, trigger, is_emergency, policy_version,
		 alert_code, reason_code, grace_entitlement_id, boundary_at, boundary_clock_suspect)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		tenant, site, iface, stayID, episode, string(trigger), d.IsEmergency, policyVersion,
		alertCode, boundedReason(res.Reason), graceEntArg, boundary, res.BoundaryClockSuspect); err != nil {
		return Result{}, err
	}
	return res, nil
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

// createGrace inserts the one grace Purchase (one_conversion_per_episode backstop) + the grace Entitlement.
func (c *Converter) createGrace(ctx context.Context, tx pgx.Tx, a createGraceArgs) (string, error) {
	var purchaseID string
	err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, pms_interface_id, stay_id, trigger, amount_minor, state, checkout_episode)
		VALUES ($1,$2,$3,$4,$5,$6,0,'GRANTED',$7) RETURNING id`,
		a.tenant, a.site, a.pkgRev, a.iface, a.stayID, string(a.trigger), a.episode).Scan(&purchaseID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", err // concurrent episode conversion won — roll back; the winner's checkout stands
		}
		return "", err
	}
	snap := policySnapshot{Version: a.policyVersion, Policy: a.policy}
	polJSON, err := json.Marshal(snap)
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
	return newEnt, nil
}

// policySnapshot pins the explicit policy version alongside the grace policy in the entitlement snapshot.
type policySnapshot struct {
	Version string `json:"policy_version"`
	grace.Policy
}

func oldEntOrNil(has bool, id string) any {
	if has {
		return id
	}
	return nil
}

// boundedReason clamps a reason to the ^[A-Z][A-Z0-9_]{0,63}$ machine-code shape the audit CHECK enforces.
func boundedReason(r string) string {
	if r == "" {
		return "UNSPECIFIED"
	}
	if len(r) > 64 {
		r = r[:64]
	}
	return r
}

// validateConfiguredGracePackage fully revalidates the site's configured grace package revision server-side
// (item 6): same tenant/site, system-owned, CHECKOUT_GRACE type, the approved CURRENT/pinned revision, a
// zero price, settlement exactly NOT_REQUIRED, and an ENABLED pinned service plan. Anything else -> invalid.
func validateConfiguredGracePackage(ctx context.Context, tx pgx.Tx, tenant, site, pkgRev string) bool {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT
		ip.is_system = true
		AND ipr.package_type = 'CHECKOUT_GRACE'
		AND ip.current_revision_id = ipr.id
		AND ipr.price_minor = 0
		AND ipr.settlement_methods = ARRAY['NOT_REQUIRED']::text[]
		AND sp.enabled = true
		FROM iam_v2.internet_package_revisions ipr
		JOIN iam_v2.internet_packages ip
		  ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		JOIN iam_v2.service_plan_revisions spr
		  ON spr.tenant_id=ipr.tenant_id AND spr.site_id=ipr.site_id AND spr.id=ipr.service_plan_revision_id
		JOIN iam_v2.service_plans sp
		  ON sp.tenant_id=spr.tenant_id AND sp.site_id=spr.site_id AND sp.id=spr.service_plan_id
		WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ipr.id=$3`, tenant, site, pkgRev).Scan(&ok)
	return err == nil && ok
}

// ensureEmergencyGracePackage idempotently provisions the canonical, system-owned Emergency-Grace package
// (item 2): a real, immutable Package/Revision + Service-Plan/Revision carrying the versioned Emergency shaping,
// created once per site and reused thereafter, so Emergency conversions preserve Purchase traceability without
// depending on Hotel-Admin config and without fabricating per-request rows. Returns (packageRevID, servicePlanRevID, timeAccountingMode).
func ensureEmergencyGracePackage(ctx context.Context, tx pgx.Tx, tenant, site string) (string, string, string, error) {
	em := grace.BuiltinEmergencyPolicy()
	// service plan (system)
	var planID string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.service_plans (tenant_id, site_id, code, enabled)
		VALUES ($1,$2,$3,true) ON CONFLICT (tenant_id, site_id, code) DO NOTHING RETURNING id`,
		tenant, site, sysEmergencyPlanCode).Scan(&planID); errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `SELECT id FROM iam_v2.service_plans WHERE tenant_id=$1 AND site_id=$2 AND code=$3`,
			tenant, site, sysEmergencyPlanCode).Scan(&planID); err != nil {
			return "", "", "", err
		}
	} else if err != nil {
		return "", "", "", err
	}
	// service plan revision 1 (append-only; idempotent by (service_plan_id, revision_no))
	var svcRev string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id, site_id, service_plan_id, revision_no, name, down_kbps, up_kbps, time_accounting_mode, data_quota_bytes)
		VALUES ($1,$2,$3,1,'emergency-grace',$4,$5,'VALIDITY_WINDOW',$6)
		ON CONFLICT (service_plan_id, revision_no) DO NOTHING RETURNING id`,
		tenant, site, planID, em.DownKbps, em.UpKbps, em.DataQuotaBytes).Scan(&svcRev); errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `SELECT id FROM iam_v2.service_plan_revisions WHERE service_plan_id=$1 AND revision_no=1`, planID).Scan(&svcRev); err != nil {
			return "", "", "", err
		}
	} else if err != nil {
		return "", "", "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.service_plans SET current_revision_id=$2 WHERE id=$1 AND current_revision_id IS DISTINCT FROM $2`, planID, svcRev); err != nil {
		return "", "", "", err
	}
	// package (system)
	var pkgID string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code, is_system)
		VALUES ($1,$2,$3,true) ON CONFLICT (tenant_id, site_id, code) DO NOTHING RETURNING id`,
		tenant, site, sysEmergencyPkgCode).Scan(&pkgID); errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `SELECT id FROM iam_v2.internet_packages WHERE tenant_id=$1 AND site_id=$2 AND code=$3`,
			tenant, site, sysEmergencyPkgCode).Scan(&pkgID); err != nil {
			return "", "", "", err
		}
	} else if err != nil {
		return "", "", "", err
	}
	// package revision 1 (append-only; CHECKOUT_GRACE, price 0, settlement NOT_REQUIRED)
	var pkgRev string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type, price_minor, settlement_methods)
		VALUES ($1,$2,$3,1,$4,'CHECKOUT_GRACE',0, ARRAY['NOT_REQUIRED']::text[])
		ON CONFLICT (package_id, revision_no) DO NOTHING RETURNING id`,
		tenant, site, pkgID, svcRev).Scan(&pkgRev); errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `SELECT id FROM iam_v2.internet_package_revisions WHERE package_id=$1 AND revision_no=1`, pkgID).Scan(&pkgRev); err != nil {
			return "", "", "", err
		}
	} else if err != nil {
		return "", "", "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$2 WHERE id=$1 AND current_revision_id IS DISTINCT FROM $2`, pkgID, pkgRev); err != nil {
		return "", "", "", err
	}
	return pkgRev, svcRev, "VALIDITY_WINDOW", nil
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
		return grace.Policy{}, "", false
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

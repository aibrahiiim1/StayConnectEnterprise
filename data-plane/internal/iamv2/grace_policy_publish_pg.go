package iamv2

// The repository half of policy-driven system grace publication (D32). Kept in its own file because it is a
// distinct boundary from the operator admin repository: it may create the reserved system plan and package
// revisions, and it may do nothing else.

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (r *PgCommerceAdminRepository) WithGracePublishTx(ctx context.Context, fn func(GracePublishTx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&pgGracePublishTx{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type pgGracePublishTx struct{ tx pgx.Tx }

// EnsureSystemGracePlanRevision returns a system plan revision carrying EXACTLY the policy scalars.
//
// Revisions are immutable and the checkout matcher demands exact equality, so a policy change means a NEW
// revision rather than an edit. An identical policy reuses the existing revision: republishing the same
// policy must not fork history, and the current-revision pointer moves only when the scalars actually differ.
func (t *pgGracePublishTx) EnsureSystemGracePlanRevision(ctx context.Context, tenantID, siteID string, p SystemGracePolicy) (string, error) {
	var planID string
	if err := t.tx.QueryRow(ctx, `
	    INSERT INTO iam_v2.service_plans (tenant_id, site_id, code, enabled)
	    VALUES ($1,$2,$3,true)
	    ON CONFLICT (tenant_id, site_id, code) DO UPDATE SET enabled = true
	    RETURNING id::text`, tenantID, siteID, systemGracePlanCode).Scan(&planID); err != nil {
		return "", err
	}
	// VALIDITY_WINDOW because the matcher requires it: grace is a window after checkout, not an
	// aggregate-time budget.
	var revID string
	err := t.tx.QueryRow(ctx, `
	    SELECT id::text FROM iam_v2.service_plan_revisions
	     WHERE tenant_id=$1 AND site_id=$2 AND service_plan_id=$3
	       AND down_kbps=$4 AND up_kbps=$5 AND data_quota_bytes=$6
	       AND max_concurrent_devices=$7 AND device_limit_policy=$8
	       AND time_accounting_mode='VALIDITY_WINDOW'
	     ORDER BY revision_no DESC LIMIT 1`,
		tenantID, siteID, planID, p.DownKbps, p.UpKbps, p.DataQuotaBytes,
		p.DeviceLimit, p.DeviceLimitPolicy).Scan(&revID)
	if err == nil {
		if _, uerr := t.tx.Exec(ctx,
			`UPDATE iam_v2.service_plans SET current_revision_id=$2
			  WHERE id=$1 AND current_revision_id IS DISTINCT FROM $2`, planID, revID); uerr != nil {
			return "", uerr
		}
		return revID, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	var nextNo int
	if err := t.tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(revision_no),0)+1 FROM iam_v2.service_plan_revisions WHERE service_plan_id=$1`,
		planID).Scan(&nextNo); err != nil {
		return "", err
	}
	if err := t.tx.QueryRow(ctx, `
	    INSERT INTO iam_v2.service_plan_revisions
	      (tenant_id, site_id, service_plan_id, revision_no, name, down_kbps, up_kbps, data_quota_bytes,
	       max_concurrent_devices, device_limit_policy, time_accounting_mode)
	    VALUES ($1,$2,$3,$4,'System checkout grace',$5,$6,$7,$8,$9,'VALIDITY_WINDOW')
	    RETURNING id::text`,
		tenantID, siteID, planID, nextNo, p.DownKbps, p.UpKbps, p.DataQuotaBytes,
		p.DeviceLimit, p.DeviceLimitPolicy).Scan(&revID); err != nil {
		return "", err
	}
	if _, err := t.tx.Exec(ctx,
		`UPDATE iam_v2.service_plans SET current_revision_id=$2 WHERE id=$1`, planID, revID); err != nil {
		return "", err
	}
	return revID, nil
}

// EnsureSystemGracePackageRevision returns the CURRENT system grace package revision expressing this policy.
func (t *pgGracePublishTx) EnsureSystemGracePackageRevision(ctx context.Context, tenantID, siteID, planRevisionID string, p SystemGracePolicy) (string, error) {
	var pkgID string
	if err := t.tx.QueryRow(ctx, `
	    INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code, active, is_system)
	    VALUES ($1,$2,$3,true,true)
	    ON CONFLICT (tenant_id, site_id, code) DO UPDATE SET active = true, is_system = true
	    RETURNING id::text`, tenantID, siteID, systemGraceCode).Scan(&pkgID); err != nil {
		return "", err
	}
	dur := graceDurationPolicyJSON(p)
	var revID string
	err := t.tx.QueryRow(ctx, `
	    SELECT id::text FROM iam_v2.internet_package_revisions
	     WHERE tenant_id=$1 AND site_id=$2 AND package_id=$3
	       AND service_plan_revision_id=$4 AND package_type='CHECKOUT_GRACE'
	       AND duration_policy = $5::jsonb
	     ORDER BY revision_no DESC LIMIT 1`,
		tenantID, siteID, pkgID, planRevisionID, dur).Scan(&revID)
	if err != nil && err != pgx.ErrNoRows {
		return "", err
	}
	if err == pgx.ErrNoRows {
		var nextNo int
		if err := t.tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(revision_no),0)+1 FROM iam_v2.internet_package_revisions WHERE package_id=$1`,
			pkgID).Scan(&nextNo); err != nil {
			return "", err
		}
		if err := t.tx.QueryRow(ctx, `
		    INSERT INTO iam_v2.internet_package_revisions
		      (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type,
		       price_minor, currency, currency_exponent, settlement_methods, duration_policy, display)
		    VALUES ($1,$2,$3,$4,$5,'CHECKOUT_GRACE',0,'USD',2,'{NOT_REQUIRED}',$6::jsonb,
		            '{"name":"Checkout grace","system":true}'::jsonb)
		    RETURNING id::text`,
			tenantID, siteID, pkgID, nextNo, planRevisionID, dur).Scan(&revID); err != nil {
			return "", err
		}
	}
	// The matcher requires the pinned revision to BE the package's current one.
	if _, err := t.tx.Exec(ctx,
		`UPDATE iam_v2.internet_packages SET current_revision_id=$2
		  WHERE id=$1 AND current_revision_id IS DISTINCT FROM $2`, pkgID, revID); err != nil {
		return "", err
	}
	return revID, nil
}

func (t *pgGracePublishTx) GraceMismatchReason(ctx context.Context, tenantID, siteID, packageRevisionID string, p SystemGracePolicy) (string, error) {
	var reason *string
	if err := t.tx.QueryRow(ctx,
		`SELECT iam_v2.grace_package_mismatch_reason($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		tenantID, siteID, packageRevisionID, p.DurationSeconds, p.DownKbps, p.UpKbps,
		p.DataQuotaBytes, p.DeviceLimit, p.DeviceLimitPolicy).Scan(&reason); err != nil {
		return "", err
	}
	if reason == nil {
		return "", nil
	}
	return *reason, nil
}

// PublishGracePolicy goes through the CANONICAL audited, versioned boundary rather than the raw config
// writer: actor, bounded reason code, optimistic version, the same matcher the conversion uses, and an
// append to iam_v2.checkout_grace_policy_publications.
func (t *pgGracePublishTx) PublishGracePolicy(ctx context.Context, req GracePublishRequest, packageRevisionID string) (int, error) {
	var v int
	err := t.tx.QueryRow(ctx,
		`SELECT iam_v2.publish_checkout_grace_policy($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		req.TenantID, req.SiteID, packageRevisionID,
		req.Policy.DurationSeconds, req.Policy.DownKbps, req.Policy.UpKbps, req.Policy.DataQuotaBytes,
		req.Policy.DeviceLimit, req.Policy.DeviceLimitPolicy, req.Policy.EligibilityWindowSeconds,
		req.ExpectedVersion, req.ActorOperatorID, req.ReasonCode).Scan(&v)
	return v, err
}

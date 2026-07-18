package iamv2

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgCommerceAdminRepository is the Phase-2 Hotel-Admin repository over iam_v2. Constructed only when the
// Phase-2 admin surface is ON; while dark the engine holds a nil repository.
type PgCommerceAdminRepository struct{ db *pgxpool.Pool }

// NewPgCommerceAdminRepository builds the admin repository over a pool.
func NewPgCommerceAdminRepository(db *pgxpool.Pool) *PgCommerceAdminRepository {
	return &PgCommerceAdminRepository{db: db}
}

func (r *PgCommerceAdminRepository) WithTx(ctx context.Context, fn func(CommerceAdminTx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := fn(&pgCommerceAdminTx{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PgCommerceAdminRepository) ListPackages(ctx context.Context, tenantID, siteID string) ([]PackageSummary, error) {
	rows, err := r.db.Query(ctx,
		`SELECT p.id::text, p.code, p.active, COALESCE(p.current_revision_id::text,''),
		        (SELECT count(*) FROM iam_v2.internet_package_revisions r WHERE r.package_id = p.id)
		   FROM iam_v2.internet_packages p
		  WHERE p.tenant_id=$1 AND p.site_id=$2
		  ORDER BY p.code`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageSummary
	for rows.Next() {
		var s PackageSummary
		if err := rows.Scan(&s.PackageID, &s.Code, &s.Active, &s.CurrentRevisionID, &s.RevisionCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PgCommerceAdminRepository) ListPackageRevisions(ctx context.Context, tenantID, siteID, packageID string) ([]RevisionInfo, error) {
	rows, err := r.db.Query(ctx,
		`SELECT r.id::text, r.revision_no, (r.id = p.current_revision_id) AS is_current,
		        r.package_type, r.price_minor, r.currency
		   FROM iam_v2.internet_package_revisions r
		   JOIN iam_v2.internet_packages p ON p.id = r.package_id
		  WHERE r.tenant_id=$1 AND r.site_id=$2 AND r.package_id=$3
		  ORDER BY r.revision_no DESC`, tenantID, siteID, packageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RevisionInfo
	for rows.Next() {
		var ri RevisionInfo
		var cur *string
		if err := rows.Scan(&ri.RevisionID, &ri.RevisionNo, &ri.IsCurrent, &ri.PackageType, &ri.PriceMinor, &cur); err != nil {
			return nil, err
		}
		if cur != nil {
			ri.Currency = *cur
		}
		out = append(out, ri)
	}
	return out, rows.Err()
}

func (r *PgCommerceAdminRepository) ListPlans(ctx context.Context, tenantID, siteID string) ([]PlanSummary, error) {
	rows, err := r.db.Query(ctx,
		`SELECT p.id::text, p.code, p.enabled, COALESCE(p.current_revision_id::text,''),
		        (SELECT count(*) FROM iam_v2.service_plan_revisions r WHERE r.service_plan_id = p.id)
		   FROM iam_v2.service_plans p
		  WHERE p.tenant_id=$1 AND p.site_id=$2
		  ORDER BY p.code`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanSummary
	for rows.Next() {
		var s PlanSummary
		if err := rows.Scan(&s.PlanID, &s.Code, &s.Enabled, &s.CurrentRevisionID, &s.RevisionCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PgCommerceAdminRepository) ListPlanRevisions(ctx context.Context, tenantID, siteID, planID string) ([]RevisionInfo, error) {
	rows, err := r.db.Query(ctx,
		`SELECT r.id::text, r.revision_no, (r.id = p.current_revision_id) AS is_current, COALESCE(r.name,'')
		   FROM iam_v2.service_plan_revisions r
		   JOIN iam_v2.service_plans p ON p.id = r.service_plan_id
		  WHERE r.tenant_id=$1 AND r.site_id=$2 AND r.service_plan_id=$3
		  ORDER BY r.revision_no DESC`, tenantID, siteID, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RevisionInfo
	for rows.Next() {
		var ri RevisionInfo
		if err := rows.Scan(&ri.RevisionID, &ri.RevisionNo, &ri.IsCurrent, &ri.Label); err != nil {
			return nil, err
		}
		out = append(out, ri)
	}
	return out, rows.Err()
}

func (r *PgCommerceAdminRepository) GetGraceConfig(ctx context.Context, tenantID, siteID string) (GraceConfig, error) {
	var gc GraceConfig
	var rev *string
	var cfg []byte
	err := r.db.QueryRow(ctx,
		`SELECT grace_package_revision_id::text, config FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		tenantID, siteID).Scan(&rev, &cfg)
	if err == pgx.ErrNoRows {
		return GraceConfig{Config: map[string]any{}}, nil
	}
	if err != nil {
		return GraceConfig{}, err
	}
	if rev != nil {
		gc.GracePackageRevisionID = *rev
	}
	gc.Config = map[string]any{}
	if len(cfg) > 0 {
		_ = json.Unmarshal(cfg, &gc.Config)
	}
	return gc, nil
}

func (r *PgCommerceAdminRepository) ListQuotes(ctx context.Context, tenantID, siteID string, limit int) ([]QuoteInspect, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id::text, package_revision_id::text, price_minor, COALESCE(currency,''),
		        to_char(expires_at,'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		        to_char(consumed_at,'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		   FROM iam_v2.offer_quotes WHERE tenant_id=$1 AND site_id=$2
		  ORDER BY expires_at DESC LIMIT $3`, tenantID, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuoteInspect
	for rows.Next() {
		var q QuoteInspect
		var consumed *string
		if err := rows.Scan(&q.ID, &q.PackageRevisionID, &q.PriceMinor, &q.Currency, &q.ExpiresAt, &consumed); err != nil {
			return nil, err
		}
		q.ConsumedAt = consumed
		out = append(out, q)
	}
	return out, rows.Err()
}

func (r *PgCommerceAdminRepository) ListPurchases(ctx context.Context, tenantID, siteID string, limit int) ([]PurchaseInspect, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id::text, package_revision_id::text, state, amount_minor, COALESCE(currency,'')
		   FROM iam_v2.purchases WHERE tenant_id=$1 AND site_id=$2
		  ORDER BY id DESC LIMIT $3`, tenantID, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PurchaseInspect
	for rows.Next() {
		var p PurchaseInspect
		if err := rows.Scan(&p.ID, &p.PackageRevisionID, &p.State, &p.AmountMinor, &p.Currency); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type pgCommerceAdminTx struct{ tx pgx.Tx }

func (t *pgCommerceAdminTx) UpsertPackage(ctx context.Context, tenantID, siteID, code string) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code, active)
		 VALUES ($1,$2,$3,true)
		 ON CONFLICT (tenant_id, site_id, code) DO UPDATE SET code = EXCLUDED.code
		 RETURNING id::text`, tenantID, siteID, code).Scan(&id)
	return id, err
}

func (t *pgCommerceAdminTx) NextRevisionNo(ctx context.Context, packageID string) (int, error) {
	var n int
	err := t.tx.QueryRow(ctx,
		`SELECT COALESCE(max(revision_no),0)+1 FROM iam_v2.internet_package_revisions WHERE package_id=$1`, packageID).Scan(&n)
	return n, err
}

func (t *pgCommerceAdminTx) PlanRevisionBelongs(ctx context.Context, tenantID, siteID, planRevisionID string) (bool, error) {
	var ok bool
	err := t.tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM iam_v2.service_plan_revisions WHERE tenant_id=$1 AND site_id=$2 AND id=$3)`,
		tenantID, siteID, planRevisionID).Scan(&ok)
	return ok, err
}

// InsertPackageRevision writes a FREE (price 0 / settlement NOT_REQUIRED), non-PMS immutable revision.
func (t *pgCommerceAdminTx) InsertPackageRevision(ctx context.Context, spec PackagePublishSpec, packageID string, revNo int) (string, error) {
	display, _ := json.Marshal(orEmptyObj(spec.Display))
	duration, _ := json.Marshal(orEmptyObj(spec.DurationPolicy))
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.internet_package_revisions
		   (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type,
		    price_minor, currency, currency_exponent, settlement_methods, duration_policy,
		    visible_from, visible_until, display)
		 VALUES ($1,$2,$3,$4,$5,'GENERAL',0,'USD',2,'{NOT_REQUIRED}',$6::jsonb,$7,$8,$9::jsonb)
		 RETURNING id::text`,
		spec.TenantID, spec.SiteID, packageID, revNo, spec.ServicePlanRevisionID,
		duration, spec.VisibleFrom, spec.VisibleUntil, display).Scan(&id)
	return id, err
}

func (t *pgCommerceAdminTx) InsertEligibilityRule(ctx context.Context, tenantID, siteID, revisionID string, rule EligibilityRule) error {
	val, _ := json.Marshal(orEmptyObj(rule.Value))
	_, err := t.tx.Exec(ctx,
		`INSERT INTO iam_v2.package_eligibility_rules (tenant_id, site_id, package_revision_id, rule_type, rule_value)
		 VALUES ($1,$2,$3,$4,$5::jsonb)`, tenantID, siteID, revisionID, rule.Type, val)
	return err
}

func (t *pgCommerceAdminTx) InsertGrantTier(ctx context.Context, tenantID, siteID, revisionID string, tier GrantTier) error {
	val, _ := json.Marshal(orEmptyObj(tier.Value))
	_, err := t.tx.Exec(ctx,
		`INSERT INTO iam_v2.package_grant_tiers (tenant_id, site_id, package_revision_id, tier_order, grant_value)
		 VALUES ($1,$2,$3,$4,$5::jsonb)`, tenantID, siteID, revisionID, tier.Order, val)
	return err
}

func (t *pgCommerceAdminTx) SetCurrentRevision(ctx context.Context, packageID, revisionID string) error {
	_, err := t.tx.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, revisionID, packageID)
	return err
}

func (t *pgCommerceAdminTx) SetPackageActive(ctx context.Context, tenantID, siteID, packageID string, active bool) error {
	_, err := t.tx.Exec(ctx,
		`UPDATE iam_v2.internet_packages SET active=$4 WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		tenantID, siteID, packageID, active)
	return err
}

// ---- service plans ----

func (t *pgCommerceAdminTx) UpsertPlan(ctx context.Context, tenantID, siteID, code string) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.service_plans (tenant_id, site_id, code, enabled)
		 VALUES ($1,$2,$3,true)
		 ON CONFLICT (tenant_id, site_id, code) DO UPDATE SET code = EXCLUDED.code
		 RETURNING id::text`, tenantID, siteID, code).Scan(&id)
	return id, err
}

func (t *pgCommerceAdminTx) NextPlanRevisionNo(ctx context.Context, planID string) (int, error) {
	var n int
	err := t.tx.QueryRow(ctx,
		`SELECT COALESCE(max(revision_no),0)+1 FROM iam_v2.service_plan_revisions WHERE service_plan_id=$1`, planID).Scan(&n)
	return n, err
}

func (t *pgCommerceAdminTx) InsertPlanRevision(ctx context.Context, spec PlanPublishSpec, planID string, revNo int) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.service_plan_revisions
		   (tenant_id, site_id, service_plan_id, revision_no, name, down_kbps, up_kbps,
		    max_concurrent_devices, device_limit_policy, idle_timeout_seconds, max_continuous_session_seconds,
		    time_accounting_mode, time_quota_seconds, data_quota_bytes)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		 RETURNING id::text`,
		spec.TenantID, spec.SiteID, planID, revNo, spec.Name, spec.DownKbps, spec.UpKbps,
		spec.MaxConcurrentDevices, spec.DeviceLimitPolicy, spec.IdleTimeoutSeconds, spec.MaxContinuousSessionSeconds,
		spec.TimeAccountingMode, spec.TimeQuotaSeconds, spec.DataQuotaBytes).Scan(&id)
	return id, err
}

func (t *pgCommerceAdminTx) SetPlanCurrentRevision(ctx context.Context, planID, revisionID string) error {
	_, err := t.tx.Exec(ctx, `UPDATE iam_v2.service_plans SET current_revision_id=$1 WHERE id=$2`, revisionID, planID)
	return err
}

// ---- grace config ----

func (t *pgCommerceAdminTx) GraceCandidateValid(ctx context.Context, tenantID, siteID, packageRevisionID string) (GraceCandidate, error) {
	var c GraceCandidate
	var cexp *int
	var settlement []string
	err := t.tx.QueryRow(ctx,
		`SELECT p.active, r.package_type, r.price_minor, COALESCE(r.currency,''), r.currency_exponent,
		        r.settlement_methods,
		        EXISTS(SELECT 1 FROM iam_v2.service_plan_revisions spr
		                WHERE spr.tenant_id=r.tenant_id AND spr.site_id=r.site_id AND spr.id=r.service_plan_revision_id)
		   FROM iam_v2.internet_package_revisions r
		   JOIN iam_v2.internet_packages p ON p.id = r.package_id
		  WHERE r.tenant_id=$1 AND r.site_id=$2 AND r.id=$3`,
		tenantID, siteID, packageRevisionID).Scan(&c.PackageActive, &c.PackageType, &c.PriceMinor, &c.Currency, &cexp, &settlement, &c.PlanRevValid)
	if err == pgx.ErrNoRows {
		return GraceCandidate{Found: false}, nil
	}
	if err != nil {
		return GraceCandidate{}, err
	}
	c.Found = true
	if cexp != nil {
		c.CurrencyExp = *cexp
	}
	c.SettlementOnly = len(settlement) == 1 && settlement[0] == "NOT_REQUIRED"
	return c, nil
}

func (t *pgCommerceAdminTx) UpsertGraceConfig(ctx context.Context, tenantID, siteID, packageRevisionID string, config map[string]any) error {
	cfg, _ := json.Marshal(orEmptyObj(config))
	_, err := t.tx.Exec(ctx,
		`INSERT INTO iam_v2.site_checkout_grace_config (tenant_id, site_id, grace_package_revision_id, config)
		 VALUES ($1,$2,$3,$4::jsonb)
		 ON CONFLICT (tenant_id, site_id) DO UPDATE SET grace_package_revision_id = EXCLUDED.grace_package_revision_id, config = EXCLUDED.config`,
		tenantID, siteID, packageRevisionID, cfg)
	return err
}

func orEmptyObj(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

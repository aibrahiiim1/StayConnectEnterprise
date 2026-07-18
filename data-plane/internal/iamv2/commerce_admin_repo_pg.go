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

func orEmptyObj(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

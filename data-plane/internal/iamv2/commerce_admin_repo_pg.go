package iamv2

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgCommerceAdminRepository struct{ db *pgxpool.Pool }

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
		packagesListSQL(), tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageSummary
	for rows.Next() {
		var s PackageSummary
		if err := rows.Scan(&s.PackageID, &s.Code, &s.Active, &s.CurrentRevisionID, &s.RevisionCount,
			&s.Name, &s.PriceMinor, &s.Currency, &s.CurrencyExponent, &s.PackageType, &s.VisibleFrom, &s.VisibleUntil,
			&s.ServicePlanID, &s.ServicePlanRevisionID, &s.ServicePlanCode, &s.ServicePlanRevisionNo,
			&s.DownKbps, &s.UpKbps, &s.MaxConcurrentDevices, &s.DeviceLimitPolicy,
			&s.TimeQuotaSeconds, &s.DataQuotaBytes, &s.SpeedAllocation,
			&s.PlanHasNewerRevision); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PgCommerceAdminRepository) ListPackageRevisions(ctx context.Context, tenantID, siteID, packageID string) ([]RevisionInfo, error) {
	rows, err := r.db.Query(ctx,
		`SELECT r.id::text, r.revision_no, (r.id = p.current_revision_id) AS is_current,
		        r.package_type, r.price_minor, r.currency, r.currency_exponent::int
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
		if err := rows.Scan(&ri.RevisionID, &ri.RevisionNo, &ri.IsCurrent, &ri.PackageType, &ri.PriceMinor, &cur, &ri.CurrencyExponent); err != nil {
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
		        (SELECT count(*) FROM iam_v2.service_plan_revisions r WHERE r.service_plan_id = p.id),
		        cur.name, cur.down_kbps, cur.up_kbps, cur.max_concurrent_devices, cur.device_limit_policy,
		        cur.idle_timeout_seconds, cur.max_continuous_session_seconds,
		        cur.time_quota_seconds, cur.data_quota_bytes, cur.time_accounting_mode,
		        cur.speed_allocation,
		        -- HOW MANY GUEST-FACING OFFERS THIS PLAN CURRENTLY DECIDES. Counted over ACTIVE packages whose
		        -- CURRENT revision pins any revision of this plan, because those are the ones whose meaning
		        -- would change if the operator repinned them to a new revision.
		        (SELECT count(*) FROM iam_v2.internet_packages ip
		           JOIN iam_v2.internet_package_revisions ipr ON ipr.id = ip.current_revision_id
		           JOIN iam_v2.service_plan_revisions psr ON psr.id = ipr.service_plan_revision_id
		          WHERE ip.tenant_id = p.tenant_id AND ip.site_id = p.site_id
		            AND ip.is_system = false AND ip.active IS TRUE
		            AND psr.service_plan_id = p.id)
		   FROM iam_v2.service_plans p
		   -- LEFT JOIN: a plan with no published revision yet is a real state, and it must still list.
		   LEFT JOIN iam_v2.service_plan_revisions cur ON cur.id = p.current_revision_id
		  WHERE p.tenant_id=$1 AND p.site_id=$2
		    -- Hide the reserved system grace plan, for the same reason ListPackages hides the system package:
		    -- it is not an operator-editable object. Showing it in the catalogue invites an operator to publish
		    -- a revision onto it (which the reserved-code guard then refuses) or to attach it to a package of
		    -- their own, and it presents system-derived internals as if they were part of the product's offer.
		    -- service_plans carries no is_system column (internet_packages does, which is why ListPackages
		    -- can filter on it), so the reserved codes are named directly -- and they come from the SAME
		    -- list the publish guard refuses, passed as an array so the two cannot disagree again.
		    AND p.code <> ALL($3::text[])
		  ORDER BY p.code`, tenantID, siteID, reservedCommerceCodes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanSummary
	for rows.Next() {
		var s PlanSummary
		if err := rows.Scan(&s.PlanID, &s.Code, &s.Enabled, &s.CurrentRevisionID, &s.RevisionCount,
			&s.Name, &s.DownKbps, &s.UpKbps, &s.MaxConcurrentDevices, &s.DeviceLimitPolicy,
			&s.IdleTimeoutSeconds, &s.MaxSessionSeconds, &s.TimeQuotaSeconds, &s.DataQuotaBytes,
			&s.TimeAccountingMode, &s.SpeedAllocation, &s.UsedByActivePackages); err != nil {
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

// GetGraceConfig projects the typed grace columns back under the keys splitGraceConfig accepts, so save -> read -> save is idempotent; omitting them would publish NULLs over the policy on the next unrelated edit.
func (r *PgCommerceAdminRepository) GetGraceConfig(ctx context.Context, tenantID, siteID string) (GraceConfig, error) {
	var gc GraceConfig
	var rev *string
	var cfg []byte
	var eligibility *int
	var duration, down, up, devLimit *int
	var quota *int64
	var devPolicy *string
	err := r.db.QueryRow(ctx,
		`SELECT grace_package_revision_id::text, config, eligibility_window_seconds,
		        grace_duration_seconds, grace_down_kbps, grace_up_kbps, grace_data_quota_bytes,
		        grace_device_limit, grace_device_limit_policy, COALESCE(config_version, 0)
		   FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		tenantID, siteID).Scan(&rev, &cfg, &eligibility, &duration, &down, &up, &quota, &devLimit, &devPolicy,
		&gc.ConfigVersion)
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
	// The free-form config jsonb is not merged into the read: grace policy is written only through the typed columns, so any keys left in it are stale and must not be shown as configuration.
	_ = cfg
	// Only non-NULL values are surfaced: an absent override must read back as absent, not as zeroes a later save would publish as a policy.
	if eligibility != nil {
		gc.Config["eligibility_window_seconds"] = *eligibility
	}
	if duration != nil {
		gc.Config["grace_duration_seconds"] = *duration
	}
	if down != nil {
		gc.Config["grace_down_kbps"] = *down
	}
	if up != nil {
		gc.Config["grace_up_kbps"] = *up
	}
	if quota != nil {
		gc.Config["grace_data_quota_bytes"] = *quota
	}
	if devLimit != nil {
		gc.Config["grace_device_limit"] = *devLimit
	}
	if devPolicy != nil {
		gc.Config["grace_device_limit_policy"] = *devPolicy
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

// reservedCommerceCodes is the one list of system-owned codes that no operator publish may land on, and every consumer must read it (two naming schemes exist: the Go constants and the SQL bootstrap). The guard refuses them with a typed error before any row is touched, instead of a DB trigger error surfacing as HTTP 500.
var reservedCommerceCodes = []string{
	systemGraceCode, systemGracePlanCode,
	"__sys_emergency_grace_pkg__", "__sys_emergency_grace_plan__",
}

func reservedCommerceCode(code string) bool {
	for _, c := range reservedCommerceCodes {
		if code == c {
			return true
		}
	}
	return false
}

const reservedCommerceCodeMsg = "reserved system grace code is not operator-publishable"

type pgCommerceAdminTx struct{ tx pgx.Tx }

func (t *pgCommerceAdminTx) UpsertPackage(ctx context.Context, tenantID, siteID, code string) (string, error) {
	// An operator publish may not reach a system package by code collision: UpsertPackage is an upsert on (tenant, site, code) and would republish the system row as an operator package.
	if reservedCommerceCode(code) {
		return "", &Error{Code: ErrInvalidInput, Msg: reservedCommerceCodeMsg}
	}
	var existingSystem bool
	if err := t.tx.QueryRow(ctx,
		`SELECT COALESCE((SELECT is_system FROM iam_v2.internet_packages
		                   WHERE tenant_id=$1 AND site_id=$2 AND code=$3), false)`,
		tenantID, siteID, code).Scan(&existingSystem); err != nil {
		return "", err
	}
	if existingSystem {
		return "", &Error{Code: ErrInvalidInput, Msg: "system package is not operator-publishable"}
	}
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code, active)
		 VALUES ($1,$2,$3,true)
		 ON CONFLICT (tenant_id, site_id, code) DO UPDATE SET code = EXCLUDED.code
		 RETURNING id::text`, tenantID, siteID, code).Scan(&id)
	return id, err
}

// CreatePackage is a new row or ErrCodeExists, never a revision of an existing package; ON CONFLICT DO NOTHING keeps two concurrent adds from landing on each other.
func (t *pgCommerceAdminTx) CreatePackage(ctx context.Context, tenantID, siteID, code string) (string, error) {
	if reservedCommerceCode(code) {
		return "", &Error{Code: ErrInvalidInput, Msg: reservedCommerceCodeMsg}
	}
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code, active)
		 VALUES ($1,$2,$3,true)
		 ON CONFLICT (tenant_id, site_id, code) DO NOTHING
		 RETURNING id::text`, tenantID, siteID, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCodeExists
	}
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

func packageTypeOr(t string) string {
	if strings.TrimSpace(t) == "" {
		return "GENERAL"
	}
	return strings.TrimSpace(t)
}

func (t *pgCommerceAdminTx) InsertPackageRevision(ctx context.Context, spec PackagePublishSpec, packageID string, revNo int) (string, error) {
	display, _ := json.Marshal(orEmptyObj(spec.Display))
	duration, _ := json.Marshal(orEmptyObj(spec.DurationPolicy))
	// A FIXED or absent allocation policy is stored as SQL NULL so revisions stay byte-identical to those published before the column existed.
	var alloc *string
	if len(spec.DataAllocationPolicy) > 0 {
		if mode, _ := spec.DataAllocationPolicy["mode"].(string); mode != "" && mode != AllocFixed {
			b, _ := json.Marshal(spec.DataAllocationPolicy)
			s := string(b)
			alloc = &s
		}
	}
	// Same NULL-for-absent rule as data_allocation_policy.
	var overrides *string
	if len(spec.PlanOverrides) > 0 {
		b, _ := json.Marshal(spec.PlanOverrides)
		s := string(b)
		overrides = &s
	}
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.internet_package_revisions
		   (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type,
		    price_minor, currency, currency_exponent, settlement_methods, duration_policy,
		    visible_from, visible_until, display, data_allocation_policy,
		    plan_overrides, renewable, max_purchases_per_stay)
		 VALUES ($1,$2,$3,$4,$5,$15,$11,$12,$13,$14::text[],$6::jsonb,$7,$8,$9::jsonb,$10::jsonb,
		         $16::jsonb,$17,$18)
		 RETURNING id::text`,
		spec.TenantID, spec.SiteID, packageID, revNo, spec.ServicePlanRevisionID,
		duration, spec.VisibleFrom, spec.VisibleUntil, display, alloc,
		spec.PriceMinor, spec.Currency, spec.CurrencyExponent, spec.AcquisitionMethods,
		packageTypeOr(spec.PackageType),
		overrides, spec.Renewable, spec.MaxPurchasesPerStay).Scan(&id)
	return id, err
}

func (t *pgCommerceAdminTx) InsertRoomChargeMapping(ctx context.Context, tenantID, siteID, revisionID string, m RoomChargeMapping) error {
	_, err := t.tx.Exec(ctx,
		`INSERT INTO iam_v2.package_settlement_mappings
		   (tenant_id, site_id, package_revision_id, pms_interface_id, mapping_revision, posting_code, tax_code, tax_rate_bp)
		 VALUES ($1,$2,$3,$4,1,$5,NULLIF($6,''),$7)`,
		tenantID, siteID, revisionID, m.PMSInterfaceID, m.PostingCode, m.TaxCode, m.TaxRateBP)
	return err
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
	// A system package's activation is not the operator's to change; the refusal sits in the write because deactivating it would silently disable checkout grace for the site.
	var isSystem bool
	if err := t.tx.QueryRow(ctx,
		`SELECT is_system FROM iam_v2.internet_packages WHERE id=$1 AND tenant_id=$2 AND site_id=$3`,
		packageID, tenantID, siteID).Scan(&isSystem); err != nil {
		return err
	}
	if isSystem {
		return &Error{Code: ErrInvalidInput, Msg: "system package activation is not operator-controlled"}
	}
	_, err := t.tx.Exec(ctx,
		`UPDATE iam_v2.internet_packages SET active=$4 WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		tenantID, siteID, packageID, active)
	return err
}

func (t *pgCommerceAdminTx) UpsertPlan(ctx context.Context, tenantID, siteID, code string) (string, error) {
	// The derived system grace plan is not operator-publishable either: a package is only as trustworthy as the plan revision it pins.
	if reservedCommerceCode(code) {
		return "", &Error{Code: ErrInvalidInput, Msg: reservedCommerceCodeMsg}
	}
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.service_plans (tenant_id, site_id, code, enabled)
		 VALUES ($1,$2,$3,true)
		 ON CONFLICT (tenant_id, site_id, code) DO UPDATE SET code = EXCLUDED.code
		 RETURNING id::text`, tenantID, siteID, code).Scan(&id)
	return id, err
}

func (t *pgCommerceAdminTx) CreatePlan(ctx context.Context, tenantID, siteID, code string) (string, error) {
	if reservedCommerceCode(code) {
		return "", &Error{Code: ErrInvalidInput, Msg: reservedCommerceCodeMsg}
	}
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.service_plans (tenant_id, site_id, code, enabled)
		 VALUES ($1,$2,$3,true)
		 ON CONFLICT (tenant_id, site_id, code) DO NOTHING
		 RETURNING id::text`, tenantID, siteID, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCodeExists
	}
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
		    time_accounting_mode, time_quota_seconds, data_quota_bytes, speed_allocation)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		 RETURNING id::text`,
		spec.TenantID, spec.SiteID, planID, revNo, spec.Name, spec.DownKbps, spec.UpKbps,
		spec.MaxConcurrentDevices, spec.DeviceLimitPolicy, spec.IdleTimeoutSeconds, spec.MaxContinuousSessionSeconds,
		spec.TimeAccountingMode, spec.TimeQuotaSeconds, spec.DataQuotaBytes, spec.SpeedAllocation).Scan(&id)
	return id, err
}

func (t *pgCommerceAdminTx) SetPlanCurrentRevision(ctx context.Context, planID, revisionID string) error {
	_, err := t.tx.Exec(ctx, `UPDATE iam_v2.service_plans SET current_revision_id=$1 WHERE id=$2`, revisionID, planID)
	return err
}

func (t *pgCommerceAdminTx) GraceCandidateValid(ctx context.Context, tenantID, siteID, packageRevisionID string) (GraceCandidate, error) {
	var c GraceCandidate
	var cexp *int
	var settlement []string
	err := t.tx.QueryRow(ctx,
		`SELECT p.active, p.is_system, r.package_type, r.price_minor, COALESCE(r.currency,''), r.currency_exponent,
		        r.settlement_methods,
		        EXISTS(SELECT 1 FROM iam_v2.service_plan_revisions spr
		                WHERE spr.tenant_id=r.tenant_id AND spr.site_id=r.site_id AND spr.id=r.service_plan_revision_id)
		   FROM iam_v2.internet_package_revisions r
		   JOIN iam_v2.internet_packages p ON p.id = r.package_id
		  WHERE r.tenant_id=$1 AND r.site_id=$2 AND r.id=$3`,
		tenantID, siteID, packageRevisionID).Scan(&c.PackageActive, &c.IsSystem, &c.PackageType, &c.PriceMinor, &c.Currency, &cexp, &settlement, &c.PlanRevValid)
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

// UpsertGraceConfig is retired and always refuses; the audited path is PublishSystemGracePolicy. Kept only to satisfy CommerceAdminTx, and it must not write an unaudited, unversioned policy.
func (t *pgCommerceAdminTx) UpsertGraceConfig(ctx context.Context, tenantID, siteID, packageRevisionID string, config map[string]any) error {
	return &Error{Code: ErrInvalidInput,
		Msg: "grace configuration must be published through PublishSystemGracePolicy (D32): the raw writer " +
			"records no actor, reason or version and derives no validated package"}
}

type graceFields struct {
	eligibilityWindowSeconds int
	durationSeconds          *int
	downKbps                 *int
	upKbps                   *int
	dataQuotaBytes           *int64
	deviceLimit              *int
	deviceLimitPolicy        *string
	rest                     map[string]any
}

// splitGraceConfig separates typed grace fields from the free-form remainder. The typed set is exactly the keys grace_config_no_dup_policy_keys forbids in config; mapping others (e.g. grace_minutes) as typed trips the all-or-none rule.
func splitGraceConfig(config map[string]any) (graceFields, error) {
	g := graceFields{eligibilityWindowSeconds: 3600, rest: map[string]any{}}
	typed := map[string]bool{
		"eligibility_window_seconds": true, "grace_duration_seconds": true,
		"grace_down_kbps": true, "grace_up_kbps": true, "grace_data_quota_bytes": true,
		"grace_device_limit": true, "grace_device_limit_policy": true,
	}
	num := func(v any) (int64, bool) {
		switch n := v.(type) {
		case json.Number:
			i, err := n.Int64()
			return i, err == nil
		case float64:
			return int64(n), n == float64(int64(n))
		case int:
			return int64(n), true
		case int64:
			return n, true
		}
		return 0, false
	}
	for k, v := range config {
		if !typed[k] {
			g.rest[k] = v
			continue
		}
		if k == "grace_device_limit_policy" {
			sv, ok := v.(string)
			if !ok {
				return g, &Error{Code: ErrInvalidInput, Msg: "grace_device_limit_policy must be a string"}
			}
			g.deviceLimitPolicy = &sv
			continue
		}
		n, ok := num(v)
		if !ok {
			return g, &Error{Code: ErrInvalidInput, Msg: "grace field " + k + " must be an integer"}
		}
		switch k {
		case "eligibility_window_seconds":
			iv := int(n)
			g.eligibilityWindowSeconds = iv
		case "grace_duration_seconds":
			iv := int(n)
			g.durationSeconds = &iv
		case "grace_down_kbps":
			iv := int(n)
			g.downKbps = &iv
		case "grace_up_kbps":
			iv := int(n)
			g.upKbps = &iv
		case "grace_data_quota_bytes":
			g.dataQuotaBytes = &n
		case "grace_device_limit":
			iv := int(n)
			g.deviceLimit = &iv
		}
	}
	// grace_all_or_none: refuse a partial specification here with a named reason rather than as an opaque CHECK violation.
	set := 0
	for _, present := range []bool{g.durationSeconds != nil, g.downKbps != nil, g.upKbps != nil,
		g.dataQuotaBytes != nil, g.deviceLimit != nil, g.deviceLimitPolicy != nil} {
		if present {
			set++
		}
	}
	if set != 0 && set != 6 {
		return g, &Error{Code: ErrInvalidInput,
			Msg: "grace override must specify all of duration, down_kbps, up_kbps, data_quota_bytes, " +
				"device_limit and device_limit_policy, or none of them"}
	}
	// The only policy the schema permits; defaulted so callers supplying the other five fields are not tripped by it.
	if set == 6 && *g.deviceLimitPolicy != "REJECT_NEW_DEVICE" {
		return g, &Error{Code: ErrInvalidInput, Msg: "grace_device_limit_policy must be REJECT_NEW_DEVICE"}
	}
	return g, nil
}

func orEmptyObj(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func (r *PgCommerceAdminRepository) WithGraceProvisionTx(ctx context.Context, fn func(GraceProvisionTx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&pgGraceProvisionTx{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type pgGraceProvisionTx struct{ tx pgx.Tx }

// Matched on is_system and the reserved code, not package_type alone: an operator CHECKOUT_GRACE package must never be adopted as the system package.
func (t *pgGraceProvisionTx) SystemGracePackage(ctx context.Context, tenantID, siteID string) (string, string, error) {
	var pkgID string
	var revID *string
	err := t.tx.QueryRow(ctx, `
	    SELECT id::text, current_revision_id::text
	      FROM iam_v2.internet_packages
	     WHERE tenant_id=$1 AND site_id=$2 AND is_system = true AND code = $3`,
		tenantID, siteID, systemGraceCode).Scan(&pkgID, &revID)
	if err == pgx.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if revID == nil {
		return pkgID, "", nil
	}
	return pkgID, *revID, nil
}

func (t *pgGraceProvisionTx) CreateSystemGracePackage(ctx context.Context, tenantID, siteID, code string) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx, `
	    INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code, active, is_system)
	    VALUES ($1,$2,$3,true,true)
	    RETURNING id::text`, tenantID, siteID, code).Scan(&id)
	return id, err
}

func (t *pgGraceProvisionTx) InsertSystemGraceRevision(ctx context.Context, tenantID, siteID, packageID, planRevisionID string, revNo int, display map[string]any) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx, `
	    INSERT INTO iam_v2.internet_package_revisions
	      (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type,
	       price_minor, currency, currency_exponent, settlement_methods, duration_policy, display)
	    VALUES ($1,$2,$3,$4,$5,'CHECKOUT_GRACE',0,'USD',2,'{NOT_REQUIRED}','{"end_mode":"MANUAL_END"}'::jsonb,$6::jsonb)
	    RETURNING id::text`,
		tenantID, siteID, packageID, revNo, planRevisionID, string(graceDisplayJSON(display))).Scan(&id)
	return id, err
}

func (t *pgGraceProvisionTx) SetCurrentRevision(ctx context.Context, packageID, revisionID string) error {
	_, err := t.tx.Exec(ctx,
		`UPDATE iam_v2.internet_packages SET current_revision_id=$2 WHERE id=$1`, packageID, revisionID)
	return err
}

// Deterministic (oldest enabled plan by code) so provisioning from the same state yields the same pin.
func (t *pgGraceProvisionTx) DefaultPlanRevisionForGrace(ctx context.Context, tenantID, siteID string) (string, error) {
	// Reserved plans are excluded: otherwise the emergency catalogue plan sorts first and gets pinned, a revision the checkout matcher refuses (PLAN_IS_EMERGENCY_CATALOG).
	var rev *string
	err := t.tx.QueryRow(ctx, `
	    SELECT p.current_revision_id::text
	      FROM iam_v2.service_plans p
	     WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.enabled = true AND p.current_revision_id IS NOT NULL
	       AND NOT (p.code = ANY($3::text[]))
	     ORDER BY p.code
	     LIMIT 1`, tenantID, siteID, reservedCommerceCodes).Scan(&rev)
	if err == pgx.ErrNoRows || rev == nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return *rev, nil
}

// GetPackageCurrent reads one package's current revision in full, including eligibility rules and grant tiers, which the authoring form hands back unchanged on save.
func (r *PgCommerceAdminRepository) GetPackageCurrent(ctx context.Context, tenantID, siteID, packageID string) (PackageCurrent, error) {
	var c PackageCurrent
	var display, duration, alloc, overrides []byte
	err := r.db.QueryRow(ctx,
		`SELECT p.id::text, p.code, p.active, cur.id::text, cur.revision_no,
		        cur.service_plan_revision_id::text, cur.package_type, cur.price_minor,
		        COALESCE(cur.currency,''), COALESCE(cur.settlement_methods, ARRAY[]::text[]),
		        COALESCE(cur.display,'{}'::jsonb), COALESCE(cur.duration_policy,'{}'::jsonb),
		        -- RFC3339, not timestamptz::text. ::text renders "2026-01-01 00:00:00+00", which is not
		        -- RFC3339: handing it back to a *time.Time field failed to decode (repin answered 400 for
		        -- every package with a sale window), and the republish a client-network replacement performs
		        -- parsed it with time.RFC3339, got an error, and DROPPED the window -- making a seasonal
		        -- package permanently visible. Rendered here once, in the format every consumer states.
		        to_char(cur.visible_from  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		        to_char(cur.visible_until AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		        COALESCE(cur.data_allocation_policy,'{}'::jsonb), cur.currency_exponent::int,
		        cur.plan_overrides, cur.renewable, cur.max_purchases_per_stay
		   FROM iam_v2.internet_packages p
		   JOIN iam_v2.internet_package_revisions cur ON cur.id = p.current_revision_id
		  WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.id=$3 AND p.is_system = false`,
		tenantID, siteID, packageID).Scan(&c.PackageID, &c.Code, &c.Active, &c.RevisionID, &c.RevisionNo,
		&c.ServicePlanRevisionID, &c.PackageType, &c.PriceMinor, &c.Currency, &c.SettlementMethods,
		&display, &duration, &c.VisibleFrom, &c.VisibleUntil, &alloc, &c.CurrencyExponent,
		&overrides, &c.Renewable, &c.MaxPurchasesPerStay)
	if err != nil {
		return PackageCurrent{}, err
	}
	// Every jsonb field is decoded number-aware: PublishRevision validators accept json.Number and refuse float64, so plain Unmarshal makes a stored value fail the gate that accepted it.
	if len(overrides) > 0 {
		c.PlanOverrides = unmarshalNumberAware(overrides)
	}
	if len(display) > 0 {
		c.Display = unmarshalNumberAware(display)
	}
	if len(duration) > 0 {
		c.DurationPolicy = unmarshalNumberAware(duration)
	}
	if len(alloc) > 0 {
		c.DataAllocationPolicy = unmarshalNumberAware(alloc)
	}

	// Conditions come through the scoped reader: svc_edged has INSERT but no SELECT on the rules and tiers tables, so reading them directly returns 500.
	rows, err := r.db.Query(ctx, packageConditionsSQL(), tenantID, siteID, packageID)
	if err != nil {
		return PackageCurrent{}, wrapIfDenied(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var ruleType *string
		var tierOrder *int
		var raw []byte
		if err := rows.Scan(&kind, &ruleType, &tierOrder, &raw); err != nil {
			return PackageCurrent{}, wrapIfDenied(err)
		}
		// UseNumber, not plain Unmarshal: tier numbers are validated by asInt64, which refuses float64, so a tier read back could never be republished.
		val := unmarshalNumberAware(raw)
		switch kind {
		case "RULE":
			if ruleType != nil {
				c.EligibilityRules = append(c.EligibilityRules, EligibilityRule{Type: *ruleType, Value: val})
			}
		case "TIER":
			if tierOrder != nil {
				c.GrantTiers = append(c.GrantTiers, GrantTier{Order: *tierOrder, Value: val})
			}
		}
	}
	// pgx reports a denied read when rows are drained, so this return is classified too.
	if err := rows.Err(); err != nil {
		return c, wrapIfDenied(err)
	}
	rows.Close()
	mrows, err := r.db.Query(ctx,
		`SELECT pms_interface_id::text, posting_code, COALESCE(tax_code,''), tax_rate_bp
		   FROM iam_v2.package_settlement_mappings
		  WHERE tenant_id=$1 AND site_id=$2 AND package_revision_id=$3 AND retired_at IS NULL
		  ORDER BY pms_interface_id`, tenantID, siteID, c.RevisionID)
	if err != nil {
		return c, wrapIfDenied(err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var m RoomChargeMapping
		if err := mrows.Scan(&m.PMSInterfaceID, &m.PostingCode, &m.TaxCode, &m.TaxRateBP); err != nil {
			return c, err
		}
		c.RoomChargeMappings = append(c.RoomChargeMappings, m)
	}
	return c, wrapIfDenied(mrows.Err())
}

// packageConditionsSQL is a function so a test can assert it references neither protected table.
func packageConditionsSQL() string {
	return `SELECT kind, rule_type, tier_order, value
	          FROM iam_v2.p2_package_current_conditions($1::uuid, $2::uuid, $3::uuid)`
}

// ErrPackageConditionsUnreadable means the runtime role cannot read the rules or tiers; editing must refuse rather than republish a partial configuration.
var ErrPackageConditionsUnreadable = errors.New("iamv2: package conditions are not readable by this role")

func wrapIfDenied(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42501" {
		return ErrPackageConditionsUnreadable
	}
	return err
}

// packagesListSQL is a function so a test can assert the query reads neither package_eligibility_rules nor package_grant_tiers (edged has no SELECT on them).
func packagesListSQL() string {
	return `SELECT p.id::text, p.code, p.active, COALESCE(p.current_revision_id::text,''),
		        (SELECT count(*) FROM iam_v2.internet_package_revisions r WHERE r.package_id = p.id),
		        cur.display->>'name', cur.price_minor, cur.currency, cur.currency_exponent::int, cur.package_type,
		        cur.visible_from::text, cur.visible_until::text,
		        -- THE ELIGIBILITY AND TIER COUNTS ARE NOT READ HERE, AND THAT IS A PRIVILEGE FACT.
		        --
		        -- svc_edged AUTHORS iam_v2.package_eligibility_rules and iam_v2.package_grant_tiers through the
		        -- controlled SECURITY DEFINER writers, but holds no SELECT on either table. Counting them from
		        -- this list made the whole query fail with "permission denied for table
		        -- package_eligibility_rules" under the real runtime role -- a 500 on the operator's package
		        -- screen -- while every test passed as a superuser.
		        --
		        -- A privilege-guarded CASE does not help: PostgreSQL checks table permissions for every
		        -- relation the statement references, whichever branch would run. The only correct fix without
		        -- a grant is not to reference them, so the summary they fed is absent until svc_edged is
		        -- given SELECT on both.
		        spr.service_plan_id::text, spr.id::text, sp.code, spr.revision_no,
		        spr.down_kbps, spr.up_kbps, spr.max_concurrent_devices, spr.device_limit_policy,
		        spr.time_quota_seconds, spr.data_quota_bytes, spr.speed_allocation,
		        -- THE FACT THAT COST A GUEST 2 Mbps: the plan has moved on and this package has not.
		        COALESCE(sp.current_revision_id <> spr.id, false)
		   FROM iam_v2.internet_packages p
		   -- LEFT JOINs throughout: a package with no published revision, or one whose plan revision has been
		   -- removed, is a real state and must still list rather than vanish from the operator's catalogue.
		   LEFT JOIN iam_v2.internet_package_revisions cur ON cur.id = p.current_revision_id
		   LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = cur.service_plan_revision_id
		   LEFT JOIN iam_v2.service_plans sp ON sp.id = spr.service_plan_id
		  WHERE p.tenant_id=$1 AND p.site_id=$2
		    -- D32: the system grace package is HIDDEN from the operator catalogue. Excluded here rather than
		    -- filtered in the handler so every caller of the catalogue gets the same answer -- a listing that
		    -- showed it would invite an operator to edit or deactivate a package whose existence, type and
		    -- provenance are not theirs to change, and deactivating it would silently disable checkout grace.
		    AND p.is_system = false
		  ORDER BY p.code`
}

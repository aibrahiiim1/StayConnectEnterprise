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

// GetGraceConfig returns what SetGrace would have to be given to reproduce the stored configuration.
//
// THE ROUND TRIP HAS TO CLOSE. The typed grace fields live in COLUMNS and the rest lives in the config
// jsonb, so a read that returned only config would omit everything typed -- and since the admin UI reads,
// edits and writes back, the next save would send those fields as absent and publish NULLs over them. An
// ordinary edit of an unrelated key would silently erase the grace policy.
//
// That asymmetry was introduced by moving the WRITE to the typed columns without moving the read, which is
// the easy half to forget: the write is the change you are making, the read is the one that has always
// worked. So the typed columns are projected back under the same key names splitGraceConfig accepts, making
// save -> read -> save idempotent by construction rather than by care.
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
		        grace_device_limit, grace_device_limit_policy
		   FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`,
		tenantID, siteID).Scan(&rev, &cfg, &eligibility, &duration, &down, &up, &quota, &devLimit, &devPolicy)
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
	// Project the typed columns back. Only non-NULL values are surfaced: an absent grace override must read
	// back as absent, not as a set of zeroes that a later save would publish as a real policy.
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

// UpsertGraceConfig publishes through the accepted typed contract.
//
// iam_v2.site_checkout_grace_config carries p3_controlled_writer_only, and unlike the capability families
// (stay, auth_context, commerce_intent, ...) this table resolves to NO family: the guard permits a write only
// from the owner of
//
//	iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int)
//
// so the raw INSERT this replaces could never succeed, whatever the transaction did. Reading grace config is
// a plain SELECT, which is why the surface looked healthy right up until an operator pressed save.
//
// The reconciliation is mechanical once the contract is read as authoritative. The typed grace fields live in
// COLUMNS and are passed as parameters; whatever else the operator supplied stays in the free-form config
// jsonb. That split is not a choice -- grace_config_no_dup_policy_keys explicitly forbids the typed keys from
// also appearing in config, precisely so there is one home for each value and no way for the two to disagree.
//
// grace_all_or_none requires the six grace fields to be all-present or all-absent, so a partial specification
// is rejected here with a deterministic reason rather than being sent to the database to fail as a CHECK.
func (t *pgCommerceAdminTx) UpsertGraceConfig(ctx context.Context, tenantID, siteID, packageRevisionID string, config map[string]any) error {
	g, err := splitGraceConfig(config)
	if err != nil {
		return err
	}
	cfg, _ := json.Marshal(orEmptyObj(g.rest))
	_, execErr := t.tx.Exec(ctx,
		`SELECT iam_v2.publish_checkout_grace_config($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		tenantID, siteID, packageRevisionID,
		g.durationSeconds, g.downKbps, g.upKbps, g.dataQuotaBytes,
		g.deviceLimit, g.deviceLimitPolicy, g.eligibilityWindowSeconds)
	if execErr != nil {
		return execErr
	}
	// The free-form remainder is written UNCONDITIONALLY, including when it is empty.
	//
	// Skipping the write when there is nothing left over looks like a harmless optimisation and is a silent
	// data-retention bug: an operator who deletes the last free-form key sends a config with no remainder, the
	// UPDATE is skipped, and the OLD keys stay in the row. The read then returns them, the UI shows keys the
	// operator just deleted, and a later save writes them back -- a value that cannot be cleared, only
	// overwritten. Save -> read -> save has to close for the empty case too, which is exactly the case an
	// existence check omits.
	if _, err := t.tx.Exec(ctx,
		`UPDATE iam_v2.site_checkout_grace_config SET config = $3::jsonb
		  WHERE tenant_id = $1 AND site_id = $2`, tenantID, siteID, cfg); err != nil {
		return err
	}
	return nil
}

// graceFields is the typed half of a grace configuration.
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

// splitGraceConfig separates the typed grace fields from the free-form remainder.
//
// WHICH KEYS ARE TYPED IS NOT A JUDGEMENT CALL. The schema states it: grace_config_no_dup_policy_keys forbids
// exactly the typed keys from also appearing in the config jsonb, so that list IS the set of fields that live
// in columns. Everything else is free-form and belongs in config.
//
// An earlier version of this function also mapped grace_minutes onto the typed duration, on the reasonable-
// sounding grounds that it is "the friendlier spelling". It is not in the forbidden list, so it is free-form,
// and treating it as typed made a config of {"grace_minutes": 30} look like a PARTIAL typed override and get
// refused by the all-or-none rule below -- a rule doing its job on an input that never should have reached it.
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
	// grace_all_or_none: refuse a partial specification here, with a reason naming the rule, rather than
	// letting it reach the database and come back as an opaque CHECK violation.
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
	// The only policy the schema permits; defaulted so a caller supplying the other five is not tripped by a
	// value that has exactly one legal setting.
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

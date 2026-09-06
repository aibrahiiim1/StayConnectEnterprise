package iamv2

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

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
		packagesListSQL(), tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageSummary
	for rows.Next() {
		var s PackageSummary
		if err := rows.Scan(&s.PackageID, &s.Code, &s.Active, &s.CurrentRevisionID, &s.RevisionCount,
			&s.Name, &s.PriceMinor, &s.Currency, &s.PackageType, &s.VisibleFrom, &s.VisibleUntil,
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
	// The free-form `config` jsonb is deliberately NOT merged into the read.
	//
	// Under D32 the only writer of grace policy is the audited publication boundary, and it writes the typed
	// columns. Nothing can write that jsonb any more, and -- because the table is owner-only -- nothing can
	// clear it either. Any keys still in it are pre-D32 residue. Returning them would present values that no
	// longer describe the running policy, that the operator cannot change and cannot delete, alongside the ones
	// that do: a "grace_minutes: 30" sitting next to grace_duration_seconds, agreeing today and silently
	// disagreeing after the next publication. Read-only stale state shown as configuration is worse than absent
	// configuration, because it is indistinguishable from the real thing.
	//
	// The column is left in place rather than dropped: it is historical evidence of what a site was configured
	// with before the cutover, and a migration that deletes it destroys that. It simply is not the answer to
	// "what is this site's grace policy".
	_ = cfg
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

// reservedCommerceCodes are the catalogue codes no operator publish may ever land on.
//
// The first two are the D32 system grace objects, guarded here because the database trigger does not cover
// them. The last two are the EMERGENCY catalogue, which the trigger DOES cover -- and that was exactly the
// problem: the trigger refused them with a plain RAISE, the transaction wrapper collapsed it to a repository
// error, and an operator who typed a reserved code got HTTP 500 "publish failed". A refusal reported as an
// internal error tells the operator the system is broken when in fact it is working, and it is the shape that
// gets escalated as an outage. Refusing all four here makes the answer uniform and truthful, and it does so
// before any row is touched.
// reservedCommerceCodes is the ONE list of codes that belong to the system rather than to an operator.
//
// There are two naming schemes because two provisioning paths created system rows at different times: the Go
// constants (__system_checkout_grace*) and the SQL bootstrap in migration 0010
// (__sys_emergency_grace_*__). Both are live, so both are reserved.
//
// It is a slice, and every consumer reads it, because the previous arrangement had the publish GUARD
// checking all four while the plans LISTING hardcoded only two of them in its SQL. The two disagreed, and
// the half that was wrong was the one an operator actually sees: the Service Plans screen listed
// __sys_emergency_grace_plan__ as though it were theirs to edit, while publishing onto it was refused.
// A list that is read in one place cannot drift from itself.
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
	// D32: the operator publisher may not reach a SYSTEM package, including by CODE COLLISION.
	//
	// UpsertPackage is an upsert on (tenant, site, code). Publishing a package whose code happens to be the
	// reserved system grace code would land ON the system row and republish it as an operator package -- the
	// one door left open after the catalogue was hidden and activation was protected. The database's
	// reserved-code trigger guards only the EMERGENCY catalog codes, so this one is enforced here.
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
	// D32: a SYSTEM package's activation is not the operator's to change.
	//
	// The operator surface already required a reason and a password re-confirmation for deactivation, but
	// both are checks that a determined operator simply satisfies -- neither says "this package is not yours".
	// Deactivating the system grace package would silently disable checkout grace for the whole site while
	// every screen still looked normal, so the refusal belongs in the write itself rather than in the UI
	// affordances around it.
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

// ---- service plans ----

func (t *pgCommerceAdminTx) UpsertPlan(ctx context.Context, tenantID, siteID, code string) (string, error) {
	// D32: the derived system grace PLAN is not operator-publishable either. A package is only as trustworthy
	// as the plan revision it pins, so leaving the plan reachable reopens the same door one level down.
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
// UpsertGraceConfig is RETIRED (D32). It is retained only to satisfy the CommerceAdminTx interface and
// always refuses.
//
// It was the raw path: publish_checkout_grace_config with no actor, no reason code, no optimistic version and
// no audit row. Every one of those is what makes a change to what departing guests receive attributable and
// safe under two operators, so leaving a working bypass beside the canonical boundary would mean the
// guarantees hold only for callers who happened to choose the right door.
//
// The live path is iamv2.PublishSystemGracePolicy -> iam_v2.publish_checkout_grace_policy, which also derives
// the system package the checkout validator will accept. Nothing in the product calls this any more; the
// refusal exists so that if something ever does, it fails loudly rather than quietly writing an unaudited,
// unversioned policy.
func (t *pgCommerceAdminTx) UpsertGraceConfig(ctx context.Context, tenantID, siteID, packageRevisionID string, config map[string]any) error {
	return &Error{Code: ErrInvalidInput,
		Msg: "grace configuration must be published through PublishSystemGracePolicy (D32): the raw writer " +
			"records no actor, reason or version and derives no validated package"}
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

// ---- system grace provisioning (D32/T0069) --------------------------------------------------------------
//
// A narrow transactional surface: create the hidden per-site system package and its first revision, and
// nothing else. It deliberately cannot reach the grace CONFIG, because policy belongs to Hotel Admin.

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

// SystemGracePackage finds the site's existing system grace package, if any.
//
// Matched on is_system AND the reserved code, not on package_type alone: an operator package that merely
// carries the CHECKOUT_GRACE type must never be adopted as the system package, which is the same distinction
// the grace validator enforces.
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

// InsertSystemGraceRevision publishes the first CHECKOUT_GRACE revision: free, settlement-not-required, and
// pinned to an enabled plan revision -- exactly the shape the contract re-validates at every checkout.
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

// DefaultPlanRevisionForGrace picks the site's current enabled plan revision to pin.
//
// Deterministic (oldest enabled plan by code) rather than "whatever is newest", so provisioning two
// appliances from the same state produces the same pin instead of depending on publication order.
func (t *pgGraceProvisionTx) DefaultPlanRevisionForGrace(ctx context.Context, tenantID, siteID string) (string, error) {
	var rev *string
	err := t.tx.QueryRow(ctx, `
	    SELECT p.current_revision_id::text
	      FROM iam_v2.service_plans p
	     WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.enabled = true AND p.current_revision_id IS NOT NULL
	     ORDER BY p.code
	     LIMIT 1`, tenantID, siteID).Scan(&rev)
	if err == pgx.ErrNoRows || rev == nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return *rev, nil
}

// GetPackageCurrent reads one package's CURRENT revision in full — including the eligibility rules and grant
// tiers, which the authoring form must hand back unchanged when it saves.
//
// Read-only. It resolves nothing and decides nothing; the revision chain is untouched.
func (r *PgCommerceAdminRepository) GetPackageCurrent(ctx context.Context, tenantID, siteID, packageID string) (PackageCurrent, error) {
	var c PackageCurrent
	var display, duration []byte
	err := r.db.QueryRow(ctx,
		`SELECT p.id::text, p.code, p.active, cur.id::text, cur.revision_no,
		        cur.service_plan_revision_id::text, cur.package_type, cur.price_minor,
		        COALESCE(cur.currency,''), COALESCE(cur.settlement_methods, ARRAY[]::text[]),
		        COALESCE(cur.display,'{}'::jsonb), COALESCE(cur.duration_policy,'{}'::jsonb),
		        cur.visible_from::text, cur.visible_until::text
		   FROM iam_v2.internet_packages p
		   JOIN iam_v2.internet_package_revisions cur ON cur.id = p.current_revision_id
		  WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.id=$3 AND p.is_system = false`,
		tenantID, siteID, packageID).Scan(&c.PackageID, &c.Code, &c.Active, &c.RevisionID, &c.RevisionNo,
		&c.ServicePlanRevisionID, &c.PackageType, &c.PriceMinor, &c.Currency, &c.SettlementMethods,
		&display, &duration, &c.VisibleFrom, &c.VisibleUntil)
	if err != nil {
		return PackageCurrent{}, err
	}
	if len(display) > 0 {
		_ = json.Unmarshal(display, &c.Display)
	}
	if len(duration) > 0 {
		_ = json.Unmarshal(duration, &c.DurationPolicy)
	}

	// THE CONDITIONS COME THROUGH THE SCOPED READER, NOT FROM THE TABLES.
	//
	// svc_edged holds INSERT on iam_v2.package_eligibility_rules and iam_v2.package_grant_tiers and no SELECT
	// on either -- the admin service composes a package, the guest-auth service evaluates it. Reading them
	// directly here is what returned 500 on the operator's package screen. iam_v2.p2_package_current_conditions
	// answers the one question Edit needs, for the CURRENT revision of this non-system package in this tenant
	// and site, and the scoping is inside the function rather than in this argument list.
	//
	// packageConditionsSQL is named so a test can assert this path references neither table.
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
		var val map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &val)
		}
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
	// pgx reports a denied read when the rows are DRAINED rather than when the statement is sent, so this
	// return is classified too. Wrapping only the Query error is what made a permission problem surface to
	// the operator as "no current configuration for this package".
	return c, wrapIfDenied(rows.Err())
}

// packageConditionsSQL calls the scoped reader. It is a function so commerce_admin_privilege_test.go can
// assert that loading a package's conditions references neither protected table by name.
func packageConditionsSQL() string {
	return `SELECT kind, rule_type, tier_order, value
	          FROM iam_v2.p2_package_current_conditions($1::uuid, $2::uuid, $3::uuid)`
}

// ErrPackageConditionsUnreadable means this runtime role cannot read a package's eligibility rules or grant
// tiers. Editing must refuse rather than load a partial configuration it would then republish.
var ErrPackageConditionsUnreadable = errors.New("iamv2: package conditions are not readable by this role")

// wrapIfDenied turns PostgreSQL's insufficient_privilege (42501) into that condition and leaves every other
// failure exactly as it was.
func wrapIfDenied(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42501" {
		return ErrPackageConditionsUnreadable
	}
	return err
}

// packagesListSQL is the operator package-catalogue query, named so a test can assert what it reads.
//
// IT IS A FUNCTION RATHER THAN A LITERAL BECAUSE OF WHAT IT MUST NOT CONTAIN. edged holds no SELECT on
// iam_v2.package_eligibility_rules or iam_v2.package_grant_tiers; referencing either makes the whole
// statement fail under the real runtime role, which is how the operator's package screen returned 500 in
// PRE-LIVE while every superuser test passed. commerce_admin_privilege_test.go enforces that.
func packagesListSQL() string {
	return `SELECT p.id::text, p.code, p.active, COALESCE(p.current_revision_id::text,''),
		        (SELECT count(*) FROM iam_v2.internet_package_revisions r WHERE r.package_id = p.id),
		        cur.display->>'name', cur.price_minor, cur.currency, cur.package_type,
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

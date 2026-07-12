package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// Commercial governance: operator-chosen subscription activation, versioned plan
// limits, and tenant-specific overrides.
//
// Entitlement resolution is deterministic and layered:
//
//	plan_limits  ->  subscription terms (which plan, and whether it is in force)
//	             ->  tenant_limit_overrides (inside their effective window)
//	             ->  the SIGNED LICENSE document (a snapshot, per site/appliance)
//
// The license is a signed snapshot: changing a plan or override NEVER rewrites an
// already-issued license. The operator must re-issue the license for new terms to
// reach an appliance — that is the point of signing it.

// ---------- Subscriptions: activation control ----------

type subscriptionReq struct {
	PlanID string `json:"plan_id"`
	// Activation is the operator's explicit choice — a plan having trial days must
	// NOT silently force a trial.
	Activation   string `json:"activation"` // active | trial | scheduled
	BillingCycle string `json:"billing_cycle"`
	StartDate    string `json:"start_date"`    // RFC3339 / YYYY-MM-DD; default now
	RenewalDate  string `json:"renewal_date"`  // default start + billing interval
	TrialEnd     string `json:"trial_end"`     // required-ish when activation=trial
	AutoRenew    *bool  `json:"auto_renew"`
	Reason       string `json:"reason"`
}

func parseDate(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return def, err
	}
	return t, nil
}

// SetSubscription creates/replaces a tenant's subscription with EXPLICIT terms.
// POST /v1/tenants/{tenantID}/subscription-terms
func (b *Base) SetSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	sess := auth.FromContext(r.Context())
	if sess == nil || (!sess.IsSuperAdmin && !hasAnyRole(sess.Roles, "tenant_admin", "billing")) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "tenant_admin or billing role required")
		return
	}
	var in subscriptionReq
	if err := DecodeJSON(r, &in); err != nil || in.PlanID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "plan_id is required")
		return
	}
	if in.Reason == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required (commercial changes are audited)")
		return
	}
	switch in.Activation {
	case "active", "trial", "scheduled":
	case "":
		in.Activation = "active" // explicit default: do NOT infer a trial from the plan
	default:
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "activation must be active, trial or scheduled")
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Plan must exist; its billing_cycle is the default when the operator doesn't
	// choose one.
	var planCycle, planCode string
	if err := b.DB.QueryRow(ctx, `SELECT billing_cycle, code FROM plans WHERE id=$1 AND is_active`, in.PlanID).
		Scan(&planCycle, &planCode); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "unknown or inactive plan")
		return
	}
	cycle := in.BillingCycle
	if cycle == "" {
		cycle = planCycle
	}
	if cycle != "monthly" && cycle != "yearly" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "billing_cycle must be monthly or yearly")
		return
	}

	now := time.Now().UTC()
	start, err := parseDate(in.StartDate, now)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "start_date must be YYYY-MM-DD or RFC3339")
		return
	}
	defRenew := start.AddDate(0, 1, 0)
	if cycle == "yearly" {
		defRenew = start.AddDate(1, 0, 0)
	}
	renew, err := parseDate(in.RenewalDate, defRenew)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "renewal_date must be YYYY-MM-DD or RFC3339")
		return
	}
	if !renew.After(start) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "renewal_date must be after start_date")
		return
	}

	status := "active"
	var trialEnd *time.Time
	switch in.Activation {
	case "trial":
		status = "trialing"
		te, err := parseDate(in.TrialEnd, start.AddDate(0, 0, 14))
		if err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "trial_end must be YYYY-MM-DD or RFC3339")
			return
		}
		if !te.After(start) {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "trial_end must be after start_date")
			return
		}
		trialEnd = &te
	case "scheduled":
		// Agreed but not in force: grants NO entitlements and cannot be licensed
		// until it is activated.
		status = "scheduled"
		if !start.After(now) {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "a scheduled subscription needs a future start_date")
			return
		}
	}
	autoRenew := true
	if in.AutoRenew != nil {
		autoRenew = *in.AutoRenew
	}

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	// Supersede any current non-terminal subscription.
	if _, err := tx.Exec(ctx, `
        UPDATE tenant_subscriptions SET status='canceled', canceled_at=now(), ended_at=now(), updated_at=now()
         WHERE tenant_id=$1 AND status IN ('trialing','active','past_due','paused','scheduled')`, tenantID); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "supersede failed")
		return
	}
	var subID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO tenant_subscriptions
            (tenant_id, plan_id, status, billing_cycle, current_period_start, current_period_end,
             trial_end, auto_renew)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`,
		tenantID, in.PlanID, status, cycle, start, renew, trialEnd, autoRenew).Scan(&subID); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "could not create subscription: "+err.Error())
		return
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO subscription_events (tenant_id, subscription_id, type, to_plan_id, actor_type, actor_id, payload)
        VALUES ($1,$2,'created',$3,'operator',NULLIF($4,'')::uuid,$5)`,
		tenantID, subID, in.PlanID, sess.OperatorID, mustJSON(map[string]any{
			"activation": in.Activation, "billing_cycle": cycle, "auto_renew": autoRenew,
			"start": start, "renewal": renew, "trial_end": trialEnd, "reason": in.Reason,
		})); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "event failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(ctx, b.DB, r, "subscription.created", "subscription", subID, map[string]any{
		"_tenant_id": tenantID, "plan_code": planCode, "activation": in.Activation,
		"status": status, "billing_cycle": cycle, "start": start, "renewal": renew,
		"trial_end": trialEnd, "auto_renew": autoRenew, "reason": in.Reason,
	})
	WriteJSON(w, http.StatusCreated, map[string]any{
		"id": subID, "status": status, "billing_cycle": cycle,
		"current_period_start": start, "current_period_end": renew,
		"trial_end": trialEnd, "auto_renew": autoRenew, "plan_code": planCode,
	})
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// ---------- Plan limits (vendor product definition) ----------

type planLimitReq struct {
	Key       string  `json:"key"`
	ValueType string  `json:"value_type"` // int | bool | string
	IntValue  *int64  `json:"int_value"`
	BoolValue *bool   `json:"bool_value"`
	StrValue  *string `json:"str_value"`
	Unit      *string `json:"unit"`
	Reason    string  `json:"reason"`
}

func (b *Base) PlanLimitsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequirePermission("platform.plans.manage"))
	r.Get("/{planID}/limits", b.listPlanLimits)
	r.With(RequireReauth(b.Redis)).Put("/{planID}/limits", b.setPlanLimit)
	r.With(RequireReauth(b.Redis)).Delete("/{planID}/limits/{key}", b.deletePlanLimit)
	r.Get("/{planID}/limits/history", b.planLimitHistory)
	return r
}

func (b *Base) listPlanLimits(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "planID")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var version int64
	_ = b.DB.QueryRow(ctx, `SELECT limits_version FROM plans WHERE id=$1`, planID).Scan(&version)
	rows, err := b.DB.Query(ctx, `
        SELECT key, value_type, int_value, bool_value, str_value, COALESCE(unit,'')
          FROM plan_limits WHERE plan_id=$1 ORDER BY key`, planID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key, vt, unit string
		var iv *int64
		var bv *bool
		var sv *string
		if rows.Scan(&key, &vt, &iv, &bv, &sv, &unit) != nil {
			continue
		}
		out = append(out, map[string]any{
			"key": key, "value_type": vt, "int_value": iv, "bool_value": bv,
			"str_value": sv, "unit": unit,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out, "limits_version": version})
}

// setPlanLimit upserts one limit and bumps the plan's limits_version. It NEVER
// touches already-issued signed licenses — those are snapshots; a license must be
// re-issued for the new value to reach an appliance.
func (b *Base) setPlanLimit(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "planID")
	var in planLimitReq
	if err := DecodeJSON(r, &in); err != nil || in.Key == "" || in.ValueType == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "key and value_type are required")
		return
	}
	if in.Reason == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required (plan changes are versioned + audited)")
		return
	}
	switch in.ValueType {
	case "int":
		if in.IntValue == nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "int_value required for value_type=int")
			return
		}
	case "bool":
		if in.BoolValue == nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bool_value required for value_type=bool")
			return
		}
	case "string":
		if in.StrValue == nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "str_value required for value_type=string")
			return
		}
	default:
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "value_type must be int, bool or string")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	sess := auth.FromContext(r.Context())

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "tx failed")
		return
	}
	defer tx.Rollback(ctx)

	var oldVal []byte
	_ = tx.QueryRow(ctx, `
        SELECT jsonb_build_object('value_type',value_type,'int_value',int_value,'bool_value',bool_value,'str_value',str_value)
          FROM plan_limits WHERE plan_id=$1 AND key=$2`, planID, in.Key).Scan(&oldVal)

	if _, err := tx.Exec(ctx, `
        INSERT INTO plan_limits (plan_id, key, value_type, int_value, bool_value, str_value, unit)
        VALUES ($1,$2,$3,$4,$5,$6,$7)
        ON CONFLICT (plan_id, key) DO UPDATE SET
            value_type=EXCLUDED.value_type, int_value=EXCLUDED.int_value,
            bool_value=EXCLUDED.bool_value, str_value=EXCLUDED.str_value, unit=EXCLUDED.unit`,
		planID, in.Key, in.ValueType, in.IntValue, in.BoolValue, in.StrValue, in.Unit); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "could not set limit: "+err.Error())
		return
	}
	var version int64
	if err := tx.QueryRow(ctx,
		`UPDATE plans SET limits_version = limits_version + 1, updated_at=now() WHERE id=$1 RETURNING limits_version`,
		planID).Scan(&version); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "plan not found")
		return
	}
	newVal := mustJSON(map[string]any{
		"value_type": in.ValueType, "int_value": in.IntValue,
		"bool_value": in.BoolValue, "str_value": in.StrValue,
	})
	if _, err := tx.Exec(ctx, `
        INSERT INTO plan_limit_history (plan_id, version, key, old_value, new_value, change_type, reason, actor_id)
        VALUES ($1,$2,$3,$4,$5,'set',$6,NULLIF($7,'')::uuid)`,
		planID, version, in.Key, oldVal, newVal, in.Reason, sess.OperatorID); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "history failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(ctx, b.DB, r, "plan.limit_set", "plan", planID, map[string]any{
		"key": in.Key, "version": version, "reason": in.Reason, "new_value": json.RawMessage(newVal),
	})
	WriteJSON(w, http.StatusOK, map[string]any{
		"status": "set", "key": in.Key, "limits_version": version,
		"note": "already-issued licenses are unchanged; re-issue a license to apply new terms",
	})
}

func (b *Base) deletePlanLimit(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "planID")
	key := chi.URLParam(r, "key")
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason query param is required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	sess := auth.FromContext(r.Context())
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	var oldVal []byte
	_ = tx.QueryRow(ctx, `
        SELECT jsonb_build_object('value_type',value_type,'int_value',int_value,'bool_value',bool_value,'str_value',str_value)
          FROM plan_limits WHERE plan_id=$1 AND key=$2`, planID, key).Scan(&oldVal)
	tag, err := tx.Exec(ctx, `DELETE FROM plan_limits WHERE plan_id=$1 AND key=$2`, planID, key)
	if err != nil || tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "limit not found on this plan")
		return
	}
	var version int64
	_ = tx.QueryRow(ctx, `UPDATE plans SET limits_version=limits_version+1, updated_at=now() WHERE id=$1 RETURNING limits_version`, planID).Scan(&version)
	_, _ = tx.Exec(ctx, `
        INSERT INTO plan_limit_history (plan_id, version, key, old_value, new_value, change_type, reason, actor_id)
        VALUES ($1,$2,$3,$4,NULL,'removed',$5,NULLIF($6,'')::uuid)`,
		planID, version, key, oldVal, reason, sess.OperatorID)
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	audit.Op(ctx, b.DB, r, "plan.limit_removed", "plan", planID, map[string]any{
		"key": key, "version": version, "reason": reason,
	})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "removed", "key": key, "limits_version": version})
}

func (b *Base) planLimitHistory(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "planID")
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT version, key, old_value, new_value, change_type, COALESCE(reason,''), changed_at
          FROM plan_limit_history WHERE plan_id=$1 ORDER BY version DESC, changed_at DESC LIMIT 200`, planID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var version int64
		var key, ct, reason string
		var oldV, newV []byte
		var at time.Time
		if rows.Scan(&version, &key, &oldV, &newV, &ct, &reason, &at) != nil {
			continue
		}
		out = append(out, map[string]any{
			"version": version, "key": key, "change_type": ct, "reason": reason, "changed_at": at,
			"old_value": rawOrNil(oldV), "new_value": rawOrNil(newV),
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func rawOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

// ---------- Tenant limit overrides ----------

type overrideReq struct {
	Key       string  `json:"key"`
	ValueType string  `json:"value_type"`
	IntValue  *int64  `json:"int_value"`
	BoolValue *bool   `json:"bool_value"`
	StrValue  *string `json:"str_value"`
	StartsAt  string  `json:"starts_at"`
	ExpiresAt string  `json:"expires_at"`
	Reason    string  `json:"reason"`
}

func (b *Base) OverridesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequirePermission("platform.plans.manage"))
	r.Get("/{tenantID}/limit-overrides", b.listOverrides)
	r.With(RequireReauth(b.Redis)).Put("/{tenantID}/limit-overrides", b.setOverride)
	r.With(RequireReauth(b.Redis)).Delete("/{tenantID}/limit-overrides/{key}", b.deleteOverride)
	return r
}

func (b *Base) listOverrides(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT key, value_type, int_value, bool_value, str_value, COALESCE(reason,''),
               starts_at, expires_at, created_at
          FROM tenant_limit_overrides WHERE tenant_id=$1 ORDER BY key`, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key, vt, reason string
		var iv *int64
		var bv *bool
		var sv *string
		var starts, created time.Time
		var expires *time.Time
		if rows.Scan(&key, &vt, &iv, &bv, &sv, &reason, &starts, &expires, &created) != nil {
			continue
		}
		active := !starts.After(time.Now()) && (expires == nil || expires.After(time.Now()))
		out = append(out, map[string]any{
			"key": key, "value_type": vt, "int_value": iv, "bool_value": bv, "str_value": sv,
			"reason": reason, "starts_at": starts, "expires_at": expires, "created_at": created,
			"in_effect": active,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (b *Base) setOverride(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	var in overrideReq
	if err := DecodeJSON(r, &in); err != nil || in.Key == "" || in.ValueType == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "key and value_type are required")
		return
	}
	if in.Reason == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required (overrides are audited)")
		return
	}
	now := time.Now().UTC()
	starts, err := parseDate(in.StartsAt, now)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "starts_at must be YYYY-MM-DD or RFC3339")
		return
	}
	var expires *time.Time
	if in.ExpiresAt != "" {
		e, err := parseDate(in.ExpiresAt, now)
		if err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "expires_at must be YYYY-MM-DD or RFC3339")
			return
		}
		if !e.After(starts) {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "expires_at must be after starts_at")
			return
		}
		expires = &e
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	sess := auth.FromContext(r.Context())
	if _, err := b.DB.Exec(ctx, `
        INSERT INTO tenant_limit_overrides
            (tenant_id, key, value_type, int_value, bool_value, str_value, reason, starts_at, expires_at, created_by)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid)
        ON CONFLICT (tenant_id, key) DO UPDATE SET
            value_type=EXCLUDED.value_type, int_value=EXCLUDED.int_value, bool_value=EXCLUDED.bool_value,
            str_value=EXCLUDED.str_value, reason=EXCLUDED.reason, starts_at=EXCLUDED.starts_at,
            expires_at=EXCLUDED.expires_at, created_by=EXCLUDED.created_by`,
		tenantID, in.Key, in.ValueType, in.IntValue, in.BoolValue, in.StrValue, in.Reason,
		starts, expires, sess.OperatorID); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "could not set override: "+err.Error())
		return
	}
	audit.Op(ctx, b.DB, r, "tenant.limit_override_set", "tenant", tenantID, map[string]any{
		"_tenant_id": tenantID, "key": in.Key, "value_type": in.ValueType,
		"int_value": in.IntValue, "bool_value": in.BoolValue, "str_value": in.StrValue,
		"starts_at": starts, "expires_at": expires, "reason": in.Reason,
	})
	WriteJSON(w, http.StatusOK, map[string]any{
		"status": "set", "key": in.Key, "starts_at": starts, "expires_at": expires,
		"note": "already-issued licenses are unchanged; re-issue a license to apply new terms",
	})
}

func (b *Base) deleteOverride(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	key := chi.URLParam(r, "key")
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason query param is required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx, `DELETE FROM tenant_limit_overrides WHERE tenant_id=$1 AND key=$2`, tenantID, key)
	if err != nil || tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "override not found")
		return
	}
	audit.Op(ctx, b.DB, r, "tenant.limit_override_removed", "tenant", tenantID, map[string]any{
		"_tenant_id": tenantID, "key": key, "reason": reason,
	})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "removed", "key": key})
}

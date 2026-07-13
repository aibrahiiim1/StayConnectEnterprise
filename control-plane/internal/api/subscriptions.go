package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type Subscription struct {
	ID                 string     `json:"id"`
	TenantID           string     `json:"tenant_id"`
	PlanID             string     `json:"plan_id"`
	PlanCode           string     `json:"plan_code"`
	PlanName           string     `json:"plan_name"`
	Status             string     `json:"status"`
	BillingCycle       string     `json:"billing_cycle"`
	CurrentPeriodStart time.Time  `json:"current_period_start"`
	CurrentPeriodEnd   time.Time  `json:"current_period_end"`
	TrialEnd           *time.Time `json:"trial_end,omitempty"`
	CancelAtPeriodEnd  bool       `json:"cancel_at_period_end"`
	CanceledAt         *time.Time `json:"canceled_at,omitempty"`
	EndedAt            *time.Time `json:"ended_at,omitempty"`
	ExternalRef        *string    `json:"external_ref,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type EffectiveLimit struct {
	Key       string  `json:"key"`
	ValueType string  `json:"value_type"`
	IntValue  *int64  `json:"int_value,omitempty"`
	BoolValue *bool   `json:"bool_value,omitempty"`
	StrValue  *string `json:"str_value,omitempty"`
	Unit      *string `json:"unit,omitempty"`
	Source    string  `json:"source"` // "plan" | "override"
}

type changePlanReq struct {
	PlanID string `json:"plan_id"`
}

type changePlanResp struct {
	Subscription Subscription `json:"subscription"`
	ChangeType   string       `json:"change_type"` // upgrade|downgrade|lateral
	FromPlanID   string       `json:"from_plan_id"`
	ToPlanID     string       `json:"to_plan_id"`
}

// SubscriptionsRoutes — mounted under /v1/tenants/{tenantID}/...
// We route these as siblings of tenants rather than inside the tenants
// subtree to avoid complicating the tenants router; path includes tenantID.
// Subscription routes live inside TenantsRoutes() in tenants.go — their
// paths share the /v1/tenants/{tenantID}/... prefix.

func (b *Base) ensureTenantAccess(r *http.Request, tenantID string) bool {
	s := auth.FromContext(r.Context())
	if s == nil {
		return false
	}
	if s.IsSuperAdmin {
		return true
	}
	return s.DefaultTenantID == tenantID
}

func (b *Base) getSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	if !b.ensureTenantAccess(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	sub, err := fetchActiveSubscription(ctx, b, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "no active subscription")
			return
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, sub)
}

func (b *Base) effectiveLimits(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	if !b.ensureTenantAccess(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT key, value_type, int_value, bool_value, str_value, unit, source
          FROM tenant_effective_limits
         WHERE tenant_id = $1
         ORDER BY key
    `, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []EffectiveLimit
	for rows.Next() {
		var el EffectiveLimit
		if err := rows.Scan(&el.Key, &el.ValueType, &el.IntValue, &el.BoolValue, &el.StrValue, &el.Unit, &el.Source); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, el)
	}
	WriteList(w, out, ListMeta{})
}

// changeSubscription swaps the tenant's plan immediately:
//  1. Close the current (non-terminal) subscription: ended_at=now(), status=canceled.
//  2. Insert a fresh row for the target plan.
//  3. Write a subscription_events row of type=plan_changed with server-derived change_type.
func (b *Base) changeSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	sess := auth.FromContext(r.Context())
	if sess == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "not authenticated")
		return
	}
	if !b.ensureTenantAccess(r, tenantID) {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "access denied")
		return
	}
	// Only platform_admin or tenant_admin may change plans.
	if !sess.IsSuperAdmin && !hasAnyRole(sess.Roles, "tenant_admin", "billing") {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "tenant_admin or billing role required")
		return
	}

	var req changePlanReq
	if err := DecodeJSON(r, &req); err != nil || req.PlanID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "plan_id required")
		return
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Load target plan.
	toPlan, err := fetchPlanCore(ctx, b, req.PlanID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "plan not found")
			return
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	if !toPlan.IsActive {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "plan is not active")
		return
	}

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin tx failed")
		return
	}
	defer tx.Rollback(ctx)

	// Fetch current (non-terminal) subscription, if any.
	var fromPlanID string
	var fromBillingCycle string
	var fromSubID string
	err = tx.QueryRow(ctx, `
        SELECT id, plan_id, billing_cycle
          FROM tenant_subscriptions
         WHERE tenant_id = $1 AND status IN ('trialing','active','past_due','paused')
         FOR UPDATE
    `, tenantID).Scan(&fromSubID, &fromPlanID, &fromBillingCycle)
	hasCurrent := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup current sub failed")
		return
	}

	// No-op if target is same plan.
	if hasCurrent && fromPlanID == req.PlanID {
		Fail(w, r, http.StatusConflict, CodeConflict, "tenant already on this plan")
		return
	}

	// Compute change_type BEFORE mutating.
	changeType := "lateral"
	if hasCurrent {
		fromPlan, err := fetchPlanCore(ctx, b, fromPlanID)
		if err == nil {
			changeType = classifyPlanChange(fromPlan, toPlan)
		}
	}

	now := time.Now()
	periodEnd := now.Add(31 * 24 * time.Hour) // monthly default
	if toPlan.BillingCycle == "yearly" {
		periodEnd = now.AddDate(1, 0, 0)
	}

	// Close current subscription if any.
	if hasCurrent {
		if _, err := tx.Exec(ctx, `
            UPDATE tenant_subscriptions
               SET status = 'canceled',
                   canceled_at = now(),
                   ended_at = now(),
                   updated_at = now()
             WHERE id = $1
        `, fromSubID); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "close current sub failed")
			return
		}
	}

	// Insert new subscription.
	status := "active"
	if toPlan.TrialDays > 0 && !hasCurrent {
		// Only grant trial on the tenant's first-ever subscription.
		status = "trialing"
	}
	var trialEnd *time.Time
	if status == "trialing" {
		t := now.Add(time.Duration(toPlan.TrialDays) * 24 * time.Hour)
		trialEnd = &t
	}
	var newSubID string
	err = tx.QueryRow(ctx, `
        INSERT INTO tenant_subscriptions
          (tenant_id, plan_id, status, billing_cycle,
           current_period_start, current_period_end, trial_end)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        RETURNING id
    `, tenantID, toPlan.ID, status, toPlan.BillingCycle, now, periodEnd, trialEnd).Scan(&newSubID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "insert sub failed")
		return
	}

	// Audit event.
	payload := map[string]any{
		"from_billing_cycle": fromBillingCycle,
		"to_billing_cycle":   toPlan.BillingCycle,
	}
	var fromArg any
	if hasCurrent {
		fromArg = fromPlanID
	}
	_, err = tx.Exec(ctx, `
        INSERT INTO subscription_events
          (tenant_id, subscription_id, type, from_plan_id, to_plan_id,
           change_type, actor_type, actor_id, payload)
        VALUES ($1, $2, 'plan_changed', $3, $4, $5, 'operator', $6, $7::jsonb)
    `, tenantID, newSubID, fromArg, toPlan.ID, changeType, sess.OperatorID, jsonb(payload))
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "audit insert failed")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}

	sub, err := fetchActiveSubscription(context.Background(), b, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "post-commit fetch failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "subscription.plan_changed", "subscription", newSubID, map[string]any{
		"_tenant_id": tenantID, "from_plan": fromPlanID, "to_plan": toPlan.ID, "change_type": changeType,
	})
	WriteJSON(w, http.StatusOK, changePlanResp{
		Subscription: *sub,
		ChangeType:   changeType,
		FromPlanID:   fromPlanID,
		ToPlanID:     toPlan.ID,
	})
}

// ---- Internal helpers -------------------------------------------------------

type planCore struct {
	ID           string
	Code         string
	BillingCycle string
	PriceCents   int
	TrialDays    int
	IsActive     bool
}

func fetchPlanCore(ctx context.Context, b *Base, id string) (*planCore, error) {
	var p planCore
	err := b.DB.QueryRow(ctx, `
        SELECT id, code, billing_cycle, price_cents, trial_days, is_active
          FROM plans WHERE id = $1
    `, id).Scan(&p.ID, &p.Code, &p.BillingCycle, &p.PriceCents, &p.TrialDays, &p.IsActive)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// classifyPlanChange derives upgrade / downgrade / lateral server-side.
//
// Rules:
//  1. Same tier (first path segment of plan.code, e.g. "starter" in
//     "starter-yearly")  → lateral, regardless of billing cycle.
//  2. Different tier     → compare monthly-equivalent price:
//     newMonthly > oldMonthly → upgrade
//     newMonthly < oldMonthly → downgrade
//     equal                   → lateral (shouldn't happen with our seed
//     but defensive default).
func classifyPlanChange(from, to *planCore) string {
	if tierOf(from.Code) == tierOf(to.Code) {
		return "lateral"
	}
	fm := monthlyCents(from)
	tm := monthlyCents(to)
	switch {
	case tm > fm:
		return "upgrade"
	case tm < fm:
		return "downgrade"
	default:
		return "lateral"
	}
}

func tierOf(code string) string {
	if i := strings.Index(code, "-"); i >= 0 {
		return code[:i]
	}
	return code
}

func monthlyCents(p *planCore) int {
	if p.BillingCycle == "yearly" && p.PriceCents > 0 {
		return p.PriceCents / 12
	}
	return p.PriceCents
}

func fetchActiveSubscription(ctx context.Context, b *Base, tenantID string) (*Subscription, error) {
	var s Subscription
	err := b.DB.QueryRow(ctx, `
        SELECT ts.id, ts.tenant_id, ts.plan_id, p.code, p.name,
               ts.status, ts.billing_cycle,
               ts.current_period_start, ts.current_period_end,
               ts.trial_end, ts.cancel_at_period_end,
               ts.canceled_at, ts.ended_at, ts.external_ref,
               ts.created_at, ts.updated_at
          FROM tenant_subscriptions ts
          JOIN plans p ON p.id = ts.plan_id
         WHERE ts.tenant_id = $1
           AND ts.status IN ('trialing','active','past_due','paused')
         ORDER BY ts.created_at DESC LIMIT 1
    `, tenantID).Scan(
		&s.ID, &s.TenantID, &s.PlanID, &s.PlanCode, &s.PlanName,
		&s.Status, &s.BillingCycle,
		&s.CurrentPeriodStart, &s.CurrentPeriodEnd,
		&s.TrialEnd, &s.CancelAtPeriodEnd,
		&s.CanceledAt, &s.EndedAt, &s.ExternalRef,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func hasAnyRole(got []string, want ...string) bool {
	for _, w := range want {
		for _, g := range got {
			if g == w {
				return true
			}
		}
	}
	return false
}

// jsonb encodes v as a JSON string suitable for a $N::jsonb bind.
func jsonb(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// cancelSubscription cancels the tenant's current (non-terminal) subscription so
// it no longer grants entitlements or blocks a Customer deletion. Billing-
// sensitive → step-up gated at the route. Idempotent.
func (b *Base) cancelSubscription(w http.ResponseWriter, r *http.Request) {
	tenantID := chi.URLParam(r, "tenantID")
	var in struct {
		Reason string `json:"reason"`
	}
	_ = DecodeJSON(r, &in)
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx, `
        UPDATE tenant_subscriptions
           SET status='canceled', canceled_at=now(), ended_at=now(), updated_at=now()
         WHERE tenant_id=$1 AND status <> 'canceled' AND ended_at IS NULL`, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "cancel failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "subscription.canceled", "tenant", tenantID,
		map[string]any{"_tenant_id": tenantID, "reason": in.Reason, "count": tag.RowsAffected()})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "canceled", "count": tag.RowsAffected()})
}

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type Plan struct {
	ID           string      `json:"id"`
	Code         string      `json:"code"`
	Name         string      `json:"name"`
	Description  *string     `json:"description,omitempty"`
	BillingCycle string      `json:"billing_cycle"`
	PriceCents   int         `json:"price_cents"`
	Currency     string      `json:"currency"`
	TrialDays    int         `json:"trial_days"`
	IsPublic     bool        `json:"is_public"`
	IsActive     bool        `json:"is_active"`
	SortOrder    int         `json:"sort_order"`
	Limits       []PlanLimit `json:"limits,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

type PlanLimit struct {
	Key       string  `json:"key"`
	ValueType string  `json:"value_type"`
	IntValue  *int64  `json:"int_value,omitempty"`
	BoolValue *bool   `json:"bool_value,omitempty"`
	StrValue  *string `json:"str_value,omitempty"`
	Unit      *string `json:"unit,omitempty"`
}

// PlansRoutes — /v1/plans. The Plan is the vendor PRODUCT DEFINITION: its limits
// and entitlements are managed here (versioned + audited). Changing a plan never
// rewrites an already-issued signed license; the license must be re-issued.
func (b *Base) PlansRoutes() http.Handler {
	r := chi.NewRouter()
	r.With(auth.RequirePermission("platform.plans.view")).Get("/", b.listPlans)
	r.With(auth.RequirePermission("platform.plans.view")).Get("/{id}", b.getPlan)
	// Plan Catalog is platform-managed: create / edit / retire require the
	// platform.plans.manage permission (not merely a role).
	r.With(auth.RequirePermission("platform.plans.manage")).Post("/", b.createPlan)
	r.With(auth.RequirePermission("platform.plans.manage")).Patch("/{id}", b.updatePlan)
	r.With(auth.RequirePermission("platform.plans.manage")).Post("/{id}/retire", b.retirePlan)
	// Plan limits / entitlements (max sites, max appliances, concurrent sessions,
	// retention, feature flags, update channel, support tier...).
	r.With(auth.RequirePermission("platform.plans.view")).Get("/{planID}/limits", b.listPlanLimits)
	r.With(auth.RequirePermission("platform.plans.view")).Get("/{planID}/limits/history", b.planLimitHistory)
	r.With(auth.RequirePermission("platform.plans.manage"), RequireReauth(b.Redis)).Put("/{planID}/limits", b.setPlanLimit)
	r.With(auth.RequirePermission("platform.plans.manage"), RequireReauth(b.Redis)).Delete("/{planID}/limits/{key}", b.deletePlanLimit)
	return r
}

type planInput struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	BillingCycle string  `json:"billing_cycle"`
	PriceCents   *int    `json:"price_cents"`
	Currency     string  `json:"currency"`
	TrialDays    *int    `json:"trial_days"`
	IsPublic     *bool   `json:"is_public"`
	IsActive     *bool   `json:"is_active"`
	SortOrder    *int    `json:"sort_order"`
}

func (b *Base) createPlan(w http.ResponseWriter, r *http.Request) {
	var in planInput
	if err := DecodeJSON(r, &in); err != nil || in.Code == "" || in.Name == "" || in.BillingCycle == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "code, name, billing_cycle required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var id string
	err := b.DB.QueryRow(ctx, `
        INSERT INTO plans (code,name,description,billing_cycle,price_cents,currency,trial_days,is_public,is_active,sort_order)
        VALUES ($1,$2,$3,$4,COALESCE($5,0),COALESCE(NULLIF($6,''),'USD'),COALESCE($7,0),COALESCE($8,true),COALESCE($9,true),COALESCE($10,0))
        RETURNING id::text`,
		in.Code, in.Name, in.Description, in.BillingCycle, in.PriceCents, in.Currency,
		in.TrialDays, in.IsPublic, in.IsActive, in.SortOrder).Scan(&id)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "create failed: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (b *Base) updatePlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in planInput
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx, `
        UPDATE plans SET
          name = COALESCE(NULLIF($2,''), name),
          description = COALESCE($3, description),
          billing_cycle = COALESCE(NULLIF($4,''), billing_cycle),
          price_cents = COALESCE($5, price_cents),
          currency = COALESCE(NULLIF($6,''), currency),
          trial_days = COALESCE($7, trial_days),
          is_public = COALESCE($8, is_public),
          is_active = COALESCE($9, is_active),
          sort_order = COALESCE($10, sort_order),
          updated_at = now()
        WHERE id = $1`,
		id, in.Name, in.Description, in.BillingCycle, in.PriceCents, in.Currency,
		in.TrialDays, in.IsPublic, in.IsActive, in.SortOrder)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "plan not found")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (b *Base) retirePlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx, `UPDATE plans SET is_active=false, is_public=false, updated_at=now() WHERE id=$1`, id)
	if err != nil || tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "plan not found")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "retired"})
}

func (b *Base) listPlans(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	// Non-platform operators only see public+active plans.
	// Super admins see everything.
	showAll := false
	if r.URL.Query().Get("all") == "true" {
		showAll = true
	}

	rows, err := b.DB.Query(ctx, `
        SELECT id, code, name, description, billing_cycle, price_cents, currency,
               trial_days, is_public, is_active, sort_order, created_at, updated_at
          FROM plans
         WHERE ($1 OR (is_public = true AND is_active = true))
         ORDER BY sort_order ASC, code ASC
    `, showAll)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []Plan
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Description, &p.BillingCycle,
			&p.PriceCents, &p.Currency, &p.TrialDays, &p.IsPublic, &p.IsActive,
			&p.SortOrder, &p.CreatedAt, &p.UpdatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, p)
	}
	WriteList(w, out, ListMeta{})
}

func (b *Base) getPlan(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var p Plan
	err := b.DB.QueryRow(ctx, `
        SELECT id, code, name, description, billing_cycle, price_cents, currency,
               trial_days, is_public, is_active, sort_order, created_at, updated_at
          FROM plans WHERE id = $1
    `, id).Scan(&p.ID, &p.Code, &p.Name, &p.Description, &p.BillingCycle,
		&p.PriceCents, &p.Currency, &p.TrialDays, &p.IsPublic, &p.IsActive,
		&p.SortOrder, &p.CreatedAt, &p.UpdatedAt)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "plan not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	// Load limits.
	if limits, err := fetchPlanLimits(ctx, b, id); err == nil {
		p.Limits = limits
	}
	WriteJSON(w, http.StatusOK, p)
}

func fetchPlanLimits(ctx context.Context, b *Base, planID string) ([]PlanLimit, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT key, value_type, int_value, bool_value, str_value, unit
          FROM plan_limits WHERE plan_id = $1 ORDER BY key
    `, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanLimit
	for rows.Next() {
		var pl PlanLimit
		if err := rows.Scan(&pl.Key, &pl.ValueType, &pl.IntValue, &pl.BoolValue, &pl.StrValue, &pl.Unit); err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	return out, rows.Err()
}

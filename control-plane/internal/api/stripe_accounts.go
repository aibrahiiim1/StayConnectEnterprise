package api

// CRUD for stripe_accounts (phase 12.4).
//
// Mirror of notification_providers / social_oauth_providers: at most one
// enabled row per tenant; secret_key + webhook_secret are write-only.
// Publishable key is NOT a secret (Stripe ships it to browsers) — we
// return it in GETs.

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type StripeAccount struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenant_id"`
	Enabled        bool       `json:"enabled"`
	DisplayName    string     `json:"display_name,omitempty"`
	PublishableKey string     `json:"publishable_key"`
	SuccessURL     string     `json:"success_url"`
	CancelURL      string     `json:"cancel_url"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type StripeAdminBase struct{ *Base }

type stripeWriteReq struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	DisplayName    *string `json:"display_name,omitempty"`
	PublishableKey *string `json:"publishable_key,omitempty"`
	SecretKey      *string `json:"secret_key,omitempty"`
	WebhookSecret  *string `json:"webhook_secret,omitempty"`
	SuccessURL     *string `json:"success_url,omitempty"`
	CancelURL      *string `json:"cancel_url,omitempty"`
}

func (b *StripeAdminBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", b.list)
	r.Post("/", b.create)
	r.Get("/{id}", b.get)
	r.Patch("/{id}", b.patch)
	r.Delete("/{id}", b.del)
	return r
}

const stripeCols = `id, tenant_id, enabled, COALESCE(display_name,''),
                    publishable_key, success_url, cancel_url,
                    last_success_at, COALESCE(last_error,''), last_error_at,
                    created_at, updated_at`

func scanStripe(row interface{ Scan(...any) error }, s *StripeAccount) error {
	return row.Scan(&s.ID, &s.TenantID, &s.Enabled, &s.DisplayName,
		&s.PublishableKey, &s.SuccessURL, &s.CancelURL,
		&s.LastSuccessAt, &s.LastError, &s.LastErrorAt,
		&s.CreatedAt, &s.UpdatedAt)
}

func (b *StripeAdminBase) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx,
		`SELECT `+stripeCols+` FROM stripe_accounts WHERE tenant_id=$1 ORDER BY created_at`, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []StripeAccount
	for rows.Next() {
		var s StripeAccount
		if err := scanStripe(rows, &s); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, s)
	}
	WriteList(w, out, ListMeta{})
}

func (b *StripeAdminBase) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var s StripeAccount
	err := scanStripe(b.DB.QueryRow(ctx,
		`SELECT `+stripeCols+` FROM stripe_accounts WHERE id=$1 AND tenant_id=$2`,
		id, tenantID), &s)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, s)
}

func (b *StripeAdminBase) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req stripeWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	pk := strings.TrimSpace(strDeref(req.PublishableKey))
	sk := strings.TrimSpace(strDeref(req.SecretKey))
	ws := strings.TrimSpace(strDeref(req.WebhookSecret))
	su := strings.TrimSpace(strDeref(req.SuccessURL))
	cu := strings.TrimSpace(strDeref(req.CancelURL))
	if pk == "" || sk == "" || ws == "" || su == "" || cu == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest,
			"publishable_key, secret_key, webhook_secret, success_url, cancel_url required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var s StripeAccount
	err := scanStripe(b.DB.QueryRow(ctx, `
        INSERT INTO stripe_accounts(tenant_id, enabled, display_name,
                                    publishable_key, secret_key, webhook_secret,
                                    success_url, cancel_url)
        VALUES ($1, $2, NULLIF($3,''), $4, $5, $6, $7, $8)
        RETURNING `+stripeCols,
		tenantID, enabled, strDeref(req.DisplayName), pk, sk, ws, su, cu,
	), &s)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "insert failed (already enabled?)")
		return
	}
	audit.Op(r.Context(), b.DB, r, "stripe_account.created", "stripe_account", s.ID, map[string]any{
		"_tenant_id": tenantID,
	})
	WriteJSON(w, http.StatusCreated, s)
}

func (b *StripeAdminBase) patch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	var req stripeWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var s StripeAccount
	err := scanStripe(b.DB.QueryRow(ctx, `
        UPDATE stripe_accounts SET
            enabled         = COALESCE($3, enabled),
            display_name    = COALESCE($4, display_name),
            publishable_key = COALESCE(NULLIF($5,''), publishable_key),
            secret_key      = COALESCE(NULLIF($6,''), secret_key),
            webhook_secret  = COALESCE(NULLIF($7,''), webhook_secret),
            success_url     = COALESCE(NULLIF($8,''), success_url),
            cancel_url      = COALESCE(NULLIF($9,''), cancel_url),
            updated_at      = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+stripeCols,
		id, tenantID,
		req.Enabled, req.DisplayName,
		strDeref(req.PublishableKey), strDeref(req.SecretKey), strDeref(req.WebhookSecret),
		strDeref(req.SuccessURL), strDeref(req.CancelURL),
	), &s)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "stripe_account.updated", "stripe_account", id, map[string]any{"_tenant_id": tenantID})
	WriteJSON(w, http.StatusOK, s)
}

func (b *StripeAdminBase) del(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx, `DELETE FROM stripe_accounts WHERE id=$1 AND tenant_id=$2`, id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "account not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "stripe_account.deleted", "stripe_account", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

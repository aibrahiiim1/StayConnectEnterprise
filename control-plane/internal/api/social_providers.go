package api

// CRUD for social_oauth_providers (phase 9.5).
//
// Mirror of notification_providers admin: list/create/patch/delete per
// tenant, secrets (client_secret) are write-only.

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

var socialAllowedProviders = map[string]bool{
	"google": true, "apple": true, "facebook": true, "microsoft": true,
}

type SocialOAuthProvider struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	Provider      string     `json:"provider"`
	Enabled       bool       `json:"enabled"`
	DisplayName   string     `json:"display_name,omitempty"`
	ClientID      string     `json:"client_id"` // public per OAuth2 spec
	RedirectURI   string     `json:"redirect_uri"`
	Scopes        string     `json:"scopes,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastErrorAt   *time.Time `json:"last_error_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type SocialAdminBase struct{ *Base }

type socialWriteReq struct {
	Provider     string  `json:"provider"` // create-only
	Enabled      *bool   `json:"enabled,omitempty"`
	DisplayName  *string `json:"display_name,omitempty"`
	ClientID     *string `json:"client_id,omitempty"`
	ClientSecret *string `json:"client_secret,omitempty"`
	RedirectURI  *string `json:"redirect_uri,omitempty"`
	Scopes       *string `json:"scopes,omitempty"`
}

func (b *SocialAdminBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", b.list)
	r.Post("/", b.create)
	r.Get("/{id}", b.get)
	r.Patch("/{id}", b.patch)
	r.Delete("/{id}", b.del)
	return r
}

const socialCols = `id, tenant_id, provider, enabled, COALESCE(display_name,''),
                    client_id, redirect_uri, COALESCE(scopes,''),
                    last_success_at, COALESCE(last_error,''), last_error_at,
                    created_at, updated_at`

func scanSocial(row interface{ Scan(...any) error }, p *SocialOAuthProvider) error {
	return row.Scan(&p.ID, &p.TenantID, &p.Provider, &p.Enabled, &p.DisplayName,
		&p.ClientID, &p.RedirectURI, &p.Scopes,
		&p.LastSuccessAt, &p.LastError, &p.LastErrorAt,
		&p.CreatedAt, &p.UpdatedAt)
}

func (b *SocialAdminBase) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx,
		`SELECT `+socialCols+` FROM social_oauth_providers WHERE tenant_id=$1 ORDER BY provider, created_at`,
		tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []SocialOAuthProvider
	for rows.Next() {
		var p SocialOAuthProvider
		if err := scanSocial(rows, &p); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, p)
	}
	WriteList(w, out, ListMeta{})
}

func (b *SocialAdminBase) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var p SocialOAuthProvider
	err := scanSocial(b.DB.QueryRow(ctx,
		`SELECT `+socialCols+` FROM social_oauth_providers WHERE id=$1 AND tenant_id=$2`,
		id, tenantID), &p)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, p)
}

func (b *SocialAdminBase) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req socialWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	if !socialAllowedProviders[req.Provider] {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "unsupported provider")
		return
	}
	if strDeref(req.ClientID) == "" || strDeref(req.ClientSecret) == "" || strDeref(req.RedirectURI) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "client_id, client_secret, redirect_uri required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var p SocialOAuthProvider
	err := scanSocial(b.DB.QueryRow(ctx, `
        INSERT INTO social_oauth_providers(
            tenant_id, provider, enabled, display_name,
            client_id, client_secret, redirect_uri, scopes
        ) VALUES (
            $1, $2, $3, NULLIF($4,''),
            $5, $6, $7, NULLIF($8,'')
        )
        RETURNING `+socialCols,
		tenantID, req.Provider, enabled, strDeref(req.DisplayName),
		strDeref(req.ClientID), strDeref(req.ClientSecret), strDeref(req.RedirectURI),
		strDeref(req.Scopes),
	), &p)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "insert failed (already enabled for this provider?)")
		return
	}
	audit.Op(r.Context(), b.DB, r, "social_oauth_provider.created", "social_oauth_provider", p.ID, map[string]any{
		"_tenant_id": tenantID, "provider": req.Provider,
	})
	WriteJSON(w, http.StatusCreated, p)
}

func (b *SocialAdminBase) patch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	var req socialWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var p SocialOAuthProvider
	err := scanSocial(b.DB.QueryRow(ctx, `
        UPDATE social_oauth_providers SET
            enabled       = COALESCE($3, enabled),
            display_name  = COALESCE($4, display_name),
            client_id     = COALESCE(NULLIF($5,''), client_id),
            client_secret = COALESCE(NULLIF($6,''), client_secret),
            redirect_uri  = COALESCE(NULLIF($7,''), redirect_uri),
            scopes        = COALESCE($8, scopes),
            updated_at    = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+socialCols,
		id, tenantID,
		req.Enabled, req.DisplayName,
		strDeref(req.ClientID), strDeref(req.ClientSecret), strDeref(req.RedirectURI),
		req.Scopes,
	), &p)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "social_oauth_provider.updated", "social_oauth_provider", id, map[string]any{
		"_tenant_id": tenantID,
	})
	WriteJSON(w, http.StatusOK, p)
}

func (b *SocialAdminBase) del(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx,
		`DELETE FROM social_oauth_providers WHERE id=$1 AND tenant_id=$2`, id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "social_oauth_provider.deleted", "social_oauth_provider", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

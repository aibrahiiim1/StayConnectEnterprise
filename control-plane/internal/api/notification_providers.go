package api

// CRUD for notification_providers (phase 8.6).
//
// Mirror of pms_providers admin: list/create/patch/delete per tenant,
// secrets (api_key, api_user) are write-only — never returned in
// responses, retained on patches that omit them.

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/configpush"
)

var notifyAllowedKinds = map[string]map[string]bool{
	"email": {"stub": true, "sendgrid": true, "ses": true},
	"sms":   {"stub": true, "twilio": true},
}

type NotificationProvider struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	Channel       string     `json:"channel"`
	Kind          string     `json:"kind"`
	Enabled       bool       `json:"enabled"`
	DisplayName   string     `json:"display_name,omitempty"`
	APIUser       string     `json:"api_user,omitempty"` // not a secret (Twilio account_sid)
	FromAddress   string     `json:"from_address,omitempty"`
	FromName      string     `json:"from_name,omitempty"`
	Region        string     `json:"region,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastErrorAt   *time.Time `json:"last_error_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type NotificationAdminBase struct {
	*Base
	ConfigPush *configpush.Pusher // reserved for future per-channel push events
}

type notifyWriteReq struct {
	Channel     string  `json:"channel"` // create-only
	Kind        string  `json:"kind"`    // create-only
	Enabled     *bool   `json:"enabled,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	APIKey      *string `json:"api_key,omitempty"`
	APIUser     *string `json:"api_user,omitempty"`
	FromAddress *string `json:"from_address,omitempty"`
	FromName    *string `json:"from_name,omitempty"`
	Region      *string `json:"region,omitempty"`
}

func (b *NotificationAdminBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", b.list)
	r.Post("/", b.create)
	r.Get("/{id}", b.get)
	r.Patch("/{id}", b.patch)
	r.Delete("/{id}", b.del)
	return r
}

const notifyCols = `id, tenant_id, channel, kind, enabled, COALESCE(display_name,''),
                    COALESCE(api_user,''), COALESCE(from_address,''), COALESCE(from_name,''),
                    COALESCE(region,''), last_success_at, COALESCE(last_error,''),
                    last_error_at, created_at, updated_at`

func scanNotify(row interface{ Scan(...any) error }, n *NotificationProvider) error {
	return row.Scan(&n.ID, &n.TenantID, &n.Channel, &n.Kind, &n.Enabled, &n.DisplayName,
		&n.APIUser, &n.FromAddress, &n.FromName, &n.Region,
		&n.LastSuccessAt, &n.LastError, &n.LastErrorAt, &n.CreatedAt, &n.UpdatedAt)
}

func (b *NotificationAdminBase) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx,
		`SELECT `+notifyCols+` FROM notification_providers WHERE tenant_id=$1 ORDER BY channel, created_at`,
		tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []NotificationProvider
	for rows.Next() {
		var n NotificationProvider
		if err := scanNotify(rows, &n); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, n)
	}
	WriteList(w, out, ListMeta{})
}

func (b *NotificationAdminBase) get(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var n NotificationProvider
	err := scanNotify(b.DB.QueryRow(ctx,
		`SELECT `+notifyCols+` FROM notification_providers WHERE id=$1 AND tenant_id=$2`,
		id, tenantID), &n)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, n)
}

func (b *NotificationAdminBase) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req notifyWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	req.Channel = strings.TrimSpace(req.Channel)
	req.Kind = strings.TrimSpace(req.Kind)
	allowed, ok := notifyAllowedKinds[req.Channel]
	if !ok {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "channel must be email|sms")
		return
	}
	if !allowed[req.Kind] {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "kind not supported for this channel")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var n NotificationProvider
	err := scanNotify(b.DB.QueryRow(ctx, `
        INSERT INTO notification_providers(
            tenant_id, channel, kind, enabled, display_name,
            api_key, api_user, from_address, from_name, region
        ) VALUES (
            $1, $2, $3, $4, NULLIF($5,''),
            NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), NULLIF($10,'')
        )
        RETURNING `+notifyCols,
		tenantID, req.Channel, req.Kind, enabled, strDeref(req.DisplayName),
		strDeref(req.APIKey), strDeref(req.APIUser),
		strDeref(req.FromAddress), strDeref(req.FromName), strDeref(req.Region),
	), &n)
	if err != nil {
		// Most likely cause: another enabled row exists for this (tenant, channel).
		Fail(w, r, http.StatusConflict, CodeConflict, "insert failed (already enabled for this channel?)")
		return
	}
	audit.Op(r.Context(), b.DB, r, "notification_provider.created", "notification_provider", n.ID, map[string]any{
		"_tenant_id": tenantID, "channel": req.Channel, "kind": req.Kind,
	})
	WriteJSON(w, http.StatusCreated, n)
}

func (b *NotificationAdminBase) patch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	var req notifyWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var n NotificationProvider
	err := scanNotify(b.DB.QueryRow(ctx, `
        UPDATE notification_providers SET
            enabled      = COALESCE($3, enabled),
            display_name = COALESCE($4, display_name),
            api_key      = COALESCE(NULLIF($5,''), api_key),
            api_user     = COALESCE($6, api_user),
            from_address = COALESCE($7, from_address),
            from_name    = COALESCE($8, from_name),
            region       = COALESCE($9, region),
            updated_at   = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+notifyCols,
		id, tenantID,
		req.Enabled, req.DisplayName,
		strDeref(req.APIKey), req.APIUser, req.FromAddress, req.FromName, req.Region,
	), &n)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "notification_provider.updated", "notification_provider", id, map[string]any{
		"_tenant_id": tenantID,
	})
	WriteJSON(w, http.StatusOK, n)
}

func (b *NotificationAdminBase) del(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx,
		`DELETE FROM notification_providers WHERE id=$1 AND tenant_id=$2`, id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "notification_provider.deleted", "notification_provider", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

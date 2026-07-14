package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type TicketTemplate struct {
	ID                   string          `json:"id"`
	TenantID             string          `json:"tenant_id"`
	Code                 string          `json:"code"`
	Name                 string          `json:"name"`
	Description          *string         `json:"description,omitempty"`
	DurationSeconds      *int            `json:"duration_seconds,omitempty"`
	DataCapBytes         *int64          `json:"data_cap_bytes,omitempty"`
	DownKbps             *int            `json:"down_kbps,omitempty"`
	UpKbps               *int            `json:"up_kbps,omitempty"`
	MaxConcurrentDevices int             `json:"max_concurrent_devices"`
	ValiditySeconds      *int            `json:"validity_seconds,omitempty"`
	Schedule             json.RawMessage `json:"schedule,omitempty"`
	PriceCents           *int            `json:"price_cents,omitempty"`
	Currency             *string         `json:"currency,omitempty"`
	IsActive             bool            `json:"is_active"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

// Pointer fields let PATCH distinguish "omit" (nil → keep old value) from
// "set". Phase 3 does not support explicit NULL-clears in PATCH; a field must
// be re-created to be un-set.
type templateReq struct {
	Code                 string          `json:"code,omitempty"` // immutable after create
	Name                 string          `json:"name,omitempty"`
	Description          *string         `json:"description,omitempty"`
	DurationSeconds      *int            `json:"duration_seconds,omitempty"`
	DataCapBytes         *int64          `json:"data_cap_bytes,omitempty"`
	DownKbps             *int            `json:"down_kbps,omitempty"`
	UpKbps               *int            `json:"up_kbps,omitempty"`
	MaxConcurrentDevices *int            `json:"max_concurrent_devices,omitempty"`
	ValiditySeconds      *int            `json:"validity_seconds,omitempty"`
	Schedule             json.RawMessage `json:"schedule,omitempty"`
	PriceCents           *int            `json:"price_cents,omitempty"`
	Currency             *string         `json:"currency,omitempty"`
	IsActive             *bool           `json:"is_active,omitempty"`
}

func (r *templateReq) validate(isCreate bool) string {
	if isCreate {
		if r.Code == "" {
			return "code required"
		}
		if r.Name == "" {
			return "name required"
		}
	}
	nonNeg := func(p *int, name string) string {
		if p != nil && *p < 0 {
			return name + " must be >= 0"
		}
		return ""
	}
	if msg := nonNeg(r.DurationSeconds, "duration_seconds"); msg != "" {
		return msg
	}
	if msg := nonNeg(r.DownKbps, "down_kbps"); msg != "" {
		return msg
	}
	if msg := nonNeg(r.UpKbps, "up_kbps"); msg != "" {
		return msg
	}
	if msg := nonNeg(r.ValiditySeconds, "validity_seconds"); msg != "" {
		return msg
	}
	if msg := nonNeg(r.PriceCents, "price_cents"); msg != "" {
		return msg
	}
	if r.DataCapBytes != nil && *r.DataCapBytes < 0 {
		return "data_cap_bytes must be >= 0"
	}
	if r.MaxConcurrentDevices != nil && *r.MaxConcurrentDevices < 1 {
		return "max_concurrent_devices must be >= 1"
	}
	return ""
}

const ttReturning = `id, tenant_id, code, name, description,
       duration_seconds, data_cap_bytes, down_kbps, up_kbps,
       max_concurrent_devices, validity_seconds, schedule,
       price_cents, currency, is_active, created_at, updated_at`

func scanTemplate(row interface{ Scan(...any) error }, t *TicketTemplate) error {
	return row.Scan(
		&t.ID, &t.TenantID, &t.Code, &t.Name, &t.Description,
		&t.DurationSeconds, &t.DataCapBytes, &t.DownKbps, &t.UpKbps,
		&t.MaxConcurrentDevices, &t.ValiditySeconds, &t.Schedule,
		&t.PriceCents, &t.Currency, &t.IsActive, &t.CreatedAt, &t.UpdatedAt,
	)
}

func (b *Base) TemplatesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", b.listTemplates)
	r.Post("/", b.createTemplate)
	r.Get("/{id}", b.getTemplate)
	r.Patch("/{id}", b.patchTemplate)
	r.Delete("/{id}", b.deleteTemplate)
	return r
}

func (b *Base) listTemplates(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()

	limit := ParseLimit(r, 50, 200)
	curT, curI, err := DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, err.Error())
		return
	}
	var tArg, iArg any
	if !curT.IsZero() {
		tArg = curT
	}
	if curI != "" {
		iArg = curI
	}

	rows, err := b.DB.Query(ctx, `
        SELECT `+ttReturning+`
          FROM ticket_templates
         WHERE tenant_id = $1
           AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3::uuid))
         ORDER BY created_at DESC, id DESC
         LIMIT $4
    `, tenantID, tArg, iArg, limit+1)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []TicketTemplate
	for rows.Next() {
		var t TicketTemplate
		if err := scanTemplate(rows, &t); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, t)
	}
	meta := ListMeta{}
	if len(out) > limit {
		last := out[limit-1]
		meta.HasMore = true
		meta.Cursor = EncodeCursor(last.CreatedAt, last.ID)
		out = out[:limit]
	}
	WriteList(w, out, meta)
}

func (b *Base) createTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req templateReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if msg := req.validate(true); msg != "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, msg)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Defaults for create-only fields.
	mcd := 1
	if req.MaxConcurrentDevices != nil {
		mcd = *req.MaxConcurrentDevices
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	var scheduleArg any
	if len(req.Schedule) > 0 {
		scheduleArg = string(req.Schedule)
	}

	var t TicketTemplate
	err := b.DB.QueryRow(ctx, `
        INSERT INTO ticket_templates
          (tenant_id, code, name, description, duration_seconds, data_cap_bytes,
           down_kbps, up_kbps, max_concurrent_devices, validity_seconds,
           schedule, price_cents, currency, is_active)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14)
        RETURNING `+ttReturning,
		tenantID, req.Code, req.Name, req.Description, req.DurationSeconds, req.DataCapBytes,
		req.DownKbps, req.UpKbps, mcd, req.ValiditySeconds,
		scheduleArg, req.PriceCents, req.Currency, active,
	).Scan(
		&t.ID, &t.TenantID, &t.Code, &t.Name, &t.Description,
		&t.DurationSeconds, &t.DataCapBytes, &t.DownKbps, &t.UpKbps,
		&t.MaxConcurrentDevices, &t.ValiditySeconds, &t.Schedule,
		&t.PriceCents, &t.Currency, &t.IsActive, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "code conflict or insert failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "ticket_template.created", "ticket_template", t.ID, map[string]any{
		"_tenant_id": tenantID, "code": t.Code, "name": t.Name,
	})
	WriteJSON(w, http.StatusCreated, t)
}

func (b *Base) getTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var t TicketTemplate
	err := scanTemplate(b.DB.QueryRow(ctx,
		`SELECT `+ttReturning+` FROM ticket_templates WHERE id = $1 AND tenant_id = $2`,
		id, tenantID), &t)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "template not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

func (b *Base) patchTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	var req templateReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if msg := req.validate(false); msg != "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, msg)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// PATCH semantics: nil pointer = don't touch. COALESCE on the SQL side.
	var nameArg any
	if req.Name != "" {
		nameArg = req.Name
	}
	var schedArg any
	if len(req.Schedule) > 0 {
		schedArg = string(req.Schedule)
	}
	var t TicketTemplate
	err := b.DB.QueryRow(ctx, `
        UPDATE ticket_templates SET
            name                   = COALESCE($3, name),
            description            = COALESCE($4, description),
            duration_seconds       = COALESCE($5, duration_seconds),
            data_cap_bytes         = COALESCE($6, data_cap_bytes),
            down_kbps              = COALESCE($7, down_kbps),
            up_kbps                = COALESCE($8, up_kbps),
            max_concurrent_devices = COALESCE($9, max_concurrent_devices),
            validity_seconds       = COALESCE($10, validity_seconds),
            schedule               = COALESCE($11::jsonb, schedule),
            price_cents            = COALESCE($12, price_cents),
            currency               = COALESCE($13, currency),
            is_active              = COALESCE($14, is_active),
            updated_at             = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+ttReturning,
		id, tenantID,
		nameArg, req.Description, req.DurationSeconds, req.DataCapBytes,
		req.DownKbps, req.UpKbps, req.MaxConcurrentDevices, req.ValiditySeconds,
		schedArg, req.PriceCents, req.Currency, req.IsActive,
	).Scan(
		&t.ID, &t.TenantID, &t.Code, &t.Name, &t.Description,
		&t.DurationSeconds, &t.DataCapBytes, &t.DownKbps, &t.UpKbps,
		&t.MaxConcurrentDevices, &t.ValiditySeconds, &t.Schedule,
		&t.PriceCents, &t.Currency, &t.IsActive, &t.CreatedAt, &t.UpdatedAt,
	)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "template not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

func (b *Base) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx,
		`DELETE FROM ticket_templates WHERE id = $1 AND tenant_id = $2`,
		id, tenantID)
	if err != nil {
		// Most likely cause: FK from vouchers.template_id (ON DELETE RESTRICT).
		Fail(w, r, http.StatusConflict, CodeConflict, "template in use by vouchers; deactivate instead")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "template not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "ticket_template.deleted", "ticket_template", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

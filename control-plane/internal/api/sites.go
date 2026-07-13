package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type Site struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenant_id"`
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	Timezone  string          `json:"timezone"`
	Country   string          `json:"country,omitempty"`
	Status    string          `json:"status"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type siteWriteReq struct {
	Code     string          `json:"code"`
	Name     string          `json:"name"`
	Timezone string          `json:"timezone"`
	Country  string          `json:"country"`
	Metadata json.RawMessage `json:"metadata"`
}

func (b *Base) SitesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", b.listSites)
	r.Post("/", b.createSite)
	r.Get("/{id}", b.getSite)
	r.Patch("/{id}", b.patchSite)
	r.Post("/{id}/archive", b.archiveSite)
	r.Post("/{id}/restore", b.restoreSite)
	r.With(RequireReauth(b.Redis)).Delete("/{id}", b.deleteSite)
	return r
}

func (b *Base) listSites(w http.ResponseWriter, r *http.Request) {
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

	// Archived sites are hidden from active lists unless explicitly requested.
	includeArchived := r.URL.Query().Get("status") == "all" || r.URL.Query().Get("include_archived") == "true"

	rows, err := b.DB.Query(ctx, `
        SELECT id, tenant_id, code, name, timezone, COALESCE(country,''), status, metadata, created_at, updated_at
          FROM sites
         WHERE tenant_id = $1
           AND ($5 OR status <> 'archived')
           AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3::uuid))
         ORDER BY created_at DESC, id DESC
         LIMIT $4
    `, tenantID, tArg, iArg, limit+1, includeArchived)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []Site
	for rows.Next() {
		var s Site
		if err := rows.Scan(&s.ID, &s.TenantID, &s.Code, &s.Name, &s.Timezone, &s.Country, &s.Status, &s.Metadata, &s.CreatedAt, &s.UpdatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, s)
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

func (b *Base) createSite(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req siteWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if req.Code == "" || req.Name == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "code and name required")
		return
	}
	tz := req.Timezone
	if tz == "" {
		tz = "UTC"
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	if !EnforceCreateLimit(ctx, b.DB, w, r, tenantID, "max_sites",
		`SELECT count(*) FROM sites WHERE tenant_id = $1`, tenantID) {
		return
	}

	meta := req.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	var s Site
	err := b.DB.QueryRow(ctx, `
        INSERT INTO sites (tenant_id, code, name, timezone, country, metadata)
        VALUES ($1, $2, $3, $4, NULLIF($5,''), $6::jsonb)
        RETURNING id, tenant_id, code, name, timezone, COALESCE(country,''), status, metadata, created_at, updated_at
    `, tenantID, req.Code, req.Name, tz, req.Country, string(meta)).Scan(
		&s.ID, &s.TenantID, &s.Code, &s.Name, &s.Timezone, &s.Country, &s.Status, &s.Metadata, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "code conflict or insert failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "site.created", "site", s.ID, map[string]any{
		"_tenant_id": tenantID, "code": s.Code, "name": s.Name,
	})
	WriteJSON(w, http.StatusCreated, s)
}

func (b *Base) getSite(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var s Site
	err := b.DB.QueryRow(ctx, `
        SELECT id, tenant_id, code, name, timezone, COALESCE(country,''), status, metadata, created_at, updated_at
          FROM sites WHERE id = $1 AND tenant_id = $2
    `, id, tenantID).Scan(&s.ID, &s.TenantID, &s.Code, &s.Name, &s.Timezone, &s.Country, &s.Status, &s.Metadata, &s.CreatedAt, &s.UpdatedAt)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, s)
}

func (b *Base) patchSite(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	var req siteWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	var metaArg any
	if len(req.Metadata) > 0 {
		metaArg = string(req.Metadata)
	}
	var s Site
	err := b.DB.QueryRow(ctx, `
        UPDATE sites SET
            name       = COALESCE(NULLIF($3,''), name),
            timezone   = COALESCE(NULLIF($4,''), timezone),
            country    = COALESCE(NULLIF($5,''), country),
            metadata   = COALESCE($6::jsonb, metadata),
            updated_at = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING id, tenant_id, code, name, timezone, COALESCE(country,''), status, metadata, created_at, updated_at
    `, id, tenantID, req.Name, req.Timezone, req.Country, metaArg).Scan(
		&s.ID, &s.TenantID, &s.Code, &s.Name, &s.Timezone, &s.Country, &s.Status, &s.Metadata, &s.CreatedAt, &s.UpdatedAt,
	)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	WriteJSON(w, http.StatusOK, s)
}

// archiveSite hides a Site from active lists without deleting anything.
func (b *Base) archiveSite(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx, `UPDATE sites SET status='archived', updated_at=now() WHERE id=$1 AND tenant_id=$2`, id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "site.archived", "site", id, map[string]any{"_tenant_id": tenantID})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "archived", "id": id})
}

// restoreSite returns an archived Site to active status.
func (b *Base) restoreSite(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	ct, err := b.DB.Exec(ctx, `UPDATE sites SET status='active', updated_at=now() WHERE id=$1 AND tenant_id=$2`, id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "site.restored", "site", id, map[string]any{"_tenant_id": tenantID})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "active", "id": id})
}

// deleteSite PERMANENTLY deletes a Site, but ONLY when it is empty: no assigned
// Appliances, no active Licenses, no live Enrollment Tokens, no pending
// operations. Otherwise it returns a 409 listing exactly what blocks it. Inactive
// (revoked/superseded/expired) licenses and spent tokens for the site are cleaned
// up as part of the delete; their history is preserved in the audit log. The DB
// RESTRICT constraints (0037) are the backstop. Requires step-up + typed name +
// reason.
func (b *Base) deleteSite(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	var s Site
	err := b.DB.QueryRow(ctx, `SELECT id, code, name FROM sites WHERE id=$1 AND tenant_id=$2`, id, tenantID).
		Scan(&s.ID, &s.Code, &s.Name)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "site not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}

	var req deleteConfirm
	_ = DecodeJSON(r, &req)
	if req.Confirm != s.Code && req.Confirm != s.Name {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest,
			"type the exact Site code or name to confirm permanent deletion",
			map[string]any{"expected": s.Code})
		return
	}

	blockers, err := b.siteBlockers(ctx, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "dependency check failed: "+err.Error())
		return
	}
	if len(blockers) > 0 {
		failBlocked(w, r, "Site", blockers)
		return
	}

	audit.Op(r.Context(), b.DB, r, "site.deleted", "site", id, map[string]any{
		"_tenant_id": tenantID, "code": s.Code, "name": s.Name, "reason": req.Reason,
	})

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	// Deleting the site cascades to session rows whose statement-level read-only
	// guard fires even at zero rows; disable it for the tx, re-enable before commit.
	toggled, err := disableLegacyGuards(ctx, tx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed: "+err.Error())
		return
	}
	for _, stmt := range []string{
		`DELETE FROM licenses WHERE site_id=$1 AND status IN ('revoked','superseded','expired')`,
		`DELETE FROM appliance_bootstrap_tokens WHERE site_id=$1`,
		`DELETE FROM sites WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed: "+err.Error())
			return
		}
	}
	if err := enableLegacyGuards(ctx, tx, toggled); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

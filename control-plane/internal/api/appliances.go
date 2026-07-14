package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type Appliance struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id"`
	SiteID     string          `json:"site_id"`
	Serial     string          `json:"serial"`
	Name       string          `json:"name"`
	Model      string          `json:"model,omitempty"`
	Version    string          `json:"version,omitempty"`
	EnrolledAt *time.Time      `json:"enrolled_at,omitempty"`
	LastSeenAt *time.Time      `json:"last_seen_at,omitempty"`
	Status     string          `json:"status"`
	PublicKey  string          `json:"public_key,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type appWriteReq struct {
	SiteID    string          `json:"site_id"`
	Serial    string          `json:"serial"`
	Name      string          `json:"name"`
	Model     string          `json:"model"`
	Version   string          `json:"version"`
	Status    string          `json:"status"`
	PublicKey string          `json:"public_key"`
	Metadata  json.RawMessage `json:"metadata"`
}

func (b *Base) AppliancesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenantOrPlatform)
	r.Get("/", b.listAppliances)
	r.Post("/", b.createAppliance)
	r.Get("/{id}", b.getAppliance)
	r.Patch("/{id}", b.patchAppliance)
	r.Delete("/{id}", b.deleteAppliance)
	r.Get("/{id}/effective-config", b.effectiveConfig) // phase 5.7.D
	return r
}

// EffectiveConfig is the resolved view of "what scd at this appliance
// should currently be enforcing": PMS providers after override resolution
// (5.6) + walled-garden rules (tenant-wide ∪ site-scoped, 5.7.A).
//
// Source of truth is the DB; we don't round-trip to scd. If scd's runtime
// state diverges from this, that's a bug worth seeing — diff it against
// the live /v1/pms-providers/{name}/health endpoint.
type EffectiveConfig struct {
	ApplianceID  string             `json:"appliance_id"`
	TenantID     string             `json:"tenant_id"`
	SiteID       string             `json:"site_id"`
	PMSProviders []PMSProvider      `json:"pms_providers"`
	WalledGarden []WalledGardenRule `json:"walled_garden"`
}

func (b *Base) effectiveConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	var siteID string
	if err := b.DB.QueryRow(ctx,
		`SELECT site_id::text FROM appliances WHERE id = $1 AND tenant_id = $2`,
		id, tenantID).Scan(&siteID); err != nil {
		if IsNoRows(err) {
			Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
			return
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	}

	out := EffectiveConfig{ApplianceID: id, TenantID: tenantID, SiteID: siteID}

	// PMS — same resolution logic as the data-plane loader: prefer
	// site-scoped over tenant-wide when the same name appears in both.
	// Guest-domain tables live in the SITE database post-cutover, so this
	// cloud-side view reads them through the guest pool (b.GuestDB).
	pmsRows, err := b.guestPool().Query(ctx, `
        SELECT id, tenant_id, COALESCE(site_id::text, ''),
               name, kind, enabled, COALESCE(display_name,''),
               COALESCE(host,''), COALESCE(port,0), use_tls,
               COALESCE(base_url,''), COALESCE(property_id,''),
               extra, field_map, normalization, stay_window,
               status, last_record_at, COALESCE(last_error,''), last_error_at,
               created_at, updated_at
          FROM pms_providers
         WHERE tenant_id = $1
           AND enabled = true
           AND (site_id IS NULL OR site_id = $2)
         ORDER BY name, (site_id IS NOT NULL) DESC
    `, tenantID, siteID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "pms query failed")
		return
	}
	defer pmsRows.Close()
	seenPMS := map[string]bool{}
	for pmsRows.Next() {
		var p PMSProvider
		if err := pmsRows.Scan(&p.ID, &p.TenantID, &p.SiteID,
			&p.Name, &p.Kind, &p.Enabled, &p.DisplayName,
			&p.Host, &p.Port, &p.UseTLS, &p.BaseURL, &p.PropertyID,
			&p.Extra, &p.FieldMap, &p.Normalization, &p.StayWindow,
			&p.Status, &p.LastRecordAt, &p.LastError, &p.LastErrorAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "pms scan failed")
			return
		}
		if seenPMS[p.Name] {
			continue // a site-scoped row already won by ORDER BY
		}
		seenPMS[p.Name] = true
		out.PMSProviders = append(out.PMSProviders, p)
	}

	// Walled-garden — additive union (tenant-wide ∪ site-scoped).
	wgRows, err := b.guestPool().Query(ctx, `
        SELECT `+wgCols+`
          FROM walled_garden_rules
         WHERE tenant_id = $1
           AND (site_id IS NULL OR site_id = $2)
         ORDER BY (site_id IS NULL) DESC, created_at DESC, id DESC
    `, tenantID, siteID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "wg query failed")
		return
	}
	defer wgRows.Close()
	for wgRows.Next() {
		var wg WalledGardenRule
		if err := scanWG(wgRows, &wg); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "wg scan failed")
			return
		}
		out.WalledGarden = append(out.WalledGarden, wg)
	}

	WriteJSON(w, http.StatusOK, out)
}

func (b *Base) listAppliances(w http.ResponseWriter, r *http.Request) {
	// Global Customer Context: super-admin may omit tenant_id for an all-customers
	// fan-out; a regular operator stays pinned to their own tenant.
	tenantID, ok := b.tenantScopeForList(w, r)
	if !ok {
		return
	}
	siteFilter := r.URL.Query().Get("site_id")
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
	var siteArg any
	if siteFilter != "" {
		siteArg = siteFilter
	}

	rows, err := b.DB.Query(ctx, `
        SELECT id, tenant_id, site_id, serial, name,
               COALESCE(model,''), COALESCE(version,''),
               enrolled_at, last_seen_at, status,
               COALESCE(public_key,''), metadata, created_at, updated_at
          FROM appliances
         WHERE tenant_id IS NOT NULL
           AND ($1 = '' OR tenant_id::text = $1)
           AND ($2::uuid IS NULL OR site_id = $2)
           AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4::uuid))
         ORDER BY created_at DESC, id DESC
         LIMIT $5
    `, tenantID, siteArg, tArg, iArg, limit+1)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []Appliance
	for rows.Next() {
		var a Appliance
		if err := rows.Scan(&a.ID, &a.TenantID, &a.SiteID, &a.Serial, &a.Name,
			&a.Model, &a.Version, &a.EnrolledAt, &a.LastSeenAt, &a.Status,
			&a.PublicKey, &a.Metadata, &a.CreatedAt, &a.UpdatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, a)
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

func (b *Base) createAppliance(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req appWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if req.SiteID == "" || req.Serial == "" || req.Name == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site_id, serial and name required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Verify site belongs to this tenant.
	var siteCount int
	if err := b.DB.QueryRow(ctx,
		`SELECT count(*) FROM sites WHERE id = $1 AND tenant_id = $2`,
		req.SiteID, tenantID).Scan(&siteCount); err != nil || siteCount == 0 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not found in this tenant")
		return
	}

	if !EnforceCreateLimit(ctx, b.DB, w, r, tenantID, "max_appliances",
		`SELECT count(*) FROM appliances WHERE tenant_id = $1`, tenantID) {
		return
	}

	status := req.Status
	if status == "" {
		status = "pending"
	}
	meta := req.Metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	var a Appliance
	err := b.DB.QueryRow(ctx, `
        INSERT INTO appliances (tenant_id, site_id, serial, name, model, version, status, public_key, metadata)
        VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), $7, NULLIF($8,''), $9::jsonb)
        RETURNING id, tenant_id, site_id, serial, name, COALESCE(model,''), COALESCE(version,''),
                  enrolled_at, last_seen_at, status, COALESCE(public_key,''),
                  metadata, created_at, updated_at
    `, tenantID, req.SiteID, req.Serial, req.Name, req.Model, req.Version, status, req.PublicKey, string(meta)).Scan(
		&a.ID, &a.TenantID, &a.SiteID, &a.Serial, &a.Name, &a.Model, &a.Version,
		&a.EnrolledAt, &a.LastSeenAt, &a.Status, &a.PublicKey, &a.Metadata, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "serial conflict or insert failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "appliance.created", "appliance", a.ID, map[string]any{
		"_tenant_id": tenantID, "site_id": a.SiteID, "serial": a.Serial, "name": a.Name,
	})
	WriteJSON(w, http.StatusCreated, a)
}

func (b *Base) getAppliance(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	var a Appliance
	err := b.DB.QueryRow(ctx, `
        SELECT id, tenant_id, site_id, serial, name, COALESCE(model,''), COALESCE(version,''),
               enrolled_at, last_seen_at, status, COALESCE(public_key,''),
               metadata, created_at, updated_at
          FROM appliances WHERE id = $1 AND tenant_id = $2
    `, id, tenantID).Scan(
		&a.ID, &a.TenantID, &a.SiteID, &a.Serial, &a.Name, &a.Model, &a.Version,
		&a.EnrolledAt, &a.LastSeenAt, &a.Status, &a.PublicKey, &a.Metadata, &a.CreatedAt, &a.UpdatedAt,
	)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, a)
}

func (b *Base) patchAppliance(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	var req appWriteReq
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
	var a Appliance
	err := b.DB.QueryRow(ctx, `
        UPDATE appliances SET
            name       = COALESCE(NULLIF($3,''), name),
            model      = COALESCE(NULLIF($4,''), model),
            version    = COALESCE(NULLIF($5,''), version),
            status     = COALESCE(NULLIF($6,''), status),
            public_key = COALESCE(NULLIF($7,''), public_key),
            metadata   = COALESCE($8::jsonb, metadata),
            updated_at = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING id, tenant_id, site_id, serial, name, COALESCE(model,''), COALESCE(version,''),
                   enrolled_at, last_seen_at, status, COALESCE(public_key,''),
                   metadata, created_at, updated_at
    `, id, tenantID, req.Name, req.Model, req.Version, req.Status, req.PublicKey, metaArg).Scan(
		&a.ID, &a.TenantID, &a.SiteID, &a.Serial, &a.Name, &a.Model, &a.Version,
		&a.EnrolledAt, &a.LastSeenAt, &a.Status, &a.PublicKey, &a.Metadata, &a.CreatedAt, &a.UpdatedAt,
	)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	WriteJSON(w, http.StatusOK, a)
}

func (b *Base) deleteAppliance(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Centralized license termination — revoke licenses BOUND to this appliance
	// first, so deleting it (appliance_ids is an FK-less uuid[]) never leaves an
	// orphaned active license.
	revoked, _ := b.revokeApplianceBoundLicenses(ctx, id)

	ct, err := b.DB.Exec(ctx, `DELETE FROM appliances WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if ct.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	audit.Op(r.Context(), b.DB, r, "appliance.deleted", "appliance", id, map[string]any{"_tenant_id": tenantID, "licenses_revoked": len(revoked)})
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/configpush"
	"github.com/stayconnect/enterprise/control-plane/internal/transport"
)

var pmsAllowedKinds = map[string]bool{
	"stub":         true,
	"protel-fias":  true,
	"opera-fias":   true,
	"fidelio-fias": true,
	"mews":         true,
	"apaleo":       true,
}

// PMSAdminBase exposes read-only admin endpoints for PMS providers: listing
// configured rows and proxying test/cache/health through the appliance
// transport. Write operations (create/update/delete of pms_providers rows)
// are deferred to 4.5.5c alongside the admin UI.
type PMSAdminBase struct {
	*Base
	Transport  transport.ApplianceTransport
	ConfigPush *configpush.Pusher
}

// PMSProvider is the read shape for a pms_providers row. Connection secrets
// (auth_key, api_key) are deliberately never returned.
type PMSProvider struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id"`
	SiteID        string          `json:"site_id,omitempty"` // empty → tenant-wide
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Enabled       bool            `json:"enabled"`
	DisplayName   string          `json:"display_name,omitempty"`
	Host          string          `json:"host,omitempty"`
	Port          int             `json:"port,omitempty"`
	UseTLS        bool            `json:"use_tls"`
	BaseURL       string          `json:"base_url,omitempty"`
	PropertyID    string          `json:"property_id,omitempty"`
	Extra         json.RawMessage `json:"extra,omitempty"`
	FieldMap      json.RawMessage `json:"field_map,omitempty"`
	Normalization json.RawMessage `json:"normalization,omitempty"`
	StayWindow    json.RawMessage `json:"stay_window,omitempty"`
	Status        string          `json:"status"`
	LastRecordAt  *time.Time      `json:"last_record_at,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	LastErrorAt   *time.Time      `json:"last_error_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func (p *PMSAdminBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	r.Get("/", p.list)
	r.Post("/", p.create)
	r.Get("/{name}", p.get)
	r.Patch("/{name}", p.patch)
	r.Delete("/{name}", p.del)
	r.Post("/{name}/test", p.test)
	r.Get("/{name}/cache", p.cache)
	r.Get("/{name}/health", p.health)
	return r
}

// pmsWriteReq is accepted for both create and patch. Fields left empty on
// patch are left untouched; jsonb fields use a "present but empty object"
// signal to clear. Secrets (auth_key, api_key) are write-only: they never
// read back and patches omit them to retain the stored value.
type pmsWriteReq struct {
	Name          string          `json:"name"`    // create-only
	Kind          string          `json:"kind"`    // create-only
	SiteID        string          `json:"site_id"` // create-only; empty = tenant-wide
	Enabled       *bool           `json:"enabled,omitempty"`
	DisplayName   *string         `json:"display_name,omitempty"`
	Host          *string         `json:"host,omitempty"`
	Port          *int            `json:"port,omitempty"`
	UseTLS        *bool           `json:"use_tls,omitempty"`
	AuthKey       *string         `json:"auth_key,omitempty"`
	BaseURL       *string         `json:"base_url,omitempty"`
	APIKey        *string         `json:"api_key,omitempty"`
	PropertyID    *string         `json:"property_id,omitempty"`
	Extra         json.RawMessage `json:"extra,omitempty"`
	FieldMap      json.RawMessage `json:"field_map,omitempty"`
	Normalization json.RawMessage `json:"normalization,omitempty"`
	StayWindow    json.RawMessage `json:"stay_window,omitempty"`
}

// scopeArg parses ?site_id=... out of the URL into an argument pair that
// matches the SQL idiom `(site_id = $N OR ($N IS NULL AND site_id IS NULL))`.
// Returns the raw string for response rendering.
func siteScopeArg(r *http.Request) (string, any) {
	s := strings.TrimSpace(r.URL.Query().Get("site_id"))
	if s == "" {
		return "", nil
	}
	return s, s
}

func (p *PMSAdminBase) list(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	// Tenant-wide rows sort first, then site-scoped ones grouped by name —
	// the UI renders them adjacent so an operator can spot overrides.
	rows, err := p.DB.Query(ctx, `
        SELECT id, tenant_id, COALESCE(site_id::text, ''),
               name, kind, enabled, COALESCE(display_name,''),
               COALESCE(host,''), COALESCE(port,0), use_tls,
               COALESCE(base_url,''), COALESCE(property_id,''),
               extra, field_map, normalization, stay_window,
               status, last_record_at, COALESCE(last_error,''), last_error_at,
               created_at, updated_at
          FROM pms_providers
         WHERE tenant_id = $1
         ORDER BY name, (site_id IS NOT NULL)
    `, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []PMSProvider
	for rows.Next() {
		var p PMSProvider
		if err := rows.Scan(&p.ID, &p.TenantID, &p.SiteID,
			&p.Name, &p.Kind, &p.Enabled, &p.DisplayName,
			&p.Host, &p.Port, &p.UseTLS, &p.BaseURL, &p.PropertyID,
			&p.Extra, &p.FieldMap, &p.Normalization, &p.StayWindow,
			&p.Status, &p.LastRecordAt, &p.LastError, &p.LastErrorAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, p)
	}
	WriteList(w, out, ListMeta{})
}

// loadOne targets either the tenant-wide row (?site_id= missing) or a
// specific site-scoped row (?site_id=<uuid>). The same row shape is used
// for all three endpoints (get/patch/delete).
func (p *PMSAdminBase) loadOne(r *http.Request) (*PMSProvider, error) {
	tenantID := auth.EffectiveTenantID(r)
	name := chi.URLParam(r, "name")
	_, siteArg := siteScopeArg(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var row PMSProvider
	err := p.DB.QueryRow(ctx, `
        SELECT id, tenant_id, COALESCE(site_id::text, ''),
               name, kind, enabled, COALESCE(display_name,''),
               COALESCE(host,''), COALESCE(port,0), use_tls,
               COALESCE(base_url,''), COALESCE(property_id,''),
               extra, field_map, normalization, stay_window,
               status, last_record_at, COALESCE(last_error,''), last_error_at,
               created_at, updated_at
          FROM pms_providers
         WHERE tenant_id = $1 AND name = $2
           AND (($3::uuid IS NULL AND site_id IS NULL) OR site_id = $3::uuid)
    `, tenantID, name, siteArg).Scan(&row.ID, &row.TenantID, &row.SiteID,
		&row.Name, &row.Kind, &row.Enabled, &row.DisplayName,
		&row.Host, &row.Port, &row.UseTLS, &row.BaseURL, &row.PropertyID,
		&row.Extra, &row.FieldMap, &row.Normalization, &row.StayWindow,
		&row.Status, &row.LastRecordAt, &row.LastError, &row.LastErrorAt,
		&row.CreatedAt, &row.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (p *PMSAdminBase) get(w http.ResponseWriter, r *http.Request) {
	row, err := p.loadOne(r)
	if IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	WriteJSON(w, http.StatusOK, row)
}

// tenantAppliance picks the first appliance belonging to this tenant. For the
// local-unix transport it's cosmetic — scd ignores applianceID — but wiring it
// in now keeps the handler honest when the NATS transport lands.
func (p *PMSAdminBase) tenantAppliance(r *http.Request) string {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	var id string
	_ = p.DB.QueryRow(ctx,
		`SELECT id FROM appliances WHERE tenant_id = $1 ORDER BY created_at LIMIT 1`,
		tenantID).Scan(&id)
	return id
}

func (p *PMSAdminBase) test(w http.ResponseWriter, r *http.Request) {
	if _, err := p.loadOne(r); IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	} else if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	res, err := p.Transport.PMSTest(r.Context(), p.tenantAppliance(r), chi.URLParam(r, "name"))
	if err != nil {
		Fail(w, r, http.StatusBadGateway, CodeBadGateway, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

func (p *PMSAdminBase) cache(w http.ResponseWriter, r *http.Request) {
	if _, err := p.loadOne(r); IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	} else if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	limit := ParseLimit(r, 50, 500)
	res, err := p.Transport.PMSCache(r.Context(), p.tenantAppliance(r), chi.URLParam(r, "name"), limit)
	if err != nil {
		Fail(w, r, http.StatusBadGateway, CodeBadGateway, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

// pmsValidName rejects obvious abuse. Names are used as URL segments and as
// scd's in-memory registry key; keep the charset narrow.
func pmsValidName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

func jsonOr(raw json.RawMessage, def string) string {
	if len(raw) == 0 {
		return def
	}
	// sanity — reject non-object payloads
	s := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(s, "{") {
		return def
	}
	return s
}

func (p *PMSAdminBase) create(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req pmsWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Kind = strings.TrimSpace(req.Kind)
	if !pmsValidName(req.Name) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "name required (a-z, 0-9, -, _; max 64)")
		return
	}
	if !pmsAllowedKinds[req.Kind] {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "unsupported kind")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Site-scope validation: if a site_id is requested it must belong to
	// the caller's tenant.
	var siteArg any
	siteID := strings.TrimSpace(req.SiteID)
	if siteID != "" {
		var count int
		if err := p.DB.QueryRow(ctx,
			`SELECT count(*) FROM sites WHERE id = $1 AND tenant_id = $2`,
			siteID, tenantID).Scan(&count); err != nil || count == 0 {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not in tenant")
			return
		}
		siteArg = siteID
	}

	var id string
	err := p.DB.QueryRow(ctx, `
        INSERT INTO pms_providers(
            tenant_id, site_id, name, kind, enabled, display_name,
            host, port, use_tls, auth_key,
            base_url, api_key, property_id,
            extra, field_map, normalization, stay_window
        ) VALUES (
            $1, $2, $3, $4, $5, NULLIF($6,''),
            NULLIF($7,''), NULLIF($8,0)::int, $9, NULLIF($10,''),
            NULLIF($11,''), NULLIF($12,''), NULLIF($13,''),
            $14::jsonb, $15::jsonb, $16::jsonb, $17::jsonb
        )
        RETURNING id
    `,
		tenantID, siteArg, req.Name, req.Kind, enabled, strDeref(req.DisplayName),
		strDeref(req.Host), intDeref(req.Port), boolDeref(req.UseTLS), strDeref(req.AuthKey),
		strDeref(req.BaseURL), strDeref(req.APIKey), strDeref(req.PropertyID),
		jsonOr(req.Extra, "{}"), jsonOr(req.FieldMap, "{}"),
		jsonOr(req.Normalization, "{}"), jsonOr(req.StayWindow, "{}"),
	).Scan(&id)
	if err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "name taken at that scope or insert failed")
		return
	}
	audit.Op(r.Context(), p.DB, r, "pms_provider.created", "pms_provider", id, map[string]any{
		"_tenant_id": tenantID, "name": req.Name, "kind": req.Kind, "site_id": siteID,
	})
	p.ConfigPush.PMS(r.Context(), tenantID, "created", req.Name)
	// Echo the newly-created row via loadOne. Forward the site scope so
	// loadOne picks the right row (tenant-wide or site-scoped).
	rctx := chi.RouteContext(r.Context())
	rctx.URLParams.Add("name", req.Name)
	if siteID != "" {
		q := r.URL.Query()
		q.Set("site_id", siteID)
		r.URL.RawQuery = q.Encode()
	}
	row, err := p.loadOne(r)
	if err != nil {
		WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "name": req.Name, "site_id": siteID})
		return
	}
	WriteJSON(w, http.StatusCreated, row)
}

func (p *PMSAdminBase) patch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	name := chi.URLParam(r, "name")
	var req pmsWriteReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	// Secrets: NULLIF('', ...) on empty means "skip"; COALESCE preserves existing.
	// For jsonb fields, a missing key leaves the column untouched; passing a
	// raw message (including "{}") overwrites.
	var extra, fmap, norm, stay any
	if req.Extra != nil {
		extra = jsonOr(req.Extra, "{}")
	}
	if req.FieldMap != nil {
		fmap = jsonOr(req.FieldMap, "{}")
	}
	if req.Normalization != nil {
		norm = jsonOr(req.Normalization, "{}")
	}
	if req.StayWindow != nil {
		stay = jsonOr(req.StayWindow, "{}")
	}
	_, siteArg := siteScopeArg(r)
	tag, err := p.DB.Exec(ctx, `
        UPDATE pms_providers SET
            enabled       = COALESCE($4, enabled),
            display_name  = COALESCE($5, display_name),
            host          = COALESCE($6, host),
            port          = COALESCE($7, port),
            use_tls       = COALESCE($8, use_tls),
            auth_key      = COALESCE(NULLIF($9,''), auth_key),
            base_url      = COALESCE($10, base_url),
            api_key       = COALESCE(NULLIF($11,''), api_key),
            property_id   = COALESCE($12, property_id),
            extra         = COALESCE($13::jsonb, extra),
            field_map     = COALESCE($14::jsonb, field_map),
            normalization = COALESCE($15::jsonb, normalization),
            stay_window   = COALESCE($16::jsonb, stay_window),
            updated_at    = now()
         WHERE tenant_id = $1 AND name = $2
           AND (($3::uuid IS NULL AND site_id IS NULL) OR site_id = $3::uuid)
    `,
		tenantID, name, siteArg,
		req.Enabled, req.DisplayName,
		req.Host, req.Port, req.UseTLS, strDeref(req.AuthKey),
		req.BaseURL, strDeref(req.APIKey), req.PropertyID,
		extra, fmap, norm, stay,
	)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	audit.Op(r.Context(), p.DB, r, "pms_provider.updated", "pms_provider", name, map[string]any{
		"_tenant_id": tenantID, "name": name,
	})
	p.ConfigPush.PMS(r.Context(), tenantID, "updated", name)
	row, err := p.loadOne(r)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "reload failed")
		return
	}
	WriteJSON(w, http.StatusOK, row)
}

func (p *PMSAdminBase) del(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	name := chi.URLParam(r, "name")
	_, siteArg := siteScopeArg(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := p.DB.Exec(ctx, `
        DELETE FROM pms_providers
         WHERE tenant_id = $1 AND name = $2
           AND (($3::uuid IS NULL AND site_id IS NULL) OR site_id = $3::uuid)
    `, tenantID, name, siteArg)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	}
	audit.Op(r.Context(), p.DB, r, "pms_provider.deleted", "pms_provider", name, map[string]any{
		"_tenant_id": tenantID, "name": name,
	})
	p.ConfigPush.PMS(r.Context(), tenantID, "deleted", name)
	w.WriteHeader(http.StatusNoContent)
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func intDeref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
func boolDeref(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func (p *PMSAdminBase) health(w http.ResponseWriter, r *http.Request) {
	if _, err := p.loadOne(r); IsNoRows(err) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found")
		return
	} else if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	res, err := p.Transport.PMSHealth(r.Context(), p.tenantAppliance(r), chi.URLParam(r, "name"))
	if err != nil {
		Fail(w, r, http.StatusBadGateway, CodeBadGateway, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

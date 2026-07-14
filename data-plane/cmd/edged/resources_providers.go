package main

// Provider configuration CRUD: PMS, notification (email/sms), social OAuth
// and Stripe accounts. Ported from the control-plane admin handlers with the
// fixed site scope. Secrets are write-only everywhere: never returned in
// responses, and a blank/omitted secret on update keeps the stored value.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// ----- PMS providers -----------------------------------------------------------

var pmsAllowedKinds = map[string]bool{
	"stub":         true,
	"protel-fias":  true,
	"opera-fias":   true,
	"fidelio-fias": true,
	"mews":         true,
	"apaleo":       true,
}

type edgePMSProvider struct {
	ID            string          `json:"id"`
	SiteID        string          `json:"site_id,omitempty"` // kept as-is from sitemigrate
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

const pmsCols = `id, COALESCE(site_id::text, ''),
       name, kind, enabled, COALESCE(display_name,''),
       COALESCE(host,''), COALESCE(port,0), use_tls,
       COALESCE(base_url,''), COALESCE(property_id,''),
       extra, field_map, normalization, stay_window,
       status, last_record_at, COALESCE(last_error,''), last_error_at,
       created_at, updated_at`

func scanPMS(row interface{ Scan(...any) error }, p *edgePMSProvider) error {
	return row.Scan(&p.ID, &p.SiteID,
		&p.Name, &p.Kind, &p.Enabled, &p.DisplayName,
		&p.Host, &p.Port, &p.UseTLS, &p.BaseURL, &p.PropertyID,
		&p.Extra, &p.FieldMap, &p.Normalization, &p.StayWindow,
		&p.Status, &p.LastRecordAt, &p.LastError, &p.LastErrorAt,
		&p.CreatedAt, &p.UpdatedAt)
}

type pmsWriteReq struct {
	Name          string          `json:"name,omitempty"` // create-only
	Kind          string          `json:"kind,omitempty"` // create-only
	Enabled       *bool           `json:"enabled,omitempty"`
	DisplayName   *string         `json:"display_name,omitempty"`
	Host          *string         `json:"host,omitempty"`
	Port          *int            `json:"port,omitempty"`
	UseTLS        *bool           `json:"use_tls,omitempty"`
	AuthKey       *string         `json:"auth_key,omitempty"` // write-only
	BaseURL       *string         `json:"base_url,omitempty"`
	APIKey        *string         `json:"api_key,omitempty"` // write-only
	PropertyID    *string         `json:"property_id,omitempty"`
	Extra         json.RawMessage `json:"extra,omitempty"`
	FieldMap      json.RawMessage `json:"field_map,omitempty"`
	Normalization json.RawMessage `json:"normalization,omitempty"`
	StayWindow    json.RawMessage `json:"stay_window,omitempty"`
}

// pmsValidName keeps the charset narrow: names are URL segments and scd's
// in-memory registry key.
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
	v := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(v, "{") || !json.Valid([]byte(v)) {
		return def
	}
	return v
}

func (s *server) pmsProvidersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listPMSProviders)
	r.Post("/", s.createPMSProvider)
	r.Get("/{name}", s.getPMSProvider)
	r.Patch("/{name}", s.patchPMSProvider)
	r.Delete("/{name}", s.deletePMSProvider)
	r.Post("/{name}/test", s.pmsTest)
	r.Get("/{name}/cache", s.pmsCache)
	r.Get("/{name}/health", s.pmsHealth)
	return r
}

// loadPMSProvider resolves {name}. Rows migrated from the cloud may exist at
// both scopes (site_id NULL and set); the site-local row wins if both exist.
func (s *server) loadPMSProvider(r *http.Request, name string) (*edgePMSProvider, error) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var p edgePMSProvider
	err := scanPMS(s.db.QueryRow(ctx, `
        SELECT `+pmsCols+`
          FROM pms_providers
         WHERE tenant_id = $1 AND name = $2
         ORDER BY (site_id IS NOT NULL) DESC
         LIMIT 1
    `, s.tenantID, name), &p)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *server) listPMSProviders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
        SELECT `+pmsCols+`
          FROM pms_providers
         WHERE tenant_id = $1
         ORDER BY name, (site_id IS NOT NULL)
    `, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgePMSProvider
	for rows.Next() {
		var p edgePMSProvider
		if err := scanPMS(rows, &p); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, p)
	}
	writeList(w, out)
}

func (s *server) getPMSProvider(w http.ResponseWriter, r *http.Request) {
	p, err := s.loadPMSProvider(r, chi.URLParam(r, "name"))
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) createPMSProvider(w http.ResponseWriter, r *http.Request) {
	var in pmsWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Kind = strings.TrimSpace(in.Kind)
	if !pmsValidName(in.Name) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "name required (a-z, 0-9, -, _; max 64)")
		return
	}
	if !pmsAllowedKinds[in.Kind] {
		jsonErr(w, http.StatusBadRequest, "bad_request", "unsupported kind")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	var id string
	err := s.db.QueryRow(ctx, `
        INSERT INTO pms_providers(
            tenant_id, site_id, name, kind, enabled, display_name,
            host, port, use_tls, auth_key,
            base_url, api_key, property_id,
            extra, field_map, normalization, stay_window
        ) VALUES (
            $1, NULL, $2, $3, $4, NULLIF($5,''),
            NULLIF($6,''), NULLIF($7,0)::int, $8, NULLIF($9,''),
            NULLIF($10,''), NULLIF($11,''), NULLIF($12,''),
            $13::jsonb, $14::jsonb, $15::jsonb, $16::jsonb
        )
        RETURNING id
    `,
		s.tenantID, in.Name, in.Kind, enabled, strDeref(in.DisplayName),
		strDeref(in.Host), intDeref(in.Port), boolDeref(in.UseTLS), strDeref(in.AuthKey),
		strDeref(in.BaseURL), strDeref(in.APIKey), strDeref(in.PropertyID),
		jsonOr(in.Extra, "{}"), jsonOr(in.FieldMap, "{}"),
		jsonOr(in.Normalization, "{}"), jsonOr(in.StayWindow, "{}"),
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "provider name already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	s.audit(r, "pms_provider.created", "pms_provider", id, map[string]any{
		"name": in.Name, "kind": in.Kind,
	})
	s.scdReloadWarn("/v1/admin/pms/reload")

	p, err := s.loadPMSProvider(r, in.Name)
	if err != nil {
		writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": in.Name})
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) patchPMSProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var in pmsWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	// jsonb fields: missing key leaves the column untouched; passing a raw
	// object (including "{}") overwrites.
	var extra, fmap, norm, stay any
	if in.Extra != nil {
		extra = jsonOr(in.Extra, "{}")
	}
	if in.FieldMap != nil {
		fmap = jsonOr(in.FieldMap, "{}")
	}
	if in.Normalization != nil {
		norm = jsonOr(in.Normalization, "{}")
	}
	if in.StayWindow != nil {
		stay = jsonOr(in.StayWindow, "{}")
	}

	tag, err := s.db.Exec(ctx, `
        UPDATE pms_providers SET
            enabled       = COALESCE($3, enabled),
            display_name  = COALESCE($4, display_name),
            host          = COALESCE($5, host),
            port          = COALESCE($6, port),
            use_tls       = COALESCE($7, use_tls),
            auth_key      = COALESCE(NULLIF($8,''), auth_key),
            base_url      = COALESCE($9, base_url),
            api_key       = COALESCE(NULLIF($10,''), api_key),
            property_id   = COALESCE($11, property_id),
            extra         = COALESCE($12::jsonb, extra),
            field_map     = COALESCE($13::jsonb, field_map),
            normalization = COALESCE($14::jsonb, normalization),
            stay_window   = COALESCE($15::jsonb, stay_window),
            updated_at    = now()
         WHERE tenant_id = $1 AND name = $2
    `,
		s.tenantID, name,
		in.Enabled, in.DisplayName,
		in.Host, in.Port, in.UseTLS, strDeref(in.AuthKey),
		in.BaseURL, strDeref(in.APIKey), in.PropertyID,
		extra, fmap, norm, stay,
	)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	s.audit(r, "pms_provider.updated", "pms_provider", name, nil)
	s.scdReloadWarn("/v1/admin/pms/reload")

	p, err := s.loadPMSProvider(r, name)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "reload failed")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) deletePMSProvider(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx,
		`DELETE FROM pms_providers WHERE tenant_id = $1 AND name = $2`, s.tenantID, name)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	s.audit(r, "pms_provider.deleted", "pms_provider", name, nil)
	s.scdReloadWarn("/v1/admin/pms/reload")
	w.WriteHeader(http.StatusNoContent)
}

// requirePMSProvider 404s unknown names before proxying to scd — keeps the
// error shape consistent with the CRUD endpoints.
func (s *server) requirePMSProvider(w http.ResponseWriter, r *http.Request, name string) bool {
	_, err := s.loadPMSProvider(r, name)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return false
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return false
	}
	return true
}

func (s *server) pmsTest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !s.requirePMSProvider(w, r, name) {
		return
	}
	s.scd.proxy(w, r, http.MethodPost, "/v1/admin/pms/"+url.PathEscape(name)+"/test", nil)
}

func (s *server) pmsCache(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !s.requirePMSProvider(w, r, name) {
		return
	}
	path := "/v1/admin/pms/" + url.PathEscape(name) + "/cache"
	if l := r.URL.Query().Get("limit"); l != "" {
		path += "?limit=" + url.QueryEscape(l)
	}
	s.scd.proxy(w, r, http.MethodGet, path, nil)
}

func (s *server) pmsHealth(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !s.requirePMSProvider(w, r, name) {
		return
	}
	s.scd.proxy(w, r, http.MethodGet, "/v1/admin/pms/"+url.PathEscape(name)+"/health", nil)
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

// ----- notification providers ----------------------------------------------------

var notifyAllowedKinds = map[string]map[string]bool{
	"email": {"stub": true, "sendgrid": true, "ses": true},
	"sms":   {"stub": true, "twilio": true},
}

type edgeNotificationProvider struct {
	ID            string     `json:"id"`
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

const notifyCols = `id, channel, kind, enabled, COALESCE(display_name,''),
       COALESCE(api_user,''), COALESCE(from_address,''), COALESCE(from_name,''),
       COALESCE(region,''), last_success_at, COALESCE(last_error,''),
       last_error_at, created_at, updated_at`

func scanNotify(row interface{ Scan(...any) error }, n *edgeNotificationProvider) error {
	return row.Scan(&n.ID, &n.Channel, &n.Kind, &n.Enabled, &n.DisplayName,
		&n.APIUser, &n.FromAddress, &n.FromName, &n.Region,
		&n.LastSuccessAt, &n.LastError, &n.LastErrorAt, &n.CreatedAt, &n.UpdatedAt)
}

type notifyWriteReq struct {
	Channel     string  `json:"channel,omitempty"` // create-only
	Kind        string  `json:"kind,omitempty"`    // create-only
	Enabled     *bool   `json:"enabled,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	APIKey      *string `json:"api_key,omitempty"` // write-only
	APIUser     *string `json:"api_user,omitempty"`
	FromAddress *string `json:"from_address,omitempty"`
	FromName    *string `json:"from_name,omitempty"`
	Region      *string `json:"region,omitempty"`
}

func (s *server) notificationProvidersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listNotifyProviders)
	r.Post("/", s.createNotifyProvider)
	r.Get("/{id}", s.getNotifyProvider)
	r.Patch("/{id}", s.patchNotifyProvider)
	r.Delete("/{id}", s.deleteNotifyProvider)
	return r
}

func (s *server) listNotifyProviders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx,
		`SELECT `+notifyCols+` FROM notification_providers WHERE tenant_id=$1 ORDER BY channel, created_at`,
		s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeNotificationProvider
	for rows.Next() {
		var n edgeNotificationProvider
		if err := scanNotify(rows, &n); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, n)
	}
	writeList(w, out)
}

func (s *server) getNotifyProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var n edgeNotificationProvider
	err := scanNotify(s.db.QueryRow(ctx,
		`SELECT `+notifyCols+` FROM notification_providers WHERE id=$1 AND tenant_id=$2`,
		id, s.tenantID), &n)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *server) createNotifyProvider(w http.ResponseWriter, r *http.Request) {
	var in notifyWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	in.Channel = strings.TrimSpace(in.Channel)
	in.Kind = strings.TrimSpace(in.Kind)
	allowed, ok := notifyAllowedKinds[in.Channel]
	if !ok {
		jsonErr(w, http.StatusBadRequest, "bad_request", "channel must be email|sms")
		return
	}
	if !allowed[in.Kind] {
		jsonErr(w, http.StatusBadRequest, "bad_request", "kind not supported for this channel")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var n edgeNotificationProvider
	err := scanNotify(s.db.QueryRow(ctx, `
        INSERT INTO notification_providers(
            tenant_id, channel, kind, enabled, display_name,
            api_key, api_user, from_address, from_name, region
        ) VALUES (
            $1, $2, $3, $4, NULLIF($5,''),
            NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), NULLIF($10,'')
        )
        RETURNING `+notifyCols,
		s.tenantID, in.Channel, in.Kind, enabled, strDeref(in.DisplayName),
		strDeref(in.APIKey), strDeref(in.APIUser),
		strDeref(in.FromAddress), strDeref(in.FromName), strDeref(in.Region),
	), &n)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "another provider is already enabled for this channel")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	s.audit(r, "notification_provider.created", "notification_provider", n.ID, map[string]any{
		"channel": in.Channel, "kind": in.Kind,
	})
	writeJSON(w, http.StatusCreated, n)
}

func (s *server) patchNotifyProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in notifyWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var n edgeNotificationProvider
	err := scanNotify(s.db.QueryRow(ctx, `
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
		id, s.tenantID,
		in.Enabled, in.DisplayName,
		strDeref(in.APIKey), in.APIUser, in.FromAddress, in.FromName, in.Region,
	), &n)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "another provider is already enabled for this channel")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	s.audit(r, "notification_provider.updated", "notification_provider", id, nil)
	writeJSON(w, http.StatusOK, n)
}

func (s *server) deleteNotifyProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx,
		`DELETE FROM notification_providers WHERE id=$1 AND tenant_id=$2`, id, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	s.audit(r, "notification_provider.deleted", "notification_provider", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ----- social OAuth providers -----------------------------------------------------

var socialAllowedProviders = map[string]bool{
	"google": true, "apple": true, "facebook": true, "microsoft": true,
}

type edgeSocialProvider struct {
	ID            string     `json:"id"`
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

const socialCols = `id, provider, enabled, COALESCE(display_name,''),
       client_id, redirect_uri, COALESCE(scopes,''),
       last_success_at, COALESCE(last_error,''), last_error_at,
       created_at, updated_at`

func scanSocial(row interface{ Scan(...any) error }, p *edgeSocialProvider) error {
	return row.Scan(&p.ID, &p.Provider, &p.Enabled, &p.DisplayName,
		&p.ClientID, &p.RedirectURI, &p.Scopes,
		&p.LastSuccessAt, &p.LastError, &p.LastErrorAt,
		&p.CreatedAt, &p.UpdatedAt)
}

type socialWriteReq struct {
	Provider     string  `json:"provider,omitempty"` // create-only
	Enabled      *bool   `json:"enabled,omitempty"`
	DisplayName  *string `json:"display_name,omitempty"`
	ClientID     *string `json:"client_id,omitempty"`
	ClientSecret *string `json:"client_secret,omitempty"` // write-only
	RedirectURI  *string `json:"redirect_uri,omitempty"`
	Scopes       *string `json:"scopes,omitempty"`
}

func (s *server) socialProvidersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listSocialProviders)
	r.Post("/", s.createSocialProvider)
	r.Get("/{id}", s.getSocialProvider)
	r.Patch("/{id}", s.patchSocialProvider)
	r.Delete("/{id}", s.deleteSocialProvider)
	return r
}

func (s *server) listSocialProviders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx,
		`SELECT `+socialCols+` FROM social_oauth_providers WHERE tenant_id=$1 ORDER BY provider, created_at`,
		s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeSocialProvider
	for rows.Next() {
		var p edgeSocialProvider
		if err := scanSocial(rows, &p); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, p)
	}
	writeList(w, out)
}

func (s *server) getSocialProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var p edgeSocialProvider
	err := scanSocial(s.db.QueryRow(ctx,
		`SELECT `+socialCols+` FROM social_oauth_providers WHERE id=$1 AND tenant_id=$2`,
		id, s.tenantID), &p)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) createSocialProvider(w http.ResponseWriter, r *http.Request) {
	var in socialWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	in.Provider = strings.TrimSpace(in.Provider)
	if !socialAllowedProviders[in.Provider] {
		jsonErr(w, http.StatusBadRequest, "bad_request", "provider must be google|apple|facebook|microsoft")
		return
	}
	if strDeref(in.ClientID) == "" || strDeref(in.ClientSecret) == "" || strDeref(in.RedirectURI) == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "client_id, client_secret, redirect_uri required")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var p edgeSocialProvider
	err := scanSocial(s.db.QueryRow(ctx, `
        INSERT INTO social_oauth_providers(
            tenant_id, provider, enabled, display_name,
            client_id, client_secret, redirect_uri, scopes
        ) VALUES (
            $1, $2, $3, NULLIF($4,''),
            $5, $6, $7, NULLIF($8,'')
        )
        RETURNING `+socialCols,
		s.tenantID, in.Provider, enabled, strDeref(in.DisplayName),
		strDeref(in.ClientID), strDeref(in.ClientSecret), strDeref(in.RedirectURI),
		strDeref(in.Scopes),
	), &p)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "another config is already enabled for this provider")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	s.audit(r, "social_oauth_provider.created", "social_oauth_provider", p.ID, map[string]any{
		"provider": in.Provider,
	})
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) patchSocialProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in socialWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var p edgeSocialProvider
	err := scanSocial(s.db.QueryRow(ctx, `
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
		id, s.tenantID,
		in.Enabled, in.DisplayName,
		strDeref(in.ClientID), strDeref(in.ClientSecret), strDeref(in.RedirectURI),
		in.Scopes,
	), &p)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "another config is already enabled for this provider")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	s.audit(r, "social_oauth_provider.updated", "social_oauth_provider", id, nil)
	writeJSON(w, http.StatusOK, p)
}

func (s *server) deleteSocialProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx,
		`DELETE FROM social_oauth_providers WHERE id=$1 AND tenant_id=$2`, id, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	s.audit(r, "social_oauth_provider.deleted", "social_oauth_provider", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ----- Stripe accounts --------------------------------------------------------------

type edgeStripeAccount struct {
	ID             string     `json:"id"`
	Enabled        bool       `json:"enabled"`
	DisplayName    string     `json:"display_name,omitempty"`
	PublishableKey string     `json:"publishable_key"` // Stripe ships this to browsers
	SuccessURL     string     `json:"success_url"`
	CancelURL      string     `json:"cancel_url"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

const stripeCols = `id, enabled, COALESCE(display_name,''),
       publishable_key, success_url, cancel_url,
       last_success_at, COALESCE(last_error,''), last_error_at,
       created_at, updated_at`

func scanStripe(row interface{ Scan(...any) error }, a *edgeStripeAccount) error {
	return row.Scan(&a.ID, &a.Enabled, &a.DisplayName,
		&a.PublishableKey, &a.SuccessURL, &a.CancelURL,
		&a.LastSuccessAt, &a.LastError, &a.LastErrorAt,
		&a.CreatedAt, &a.UpdatedAt)
}

type stripeWriteReq struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	DisplayName    *string `json:"display_name,omitempty"`
	PublishableKey *string `json:"publishable_key,omitempty"`
	SecretKey      *string `json:"secret_key,omitempty"`     // write-only
	WebhookSecret  *string `json:"webhook_secret,omitempty"` // write-only
	SuccessURL     *string `json:"success_url,omitempty"`
	CancelURL      *string `json:"cancel_url,omitempty"`
}

func (s *server) stripeAccountsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listStripeAccounts)
	r.Post("/", s.createStripeAccount)
	r.Get("/{id}", s.getStripeAccount)
	r.Patch("/{id}", s.patchStripeAccount)
	r.Delete("/{id}", s.deleteStripeAccount)
	return r
}

func (s *server) listStripeAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx,
		`SELECT `+stripeCols+` FROM stripe_accounts WHERE tenant_id=$1 ORDER BY created_at`, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeStripeAccount
	for rows.Next() {
		var a edgeStripeAccount
		if err := scanStripe(rows, &a); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, a)
	}
	writeList(w, out)
}

func (s *server) getStripeAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var a edgeStripeAccount
	err := scanStripe(s.db.QueryRow(ctx,
		`SELECT `+stripeCols+` FROM stripe_accounts WHERE id=$1 AND tenant_id=$2`,
		id, s.tenantID), &a)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *server) createStripeAccount(w http.ResponseWriter, r *http.Request) {
	var in stripeWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	pk := strings.TrimSpace(strDeref(in.PublishableKey))
	sk := strings.TrimSpace(strDeref(in.SecretKey))
	ws := strings.TrimSpace(strDeref(in.WebhookSecret))
	su := strings.TrimSpace(strDeref(in.SuccessURL))
	cu := strings.TrimSpace(strDeref(in.CancelURL))
	if pk == "" || sk == "" || ws == "" || su == "" || cu == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request",
			"publishable_key, secret_key, webhook_secret, success_url, cancel_url required")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var a edgeStripeAccount
	err := scanStripe(s.db.QueryRow(ctx, `
        INSERT INTO stripe_accounts(tenant_id, enabled, display_name,
                                    publishable_key, secret_key, webhook_secret,
                                    success_url, cancel_url)
        VALUES ($1, $2, NULLIF($3,''), $4, $5, $6, $7, $8)
        RETURNING `+stripeCols,
		s.tenantID, enabled, strDeref(in.DisplayName), pk, sk, ws, su, cu,
	), &a)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "another stripe account is already enabled")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	s.audit(r, "stripe_account.created", "stripe_account", a.ID, nil)
	writeJSON(w, http.StatusCreated, a)
}

func (s *server) patchStripeAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in stripeWriteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var a edgeStripeAccount
	err := scanStripe(s.db.QueryRow(ctx, `
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
		id, s.tenantID,
		in.Enabled, in.DisplayName,
		strDeref(in.PublishableKey), strDeref(in.SecretKey), strDeref(in.WebhookSecret),
		strDeref(in.SuccessURL), strDeref(in.CancelURL),
	), &a)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "another stripe account is already enabled")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	s.audit(r, "stripe_account.updated", "stripe_account", id, nil)
	writeJSON(w, http.StatusOK, a)
}

func (s *server) deleteStripeAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx,
		`DELETE FROM stripe_accounts WHERE id=$1 AND tenant_id=$2`, id, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	s.audit(r, "stripe_account.deleted", "stripe_account", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

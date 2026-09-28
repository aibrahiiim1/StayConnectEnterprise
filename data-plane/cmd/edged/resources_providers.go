package main

// Provider configuration CRUD: PMS, notification (email/sms) and social OAuth.
// (Card payment accounts are NOT here: they live under payment-providers, with
// sealed credentials held by scd.) Ported from the control-plane admin handlers with the
// fixed site scope. Secrets are write-only everywhere: never returned in
// responses, and a blank/omitted secret on update keeps the stored value.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/social"
	"github.com/stayconnect/enterprise/data-plane/internal/socialloader"
)

// The PMS provider CRUD surface and its connector-kind list were removed here; see
// resources_phase3_interface_authoring.go for the canonical set of kinds the PMS Interface runtime can
// actually run, and for why the legacy list was not simply reused.
//
// `public.pms_providers` and scd's loader remain for rollback and history. They carry no operator surface,
// and internal/pmsloader now refuses to construct or start a legacy FIAS socket owner at all, so the
// retained rows cannot produce a second PMS owner.

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
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Enabled     bool   `json:"enabled"`
	DisplayName string `json:"display_name,omitempty"`
	ClientID    string `json:"client_id"` // public per OAuth2 spec
	RedirectURI string `json:"redirect_uri"`
	Scopes      string `json:"scopes,omitempty"`
	// Non-secret provider settings kept in the row's extra jsonb (the same
	// socialloader.Extra scd builds the provider from): Microsoft's tenant,
	// Apple's Team ID and Key ID. Apple's .p8 private key is the
	// client_secret and, like every secret, is never returned.
	Tenant        string     `json:"tenant,omitempty"`
	TeamID        string     `json:"team_id,omitempty"`
	KeyID         string     `json:"key_id,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastErrorAt   *time.Time `json:"last_error_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

const socialCols = `id, provider, enabled, COALESCE(display_name,''),
       client_id, redirect_uri, COALESCE(scopes,''),
       last_success_at, COALESCE(last_error,''), last_error_at,
       created_at, updated_at, COALESCE(extra,'{}'::jsonb)::text`

func scanSocial(row interface{ Scan(...any) error }, p *edgeSocialProvider) error {
	var extraJSON string
	if err := row.Scan(&p.ID, &p.Provider, &p.Enabled, &p.DisplayName,
		&p.ClientID, &p.RedirectURI, &p.Scopes,
		&p.LastSuccessAt, &p.LastError, &p.LastErrorAt,
		&p.CreatedAt, &p.UpdatedAt, &extraJSON); err != nil {
		return err
	}
	var x socialloader.Extra
	_ = json.Unmarshal([]byte(extraJSON), &x) // unreadable extra shows as unset; scd refuses to build from it
	p.Tenant, p.TeamID, p.KeyID = x.Tenant, x.TeamID, x.KeyID
	return nil
}

type socialWriteReq struct {
	Provider     string  `json:"provider,omitempty"` // create-only
	Enabled      *bool   `json:"enabled,omitempty"`
	DisplayName  *string `json:"display_name,omitempty"`
	ClientID     *string `json:"client_id,omitempty"`
	ClientSecret *string `json:"client_secret,omitempty"` // write-only
	RedirectURI  *string `json:"redirect_uri,omitempty"`
	Scopes       *string `json:"scopes,omitempty"`
	Tenant       *string `json:"tenant,omitempty"`  // microsoft only
	TeamID       *string `json:"team_id,omitempty"` // apple only
	KeyID        *string `json:"key_id,omitempty"`  // apple only
}

// extraPatch is the JSON object written to the row's extra column: only the
// keys the request actually carried, trimmed. On create it is the whole
// column; on update it is merged over what is stored (extra || patch), so a
// PATCH that omits team_id keeps the stored one.
func (in *socialWriteReq) extraPatch() string {
	m := map[string]string{}
	for k, v := range map[string]*string{"tenant": in.Tenant, "team_id": in.TeamID, "key_id": in.KeyID} {
		if v != nil {
			m[k] = strings.TrimSpace(*v)
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// validateSocialWrite checks the provider-specific fields of a create
// (creating=true) or an update and returns an operator-readable reason, or ""
// when the request is acceptable. It is the save-time half of what scd's
// loader enforces at start-up: a configuration that could never sign anyone
// in is refused here, where the operator sees why, rather than logged on the
// appliance at the next guest's attempt. The secret itself never appears in
// the message.
func validateSocialWrite(provider string, in *socialWriteReq, creating bool) string {
	trim := func(p *string) (string, bool) {
		if p == nil {
			return "", false
		}
		return strings.TrimSpace(*p), true
	}
	tenant, hasTenant := trim(in.Tenant)
	teamID, hasTeam := trim(in.TeamID)
	keyID, hasKey := trim(in.KeyID)
	if provider != "microsoft" && hasTenant && tenant != "" {
		return "tenant applies to Microsoft only"
	}
	if provider != "apple" && ((hasTeam && teamID != "") || (hasKey && keyID != "")) {
		return "team_id and key_id apply to Apple only"
	}
	switch provider {
	case "microsoft":
		if hasTenant && !social.ValidMicrosoftTenant(tenant) {
			return "tenant must be common, organizations, consumers, a directory (tenant) ID or a verified domain"
		}
	case "apple":
		if creating && (!hasTeam || !hasKey) {
			return "Sign in with Apple needs team_id (the Apple Team ID) and key_id (the Key ID of the .p8 key)"
		}
		if hasTeam && !social.ValidAppleID(teamID) {
			return "team_id must be the 10-character Apple Team ID (uppercase letters and digits)"
		}
		if hasKey && !social.ValidAppleID(keyID) {
			return "key_id must be the 10-character Key ID shown next to the key in the Apple developer account"
		}
		if secret := strDeref(in.ClientSecret); secret != "" {
			if _, err := social.ParseP256PrivateKey(secret); err != nil {
				return "client_secret must be the Sign in with Apple private key (.p8): " + err.Error()
			}
		}
	}
	return ""
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
	if msg := validateSocialWrite(in.Provider, &in, true); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
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
            client_id, client_secret, redirect_uri, scopes, extra
        ) VALUES (
            $1, $2, $3, NULLIF($4,''),
            $5, $6, $7, NULLIF($8,''), $9::jsonb
        )
        RETURNING `+socialCols,
		s.tenantID, in.Provider, enabled, strDeref(in.DisplayName),
		strDeref(in.ClientID), strDeref(in.ClientSecret), strDeref(in.RedirectURI),
		strDeref(in.Scopes), in.extraPatch(),
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
	// The provider-specific checks need to know which provider this row is.
	var provider string
	err := s.db.QueryRow(ctx,
		`SELECT provider FROM social_oauth_providers WHERE id=$1 AND tenant_id=$2`,
		id, s.tenantID).Scan(&provider)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if msg := validateSocialWrite(provider, &in, false); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	var p edgeSocialProvider
	err = scanSocial(s.db.QueryRow(ctx, `
        UPDATE social_oauth_providers SET
            enabled       = COALESCE($3, enabled),
            display_name  = COALESCE($4, display_name),
            client_id     = COALESCE(NULLIF($5,''), client_id),
            client_secret = COALESCE(NULLIF($6,''), client_secret),
            redirect_uri  = COALESCE(NULLIF($7,''), redirect_uri),
            scopes        = COALESCE($8, scopes),
            extra         = COALESCE(extra,'{}'::jsonb) || $9::jsonb,
            updated_at    = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+socialCols,
		id, s.tenantID,
		in.Enabled, in.DisplayName,
		strDeref(in.ClientID), strDeref(in.ClientSecret), strDeref(in.RedirectURI),
		in.Scopes, in.extraPatch(),
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

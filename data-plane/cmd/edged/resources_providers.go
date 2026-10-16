package main

// Provider configuration CRUD: PMS, notification (email/sms/whatsapp) and social OAuth.
// (Card payment accounts are NOT here: they live under payment-providers, with
// sealed credentials held by scd.) Ported from the control-plane admin handlers with the
// fixed site scope. Secrets are write-only everywhere: never returned in
// responses, and a blank/omitted secret on update keeps the stored value.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
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

// WhatsApp is its own channel with its own provider kinds; it is never an SMS kind.
//
// "ses" is no longer offered: edged accepted it while scd had no adapter for it, so an operator could save a
// sender that silently fell back to the stub. A row that already carries it is still read and validated.
var notifyAllowedKinds = map[string]map[string]bool{
	"email":    {"stub": true, "sendgrid": true, "smtp": true},
	"sms":      {"stub": true, "twilio": true},
	"whatsapp": {"stub": true, "meta_whatsapp": true, "twilio_whatsapp": true},
}

// whatsappExtraKeys are the non-secret settings a WhatsApp provider keeps in notification_providers.extra.
var whatsappExtraKeys = map[string]bool{"template_name": true, "language": true, "content_sid": true}

// smtpExtraKeys are the non-secret settings of the site's own mail server (contract §7.1). The password is
// the sealed secret, never a column.
var smtpExtraKeys = map[string]bool{"host": true, "port": true, "security": true, "username": true, "timeout_seconds": true}

func extraKeysFor(channel, kind string) map[string]bool {
	switch {
	case channel == "whatsapp":
		return whatsappExtraKeys
	case channel == "email" && kind == "smtp":
		return smtpExtraKeys
	}
	return map[string]bool{}
}

var (
	reMetaPhoneID   = regexp.MustCompile(`^[0-9]{5,32}$`)
	reMetaTemplate  = regexp.MustCompile(`^[a-z0-9_]{1,512}$`)
	reTemplateLang  = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?$`)
	reTwilioSID     = regexp.MustCompile(`^AC[0-9a-fA-F]{32}$`)
	reTwilioContent = regexp.MustCompile(`^HX[0-9a-fA-F]{32}$`)
	reE164          = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
)

type edgeNotificationProvider struct {
	ID            string            `json:"id"`
	Channel       string            `json:"channel"`
	Kind          string            `json:"kind"`
	Enabled       bool              `json:"enabled"`
	DisplayName   string            `json:"display_name,omitempty"`
	APIUser       string            `json:"api_user,omitempty"` // not a secret (Twilio account SID / Meta phone number ID)
	FromAddress   string            `json:"from_address,omitempty"`
	FromName      string            `json:"from_name,omitempty"`
	Region        string            `json:"region,omitempty"`
	Extra         map[string]string `json:"extra,omitempty"` // non-secret provider settings (WhatsApp template)
	LastSuccessAt *time.Time        `json:"last_success_at,omitempty"`
	LastError     string            `json:"last_error,omitempty"`
	LastErrorAt   *time.Time        `json:"last_error_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

const notifyCols = `id, channel, kind, enabled, COALESCE(display_name,''),
       COALESCE(api_user,''), COALESCE(from_address,''), COALESCE(from_name,''),
       COALESCE(region,''), COALESCE(extra,'{}'::jsonb)::text, last_success_at, COALESCE(last_error,''),
       last_error_at, created_at, updated_at`

func scanNotify(row interface{ Scan(...any) error }, n *edgeNotificationProvider) error {
	var extra string
	if err := row.Scan(&n.ID, &n.Channel, &n.Kind, &n.Enabled, &n.DisplayName,
		&n.APIUser, &n.FromAddress, &n.FromName, &n.Region, &extra,
		&n.LastSuccessAt, &n.LastError, &n.LastErrorAt, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return err
	}
	n.Extra = publicExtra(extra)
	return nil
}

// publicExtra returns only the known non-secret settings of the extra column.
func publicExtra(raw string) map[string]string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		if sv, ok := v.(string); ok && (whatsappExtraKeys[k] || smtpExtraKeys[k]) {
			out[k] = sv
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type notifyWriteReq struct {
	Channel     string            `json:"channel,omitempty"` // create-only
	Kind        string            `json:"kind,omitempty"`    // create-only
	Enabled     *bool             `json:"enabled,omitempty"`
	DisplayName *string           `json:"display_name,omitempty"`
	APIKey      *string           `json:"api_key,omitempty"` // write-only
	APIUser     *string           `json:"api_user,omitempty"`
	FromAddress *string           `json:"from_address,omitempty"`
	FromName    *string           `json:"from_name,omitempty"`
	Region      *string           `json:"region,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"` // WhatsApp template settings; an empty value removes a key
}

// onlySwitchesOff reports a patch that turns the sender off and changes nothing else: the one change that is
// never subject to configuration validation.
func (in notifyWriteReq) onlySwitchesOff() bool {
	return in.Enabled != nil && !*in.Enabled && in.DisplayName == nil && in.APIKey == nil &&
		in.APIUser == nil && in.FromAddress == nil && in.FromName == nil && in.Region == nil && len(in.Extra) == 0
}

// notifyProviderShape is what validation sees: the provider as it would be stored.
type notifyProviderShape struct {
	Channel, Kind, APIUser, FromAddress string
	HasAPIKey                           bool
	Extra                               map[string]string
}

// validateNotifyProvider returns a readable message for a provider that cannot work, or "" when it can.
// Only WhatsApp providers are checked field by field; email and SMS keep their historical rules.
func validateNotifyProvider(p notifyProviderShape) string {
	if p.Channel == "email" && p.Kind == "smtp" {
		for k := range p.Extra {
			if !smtpExtraKeys[k] {
				return fmt.Sprintf("unknown SMTP setting %q (allowed: host, port, security, username, timeout_seconds)", k)
			}
		}
		port, _ := strconv.Atoi(strings.TrimSpace(p.Extra["port"]))
		sec := mail.SMTPSecurity(strings.TrimSpace(p.Extra["security"]))
		if sec == "" {
			sec = mail.SMTPStartTLS
		}
		if port == 0 {
			port = mail.DefaultSMTPPort(sec)
		}
		timeout := 0
		if t := strings.TrimSpace(p.Extra["timeout_seconds"]); t != "" {
			n, err := strconv.Atoi(t)
			if err != nil || n < 1 || n > 120 {
				return "timeout_seconds must be a whole number of seconds from 1 to 120"
			}
			timeout = n
		}
		password := ""
		if p.HasAPIKey {
			password = "set"
		}
		cfg := mail.SMTPConfig{Host: strings.TrimSpace(p.Extra["host"]), Port: port, Security: sec,
			Username: strings.TrimSpace(p.Extra["username"]), Password: password, FromAddress: strings.TrimSpace(p.FromAddress),
			Timeout: time.Duration(timeout) * time.Second}
		if msg := mail.ValidateSMTPConfig(cfg); msg != "" {
			return msg
		}
		return ""
	}
	if p.Channel != "whatsapp" {
		if len(p.Extra) > 0 {
			return "extra settings apply only to WhatsApp and SMTP providers"
		}
		return ""
	}
	for k := range p.Extra {
		if !whatsappExtraKeys[k] {
			return fmt.Sprintf("unknown WhatsApp setting %q (allowed: template_name, language, content_sid)", k)
		}
	}
	lang := strings.TrimSpace(p.Extra["language"])
	switch p.Kind {
	case "stub":
		return ""
	case "meta_whatsapp":
		switch {
		case !reMetaPhoneID.MatchString(strings.TrimSpace(p.APIUser)):
			return "Meta WhatsApp needs the phone number ID (digits) in api_user"
		case !p.HasAPIKey:
			return "Meta WhatsApp needs the access token in api_key"
		case !reMetaTemplate.MatchString(strings.TrimSpace(p.Extra["template_name"])):
			return "Meta WhatsApp needs extra.template_name: the approved authentication template name (lowercase letters, digits, underscores)"
		case lang != "" && !reTemplateLang.MatchString(lang):
			return "extra.language must be a template language code such as en, ar or en_US"
		}
		return ""
	case "twilio_whatsapp":
		switch {
		case !reTwilioSID.MatchString(strings.TrimSpace(p.APIUser)):
			return "Twilio WhatsApp needs the account SID (AC followed by 32 hex characters) in api_user"
		case !p.HasAPIKey:
			return "Twilio WhatsApp needs the auth token in api_key"
		case !reE164.MatchString(strings.TrimPrefix(strings.TrimSpace(p.FromAddress), "whatsapp:")):
			return "Twilio WhatsApp needs the WhatsApp sender number in from_address, in E.164 form (for example +14155238886)"
		case !reTwilioContent.MatchString(strings.TrimSpace(p.Extra["content_sid"])):
			return "Twilio WhatsApp needs extra.content_sid: the approved authentication template (HX followed by 32 hex characters)"
		}
		return ""
	}
	return "kind not supported for this channel"
}

// mergeExtra applies a patch to stored settings; an empty value removes the key.
func mergeExtra(stored, patch map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range patch {
		if strings.TrimSpace(v) == "" {
			delete(out, k)
		} else {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

func (s *server) notificationProvidersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listNotifyProviders)
	r.Post("/", s.createNotifyProvider)
	r.Get("/{id}", s.getNotifyProvider)
	r.Patch("/{id}", s.patchNotifyProvider)
	r.Delete("/{id}", s.deleteNotifyProvider)
	// "Send a test message": scd builds the sender exactly as the loader does and delivers one message.
	r.Post("/{id}/test", s.testNotifyProvider)
	return r
}

// storeNotifySecret hands a secret to scd, which seals it under the appliance key and clears the plaintext
// column (contract §7.3). edged never holds the key. ok=false with the operator-readable reason on failure.
func (s *server) storeNotifySecret(r *http.Request, id, secret string) (bool, string) {
	status, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/admin/notify/providers/"+id+"/secret",
		map[string]string{"secret": secret})
	if err != nil {
		return false, "the secret could not be stored: the service is unavailable"
	}
	if status != http.StatusOK {
		var e struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Message != "" {
			return false, e.Message
		}
		return false, "the secret could not be stored (" + e.Error + ")"
	}
	return true, ""
}

// notifySecretStored asks scd whether a sealed secret generation exists for the row. scd holds the key and the
// grant; edged holds neither. Unreachable scd reads as "none stored" and the operator sees the ordinary
// "a password is required" refusal rather than a silent success that could not send.
func (s *server) notifySecretStored(r *http.Request, id string) bool {
	status, body, err := s.scd.call(r.Context(), http.MethodGet, "/v1/admin/notify/providers/"+id+"/secret-state", nil)
	if err != nil || status != http.StatusOK {
		return false
	}
	var out struct {
		Sealed bool `json:"sealed"`
	}
	return json.Unmarshal(body, &out) == nil && out.Sealed
}

// testNotifyProvider proxies the test to scd and returns its verdict verbatim.
func (s *server) testNotifyProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		To string `json:"to"`
	}
	if err := decodeJSON(r, &in); err != nil || strings.TrimSpace(in.To) == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "a recipient is required")
		return
	}
	status, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/admin/notify/providers/"+id+"/test", map[string]string{"to": strings.TrimSpace(in.To)})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unavailable", "the service is unavailable")
		return
	}
	s.audit(r, "notification_provider.tested", "notification_provider", id, map[string]any{"status": status})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
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
		jsonErr(w, http.StatusBadRequest, "bad_request", "channel must be email|sms|whatsapp")
		return
	}
	if !allowed[in.Kind] {
		jsonErr(w, http.StatusBadRequest, "bad_request", "kind not supported for this channel")
		return
	}
	extra := mergeExtra(nil, in.Extra)
	if msg := validateNotifyProvider(notifyProviderShape{
		Channel: in.Channel, Kind: in.Kind, APIUser: strDeref(in.APIUser), FromAddress: strDeref(in.FromAddress),
		HasAPIKey: strings.TrimSpace(strDeref(in.APIKey)) != "", Extra: extra,
	}); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	extraJSON, _ := json.Marshal(extra)
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// THE SECRET NEVER TOUCHES THE PLAINTEXT COLUMN. The row is written without it, the secret is handed to
	// scd to seal, and a row whose secret could not be sealed is removed again rather than left half-made.
	var n edgeNotificationProvider
	err := scanNotify(s.db.QueryRow(ctx, `
        INSERT INTO notification_providers(
            tenant_id, channel, kind, enabled, display_name,
            api_key, api_user, from_address, from_name, region, extra
        ) VALUES (
            $1, $2, $3, $4, NULLIF($5,''),
            NULL, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), $10::jsonb
        )
        RETURNING `+notifyCols,
		s.tenantID, in.Channel, in.Kind, enabled, strDeref(in.DisplayName),
		strDeref(in.APIUser),
		strDeref(in.FromAddress), strDeref(in.FromName), strDeref(in.Region), string(extraJSON),
	), &n)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "another provider is already enabled for this channel")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	if secret := strings.TrimSpace(strDeref(in.APIKey)); secret != "" {
		if ok, msg := s.storeNotifySecret(r, n.ID, secret); !ok {
			_, _ = s.db.Exec(ctx, `DELETE FROM notification_providers WHERE id=$1 AND tenant_id=$2`, n.ID, s.tenantID)
			jsonErr(w, http.StatusBadGateway, "secret_not_stored", msg)
			return
		}
	}
	s.audit(r, "notification_provider.created", "notification_provider", n.ID, map[string]any{
		"channel": in.Channel, "kind": in.Kind,
	})
	s.pokeSCD(r, "/v1/admin/notify/reload")
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

	// Validate the provider as it will be stored: the current row with this patch applied.
	var cur notifyProviderShape
	var curExtra string
	err := s.db.QueryRow(ctx, `
        SELECT channel, kind, COALESCE(api_user,''), COALESCE(from_address,''),
               COALESCE(api_key,'') <> '', COALESCE(extra,'{}'::jsonb)::text
          FROM notification_providers WHERE id=$1 AND tenant_id=$2`, id, s.tenantID).
		Scan(&cur.Channel, &cur.Kind, &cur.APIUser, &cur.FromAddress, &cur.HasAPIKey, &curExtra)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if in.APIUser != nil {
		cur.APIUser = *in.APIUser
	}
	if in.FromAddress != nil {
		cur.FromAddress = *in.FromAddress
	}
	// HasAPIKey means "a secret exists somewhere": the plaintext column (rows written before 0107), a sealed
	// generation (rows written since, which scd alone can read -- edged is deliberately not granted that
	// table, and asking it directly failed silently and refused every PATCH on a sealed sender), or the one
	// arriving in this request.
	if strings.TrimSpace(strDeref(in.APIKey)) != "" {
		cur.HasAPIKey = true
	} else if !cur.HasAPIKey {
		cur.HasAPIKey = s.notifySecretStored(r, id)
	}
	// SWITCHING A SENDER OFF IS ALWAYS ALLOWED. Validation exists so a sender that could never send is not
	// switched on; it must never hold a sender on because its settings no longer validate (found live: a
	// working Gmail sender could not be switched off).
	switchingOff := in.onlySwitchesOff()
	var extraArg *string
	if len(in.Extra) > 0 {
		cur.Extra = mergeExtra(publicExtra(curExtra), in.Extra)
		b, _ := json.Marshal(cur.Extra)
		sb := string(b)
		extraArg = &sb
	} else if cur.Channel == "whatsapp" || cur.Kind == "smtp" {
		cur.Extra = publicExtra(curExtra)
	} // stored extras on other rows are left exactly as they are
	if !switchingOff {
		if msg := validateNotifyProvider(cur); msg != "" {
			jsonErr(w, http.StatusBadRequest, "bad_request", msg)
			return
		}
	}

	var n edgeNotificationProvider
	err = scanNotify(s.db.QueryRow(ctx, `
        UPDATE notification_providers SET
            enabled      = COALESCE($3, enabled),
            display_name = COALESCE($4, display_name),
            api_user     = COALESCE($5, api_user),
            from_address = COALESCE($6, from_address),
            from_name    = COALESCE($7, from_name),
            region       = COALESCE($8, region),
            extra        = COALESCE($9::jsonb, extra),
            updated_at   = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+notifyCols,
		id, s.tenantID,
		in.Enabled, in.DisplayName,
		in.APIUser, in.FromAddress, in.FromName, in.Region, extraArg,
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
	if secret := strings.TrimSpace(strDeref(in.APIKey)); secret != "" {
		if ok, msg := s.storeNotifySecret(r, id, secret); !ok {
			jsonErr(w, http.StatusBadGateway, "secret_not_stored", msg)
			return
		}
	}
	s.audit(r, "notification_provider.updated", "notification_provider", id, nil)
	s.pokeSCD(r, "/v1/admin/notify/reload")
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
	s.pokeSCD(r, "/v1/admin/notify/reload")
	w.WriteHeader(http.StatusNoContent)
}

// ----- social OAuth providers -----------------------------------------------------
//
// CUSTOMER-OWNED APPLICATIONS (docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md §5). Each hotel
// registers its own Google, Apple, Microsoft and Facebook application; this surface stores what scd needs to
// use it.
//
//   - THE CALLBACK IS DERIVED, NOT TYPED. It is https://<portal hostname>/auth/social/callback for each portal
//     hostname scd established (the OneGate hostname and, once registered, the hotel portal domain). The hotel
//     ticks the hostnames it registered in its app (callback_hosts); a free-text redirect URI is no longer
//     stored. One sent by an older client is still accepted and turned into its host, as migration 0116 did.
//   - THE SECRET IS WRITE-ONLY AND SEALED BY scd. edged forwards it over scd's socket
//     (PUT /v1/social/providers/{id}/secret) and never writes social_oauth_providers.client_secret: scd seals
//     it under social_dek.key into iam_v2.social_provider_secret_generations, which edged holds no grant on.
//   - SECRET_SET: edged cannot read the sealed generations, and asks scd for nothing it has no endpoint for. It
//     reports secret_set=true for every row it returns, and that is honest by construction: a row is created
//     only together with its secret (the insert is undone when scd refuses the secret), and a secret can be
//     replaced but never removed through this surface.
//   - OFFERED: a provider is offered to clients only while HTTPS is active and the canonical hostname is one of
//     its callback_hosts (§5); the response says which, and why not.
//   - Every change asks scd to reload its providers (POST /v1/social/reload); scd also reloads every 60 s.

var socialAllowedProviders = map[string]bool{
	"google": true, "apple": true, "facebook": true, "microsoft": true,
}

// scd's social endpoints, named once.
const scdSocialReload = "/v1/social/reload"

func scdSocialSecretPath(id string) string { return "/v1/social/providers/" + id + "/secret" }

type edgeSocialProvider struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Enabled     bool   `json:"enabled"`
	DisplayName string `json:"display_name,omitempty"`
	ClientID    string `json:"client_id"` // public per OAuth2 spec
	Scopes      string `json:"scopes,omitempty"`
	// Non-secret provider settings kept in the row's extra jsonb (the same
	// socialloader.Extra scd builds the provider from): Microsoft's tenant,
	// Apple's Team ID and Key ID. Apple's .p8 private key is the
	// client_secret and, like every secret, is never returned.
	Tenant string `json:"tenant,omitempty"`
	TeamID string `json:"team_id,omitempty"`
	KeyID  string `json:"key_id,omitempty"`
	// The portal hostnames whose callback URL the hotel registered in its provider app, and the callback URL
	// for every portal hostname that exists now (registered or not), so the page can show and copy them.
	CallbackHosts []string            `json:"callback_hosts"`
	CallbackURLs  []portalCallbackURL `json:"callback_urls"`
	// See SECRET_SET above: true for every stored row.
	SecretSet bool `json:"secret_set"`
	// Offered is whether the portal shows this provider to clients right now; OfferedReason says why not:
	// disabled | http_fallback | needs_hotel_domain | callback_not_registered.
	Offered       bool       `json:"offered"`
	OfferedReason string     `json:"offered_reason,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LastErrorAt   *time.Time `json:"last_error_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

const socialCols = `id, provider, enabled, COALESCE(display_name,''),
       client_id, COALESCE(scopes,''), callback_hosts,
       last_success_at, COALESCE(last_error,''), last_error_at,
       created_at, updated_at, COALESCE(extra,'{}'::jsonb)::text`

func scanSocial(row interface{ Scan(...any) error }, p *edgeSocialProvider) error {
	var extraJSON string
	if err := row.Scan(&p.ID, &p.Provider, &p.Enabled, &p.DisplayName,
		&p.ClientID, &p.Scopes, &p.CallbackHosts,
		&p.LastSuccessAt, &p.LastError, &p.LastErrorAt,
		&p.CreatedAt, &p.UpdatedAt, &extraJSON); err != nil {
		return err
	}
	if p.CallbackHosts == nil {
		p.CallbackHosts = []string{}
	}
	var x socialloader.Extra
	_ = json.Unmarshal([]byte(extraJSON), &x) // unreadable extra shows as unset; scd refuses to build from it
	p.Tenant, p.TeamID, p.KeyID = x.Tenant, x.TeamID, x.KeyID
	p.SecretSet = true
	return nil
}

// socialMethodSwitches reads the Sign-in methods switch for each identity provider (auth_methods.social).
// ok=false when it cannot be read.
func (s *server) socialMethodSwitches(ctx context.Context) (map[string]bool, bool) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(auth_methods->'social', '{}'::jsonb)::text FROM tenants WHERE id=$1`,
		s.tenantID).Scan(&raw); err != nil {
		return nil, false
	}
	var social map[string]struct {
		Enabled bool `json:"enabled"`
	}
	if json.Unmarshal(raw, &social) != nil {
		return nil, false
	}
	out := map[string]bool{}
	for k, v := range social {
		out[k] = v.Enabled
	}
	return out, true
}

// applySocialSwitch: a provider whose Sign-in methods switch is off is NOT offered, whatever its application
// says -- scd removes it from the portal on exactly that switch (applySocialOffering). The badge used to read
// "Offered" for a provider the portal did not show. An unreadable switch is reported as not offered (fail
// closed), with its own reason.
func applySocialSwitch(p *edgeSocialProvider, switches map[string]bool, ok bool) {
	if !p.Offered {
		return
	}
	switch {
	case !ok:
		p.Offered, p.OfferedReason = false, "method_unknown"
	case !switches[p.Provider]:
		p.Offered, p.OfferedReason = false, "method_off"
	}
}

// decorateSocial adds what depends on the portal's current HTTPS state: the callback URLs and the offered line.
func decorateSocial(p *edgeSocialProvider, st portalTLSState) {
	p.CallbackURLs = portalHostnames(st)
	p.Offered, p.OfferedReason = socialOffered(p.Provider, p.Enabled, p.CallbackHosts, st)
}

// socialOffered is the contract's rule (§5), with the reason an administrator can act on when it is false.
func socialOffered(provider string, enabled bool, callbackHosts []string, st portalTLSState) (bool, string) {
	if !enabled {
		return false, "disabled"
	}
	canonical := strings.ToLower(st.CanonicalHostname)
	if !st.HTTPSActive || canonical == "" {
		return false, "http_fallback"
	}
	// Google accepts only callback domains the app owner verified in Search Console, which a OneGate hostname
	// can never be: Google needs the hotel portal domain to be the canonical hostname.
	if provider == "google" && canonical == strings.ToLower(st.OneGateHostname) {
		return false, "needs_hotel_domain"
	}
	for _, h := range callbackHosts {
		if strings.ToLower(h) == canonical {
			return true, ""
		}
	}
	return false, "callback_not_registered"
}

// validateCallbackHosts normalises the ticked hostnames and refuses any that is not a portal hostname this
// appliance has now. The result is deduplicated and in a stable order.
func validateCallbackHosts(hosts []string, st portalTLSState) ([]string, string) {
	known := map[string]bool{}
	for _, c := range portalHostnames(st) {
		known[c.Hostname] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range hosts {
		h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if h == "" || seen[h] {
			continue
		}
		if !known[h] {
			if len(known) == 0 {
				return nil, "this appliance has no portal hostname yet, so no callback URL can be registered; see Portal HTTPS"
			}
			return nil, h + " is not a portal hostname of this appliance; tick only the callback URLs shown"
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out, ""
}

type socialWriteReq struct {
	Provider      string    `json:"provider,omitempty"` // create-only
	Enabled       *bool     `json:"enabled,omitempty"`
	DisplayName   *string   `json:"display_name,omitempty"`
	ClientID      *string   `json:"client_id,omitempty"`
	ClientSecret  *string   `json:"client_secret,omitempty"` // write-only, forwarded to scd
	CallbackHosts *[]string `json:"callback_hosts,omitempty"`
	// RedirectURI is accepted from older clients only. It is never stored: its host becomes a callback host.
	RedirectURI *string `json:"redirect_uri,omitempty"`
	Scopes      *string `json:"scopes,omitempty"`
	Tenant      *string `json:"tenant,omitempty"`  // microsoft only
	TeamID      *string `json:"team_id,omitempty"` // apple only
	KeyID       *string `json:"key_id,omitempty"`  // apple only
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

// requestedCallbackHosts resolves what the request asks for: callback_hosts when sent, else the host of a
// legacy redirect_uri, else nil (unchanged on update, none on create). The redirect URI is checked for shape
// first, exactly as before, so an older client still gets the same refusal for a URL that could never work.
func (in *socialWriteReq) requestedCallbackHosts() (*[]string, string) {
	if in.CallbackHosts != nil {
		return in.CallbackHosts, ""
	}
	if in.RedirectURI == nil || strings.TrimSpace(*in.RedirectURI) == "" {
		return nil, ""
	}
	raw := strings.TrimSpace(*in.RedirectURI)
	if msg := validateSocialRedirectURI(raw); msg != "" {
		return nil, msg
	}
	u, _ := url.Parse(raw)
	hosts := []string{u.Hostname()}
	return &hosts, ""
}

// validateSocialWrite checks the provider-specific fields of a create
// (creating=true) or an update and returns an operator-readable reason, or ""
// when the request is acceptable. It is the save-time half of what scd's
// loader enforces at start-up: a configuration that could never sign anyone
// in is refused here, where the operator sees why, rather than logged on the
// appliance at the next guest's attempt. The secret itself never appears in
// the message.
func validateSocialWrite(provider string, in *socialWriteReq, creating bool) string {
	if in.RedirectURI != nil && strings.TrimSpace(*in.RedirectURI) != "" {
		if msg := validateSocialRedirectURI(strings.TrimSpace(*in.RedirectURI)); msg != "" {
			return msg
		}
	}
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

// storeSocialSecret hands a secret to scd, which seals it and keeps it out of the plaintext column. ok=false
// with an operator-readable reason; the secret itself never appears in it.
func (s *server) storeSocialSecret(r *http.Request, id, secret string) (bool, string) {
	if s.scd == nil {
		return false, "the secret could not be stored: the service is unavailable"
	}
	status, body, err := s.scd.call(r.Context(), http.MethodPut, scdSocialSecretPath(id),
		map[string]string{"secret": secret})
	if err != nil {
		return false, "the secret could not be stored: the service is unavailable"
	}
	if status >= 300 {
		var e struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Message != "" {
			return false, e.Message
		}
		return false, "the secret could not be stored (" + e.Error + ")"
	}
	return true, ""
}

// reloadSocial asks scd to rebuild its providers now. Best-effort: scd reloads every 60 s regardless.
func (s *server) reloadSocial() {
	if s.scd != nil {
		s.scdReloadWarn(scdSocialReload)
	}
}

func (s *server) listSocialProviders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	st, err := s.loadPortalTLSState(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	switches, switchesOK := s.socialMethodSwitches(ctx)
	rows, err := s.db.Query(ctx,
		`SELECT `+socialCols+` FROM social_oauth_providers WHERE tenant_id=$1 ORDER BY provider, created_at`,
		s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []edgeSocialProvider{}
	for rows.Next() {
		var p edgeSocialProvider
		if err := scanSocial(rows, &p); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		decorateSocial(&p, st)
		applySocialSwitch(&p, switches, switchesOK)
		out = append(out, p)
	}
	// The hostnames are returned beside the list too, so the Add form can offer them before any provider exists.
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "meta": listMeta{},
		"callback_urls": portalHostnames(st), "https_active": st.HTTPSActive,
		"canonical_hostname": st.CanonicalHostname, "onegate_hostname": st.OneGateHostname})
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
	st, err := s.loadPortalTLSState(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	decorateSocial(&p, st)
	sw, swOK := s.socialMethodSwitches(ctx)
	applySocialSwitch(&p, sw, swOK)
	writeJSON(w, http.StatusOK, p)
}

// resolveCallbackHosts turns the request into the hosts to store: nil means "unchanged".
func resolveCallbackHosts(in *socialWriteReq, st portalTLSState) ([]string, string) {
	req, msg := in.requestedCallbackHosts()
	if msg != "" || req == nil {
		return nil, msg
	}
	return validateCallbackHosts(*req, st)
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
	if strings.TrimSpace(strDeref(in.ClientID)) == "" || strDeref(in.ClientSecret) == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "client_id and client_secret are required")
		return
	}
	if msg := validateSocialWrite(in.Provider, &in, true); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	st, err := s.loadPortalTLSState(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal hostnames could not be read")
		return
	}
	hosts, msg := resolveCallbackHosts(&in, st)
	if msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	if hosts == nil {
		hosts = []string{}
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	var p edgeSocialProvider
	err = scanSocial(s.db.QueryRow(ctx, `
        INSERT INTO social_oauth_providers(
            tenant_id, provider, enabled, display_name,
            client_id, client_secret, redirect_uri, callback_hosts, scopes, extra
        ) VALUES (
            $1, $2, $3, NULLIF($4,''),
            $5, NULL, NULL, $6, NULLIF($7,''), $8::jsonb
        )
        RETURNING `+socialCols,
		s.tenantID, in.Provider, enabled, strDeref(in.DisplayName),
		strings.TrimSpace(strDeref(in.ClientID)), hosts,
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
	// The row exists, so scd can seal against its id. A refused secret undoes the row: a provider without a
	// secret could never sign anyone in, and secret_set would no longer be true by construction.
	if ok, msg := s.storeSocialSecret(r, p.ID, strDeref(in.ClientSecret)); !ok {
		if _, derr := s.db.Exec(ctx, `DELETE FROM social_oauth_providers WHERE id=$1 AND tenant_id=$2`, p.ID, s.tenantID); derr != nil {
			slog.Error("social provider: secret refused and the row could not be removed", "id", p.ID, "err", derr)
		}
		jsonErr(w, http.StatusBadGateway, "secret_not_stored", msg)
		return
	}
	s.audit(r, "social_oauth_provider.created", "social_oauth_provider", p.ID, map[string]any{
		"provider": in.Provider, "callback_hosts": hosts,
	})
	s.reloadSocial()
	decorateSocial(&p, st)
	sw, swOK := s.socialMethodSwitches(ctx)
	applySocialSwitch(&p, sw, swOK)
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
	st, err := s.loadPortalTLSState(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal hostnames could not be read")
		return
	}
	hosts, msg := resolveCallbackHosts(&in, st) // nil = unchanged
	if msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	// The secret first: if scd refuses it, nothing else in the request has been applied either.
	if secret := strDeref(in.ClientSecret); secret != "" {
		if ok, msg := s.storeSocialSecret(r, id, secret); !ok {
			jsonErr(w, http.StatusBadGateway, "secret_not_stored", msg)
			return
		}
	}
	var p edgeSocialProvider
	err = scanSocial(s.db.QueryRow(ctx, `
        UPDATE social_oauth_providers SET
            enabled        = COALESCE($3, enabled),
            display_name   = COALESCE($4, display_name),
            client_id      = COALESCE(NULLIF($5,''), client_id),
            callback_hosts = COALESCE($6, callback_hosts),
            scopes         = COALESCE($7, scopes),
            extra          = COALESCE(extra,'{}'::jsonb) || $8::jsonb,
            updated_at     = now()
         WHERE id = $1 AND tenant_id = $2
         RETURNING `+socialCols,
		id, s.tenantID,
		in.Enabled, in.DisplayName,
		strings.TrimSpace(strDeref(in.ClientID)), hosts,
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
	s.audit(r, "social_oauth_provider.updated", "social_oauth_provider", id, map[string]any{
		"secret_replaced": strDeref(in.ClientSecret) != "", "callback_hosts": hosts,
	})
	s.reloadSocial()
	decorateSocial(&p, st)
	sw, swOK := s.socialMethodSwitches(ctx)
	applySocialSwitch(&p, sw, swOK)
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
	s.reloadSocial()
	w.WriteHeader(http.StatusNoContent)
}

// socialCallbackPath is the Client Portal's social callback. The redirect URI registered with a provider must
// point at it, over HTTPS, on a name the guest's browser reaches the portal by.
const socialCallbackPath = "/auth/social/callback"

// validateSocialRedirectURI refuses a redirect URI that could never bring a guest back to the portal: it must
// be an absolute https URL whose path is the portal's callback, with no query or fragment (providers compare it
// exactly, and Apple and Microsoft refuse plain http).
func validateSocialRedirectURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "redirect_uri must be an absolute URL such as https://portal.example.com" + socialCallbackPath
	}
	if u.Scheme != "https" {
		return "redirect_uri must use https"
	}
	if u.Path != socialCallbackPath {
		return "redirect_uri must end with " + socialCallbackPath + " (the Client Portal's callback)"
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "redirect_uri must not carry a query, fragment or credentials"
	}
	return ""
}

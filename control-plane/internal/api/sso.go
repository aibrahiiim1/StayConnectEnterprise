package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/oidc"
)

// SSOBase wires the OIDC registry + session store the SSO handlers need
// (those aren't on api.Base because they're auth-specific).
type SSOBase struct {
	*Base
	Registry     *oidc.Registry
	Sessions     *auth.SessionStore
	Redis        *redis.Client // unused in 4.4 but reserved for nonce-store later
	CookieSecure bool
	StateTTL     time.Duration // default 10m
}

// SSORoutes mounts under /v1/auth/sso (no auth required for these routes).
func (s *SSOBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/providers", s.providers)
	r.Get("/start", s.start)
	r.Get("/callback", s.callback)
	return r
}

// ---- GET /v1/auth/sso/providers?tenant=slug --------------------------------

type ssoProvider struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
}

func (s *SSOBase) providers(w http.ResponseWriter, r *http.Request) {
	tenantSlug := strings.TrimSpace(r.URL.Query().Get("tenant"))
	if tenantSlug == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant slug required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	rows, err := s.DB.Query(ctx, `
        SELECT p.name, p.display_name, p.kind
          FROM idp_providers p
          JOIN tenants t ON t.id = p.tenant_id
         WHERE t.slug = $1 AND p.enabled = true
         ORDER BY p.display_name
    `, tenantSlug)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()

	var out []ssoProvider
	for rows.Next() {
		var p ssoProvider
		if err := rows.Scan(&p.Name, &p.DisplayName, &p.Kind); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		out = append(out, p)
	}
	WriteList(w, out, ListMeta{})
}

// ---- GET /v1/auth/sso/start?tenant=slug&provider=name&return_to=path ------
// Browser hits this from the login page. We mint state+nonce, store the row,
// and 302 to the IdP's authorize URL.

func (s *SSOBase) start(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tenantSlug := strings.TrimSpace(q.Get("tenant"))
	providerName := strings.TrimSpace(q.Get("provider"))
	returnTo := strings.TrimSpace(q.Get("return_to"))
	if tenantSlug == "" || providerName == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "tenant and provider required")
		return
	}
	if returnTo == "" || !strings.HasPrefix(returnTo, "/") {
		returnTo = "/dashboard"
	}

	ctx, cancel := DBCtx(r)
	defer cancel()

	// Resolve tenant + provider config.
	var (
		tenantID, providerID string
		kind                 string
		enabled              bool
	)
	err := s.DB.QueryRow(ctx, `
        SELECT t.id::text, p.id::text, p.kind, p.enabled
          FROM idp_providers p JOIN tenants t ON t.id = p.tenant_id
         WHERE t.slug = $1 AND p.name = $2
    `, tenantSlug, providerName).Scan(&tenantID, &providerID, &kind, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "provider not found for this tenant")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	}
	if !enabled {
		Fail(w, r, http.StatusForbidden, CodeForbidden, "provider disabled")
		return
	}

	prov, err := s.Registry.Get(providerName)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "provider not registered in this build")
		return
	}

	state, err := randHex(32)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "state gen")
		return
	}
	nonce, err := randHex(16)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "nonce gen")
		return
	}

	// Build the redirect URI back to the same origin the browser is using.
	// Behind the Next.js proxy, r.Host is the upstream (127.0.0.1:8080) so we
	// honour X-Forwarded-Host / -Proto first when present. Cookies set on the
	// callback's response thus land on the UI's origin.
	scheme := "http"
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = strings.SplitN(v, ",", 2)[0]
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = strings.SplitN(v, ",", 2)[0]
	}
	redirectURI := fmt.Sprintf("%s://%s/api/v1/auth/sso/callback", scheme, host)

	ttl := s.StateTTL
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	expires := time.Now().Add(ttl)
	_, err = s.DB.Exec(ctx, `
        INSERT INTO auth_oidc_states
          (state, nonce, tenant_id, provider_id, redirect_uri, return_to, client_ip, user_agent, expires_at)
        VALUES
          ($1, $2, $3::uuid, $4::uuid, $5, $6,
           CASE WHEN $7 = '' THEN NULL ELSE $7::inet END,
           NULLIF($8,''), $9)
    `, state, nonce, tenantID, providerID, redirectURI, returnTo, clientIP(r), r.UserAgent(), expires)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "state insert")
		return
	}

	http.Redirect(w, r, prov.AuthorizeURL(state, nonce, redirectURI), http.StatusFound)
}

// ---- GET /v1/auth/sso/callback?state=...&code=... -------------------------

func (s *SSOBase) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	if state == "" || code == "" {
		s.renderCallbackError(w, r, http.StatusBadRequest, "missing state or code")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Atomic state lookup + consume.
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		s.renderCallbackError(w, r, http.StatusInternalServerError, "tx begin failed")
		return
	}
	defer tx.Rollback(ctx)

	var (
		nonce, redirectURI, returnTo string
		tenantID, providerID         string
		clientIPDB                   *string
		expiresAt                    time.Time
		consumedAt                   *time.Time
	)
	err = tx.QueryRow(ctx, `
        SELECT nonce, tenant_id::text, provider_id::text, redirect_uri,
               COALESCE(return_to, '/dashboard'),
               host(client_ip), expires_at, consumed_at
          FROM auth_oidc_states WHERE state = $1 FOR UPDATE
    `, state).Scan(&nonce, &tenantID, &providerID, &redirectURI, &returnTo,
		&clientIPDB, &expiresAt, &consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		s.renderCallbackError(w, r, http.StatusBadRequest, "unknown state — possible CSRF")
		return
	}
	if err != nil {
		s.renderCallbackError(w, r, http.StatusInternalServerError, "state lookup failed")
		return
	}
	if consumedAt != nil {
		s.renderCallbackError(w, r, http.StatusConflict, "state already consumed")
		return
	}
	if time.Now().After(expiresAt) {
		s.renderCallbackError(w, r, http.StatusGone, "state expired")
		return
	}
	if clientIPDB != nil && *clientIPDB != clientIP(r) {
		s.renderCallbackError(w, r, http.StatusForbidden, "device IP changed mid-flow")
		return
	}

	// Resolve provider config + claims map for downstream linking.
	var (
		providerName  string
		autoProvision bool
		claimsMapRaw  []byte
	)
	if err := tx.QueryRow(ctx, `
        SELECT name, auto_provision, claims_map::text
          FROM idp_providers WHERE id = $1::uuid
    `, providerID).Scan(&providerName, &autoProvision, &claimsMapRaw); err != nil {
		s.renderCallbackError(w, r, http.StatusInternalServerError, "provider lookup failed")
		return
	}

	prov, err := s.Registry.Get(providerName)
	if err != nil {
		s.renderCallbackError(w, r, http.StatusBadRequest, "provider not registered")
		return
	}
	claims, err := prov.Exchange(ctx, code, redirectURI, nonce)
	if err != nil {
		slog.Warn("sso exchange", "err", err, "provider", providerName)
		switch {
		case errors.Is(err, oidc.ErrNonceMismatch):
			s.renderCallbackError(w, r, http.StatusForbidden, "nonce mismatch")
		default:
			s.renderCallbackError(w, r, http.StatusBadRequest, "code exchange failed")
		}
		return
	}
	if !claims.EmailVerified || claims.Email == "" {
		s.renderCallbackError(w, r, http.StatusForbidden, "email not verified by provider")
		return
	}

	// Single-use the state.
	if _, err := tx.Exec(ctx,
		`UPDATE auth_oidc_states SET consumed_at = now() WHERE state = $1`, state); err != nil {
		s.renderCallbackError(w, r, http.StatusInternalServerError, "state consume failed")
		return
	}

	// Resolve / link / provision the operator.
	op, provisioned, err := resolveOrProvision(ctx, tx, tenantID, claims, oidc.ParseClaimsMap(claimsMapRaw), autoProvision)
	if err != nil {
		s.renderCallbackError(w, r, http.StatusForbidden, err.Error())
		return
	}
	if _, err := tx.Exec(ctx,
		`UPDATE operators SET last_sso_login_at = now(), updated_at = now() WHERE id = $1`,
		op.ID); err != nil {
		s.renderCallbackError(w, r, http.StatusInternalServerError, "operator stamp failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.renderCallbackError(w, r, http.StatusInternalServerError, "commit failed")
		return
	}

	// Emit audit (post-commit; failure here is non-fatal).
	if provisioned {
		audit.Op(r.Context(), s.DB, r, "operator.sso_provisioned", "operator", op.ID, map[string]any{
			"_tenant_id": tenantID, "provider": providerName, "email": claims.Email,
		})
	} else {
		audit.Op(r.Context(), s.DB, r, "operator.sso_login", "operator", op.ID, map[string]any{
			"_tenant_id": tenantID, "provider": providerName, "email": claims.Email,
		})
	}

	// Mint a session cookie (same as POST /v1/auth/login does).
	sess, err := s.Sessions.Create(ctx, auth.Session{
		OperatorID:      op.ID,
		Email:           op.Email,
		IsSuperAdmin:    op.IsSuperAdmin,
		DefaultTenantID: op.DefaultTenant,
		Roles:           op.Roles,
		SiteIDs:         op.SiteIDs,
		TenantWide:      op.TenantWide,
	})
	if err != nil {
		s.renderCallbackError(w, r, http.StatusInternalServerError, "session create failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sess.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
	})

	dest := returnTo
	if dest == "" || !strings.HasPrefix(dest, "/") {
		dest = "/dashboard"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// ---- Account linking + auto-provisioning ----------------------------------

// resolveOrProvision finds the operator for these claims, creating one if the
// provider permits auto-provisioning. Returns (op, didProvision, error).
//
// Lookup order (per design):
//  1. operators.tenant_id = X AND oidc_sub = claims.Sub  → returning SSO user
//  2. operators.tenant_id = X AND lower(email) = lower(claims.Email)
//     → account linking: stamp oidc_sub on the existing local row
//  3. auto_provision == true → INSERT new operator + role from claims map
//  4. else → 403
func resolveOrProvision(ctx context.Context, tx pgx.Tx, tenantID string, c *oidc.Claims, cm oidc.ClaimsMap, autoProvision bool) (*auth.Operator, bool, error) {
	// (1) by oidc_sub
	if id, ok, err := findOperatorByOIDC(ctx, tx, tenantID, c.Sub); err != nil {
		return nil, false, err
	} else if ok {
		op, err := loadOperator(ctx, tx, id)
		return op, false, err
	}
	// (2) by email
	if id, hasSub, ok, err := findOperatorByEmail(ctx, tx, tenantID, c.Email); err != nil {
		return nil, false, err
	} else if ok {
		if hasSub {
			// Email matches an SSO user with a different sub — refuse.
			return nil, false, fmt.Errorf("email collides with another federated account")
		}
		// Link: stamp oidc_sub + flip auth_method.
		if _, err := tx.Exec(ctx,
			`UPDATE operators SET oidc_sub = $2, auth_method = 'sso', updated_at = now() WHERE id = $1`,
			id, c.Sub); err != nil {
			return nil, false, err
		}
		op, err := loadOperator(ctx, tx, id)
		return op, false, err
	}
	// (3) auto-provision
	if !autoProvision {
		return nil, false, fmt.Errorf("operator not found and auto-provisioning is disabled")
	}
	roles := cm.ResolveRoles(c.Groups)
	if len(roles) == 0 {
		// Provider returned no mappable groups and no default role — refuse
		// rather than create a privilege-less stub.
		return nil, false, fmt.Errorf("no role mapping for this user (groups=%v)", c.Groups)
	}
	displayName := c.Name
	if displayName == "" {
		displayName = c.Email
	}

	var newID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO operators (tenant_id, email, display_name, password_hash, status, auth_method, oidc_sub)
        VALUES ($1::uuid, $2, $3, NULL, 'active', 'sso', $4)
        RETURNING id
    `, tenantID, strings.ToLower(c.Email), displayName, c.Sub).Scan(&newID); err != nil {
		return nil, false, fmt.Errorf("provision insert: %w", err)
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx, `
            INSERT INTO operator_roles (operator_id, tenant_id, role)
            SELECT $1, $2::uuid, $3
             WHERE NOT EXISTS (
                 SELECT 1 FROM operator_roles
                  WHERE operator_id = $1 AND tenant_id = $2::uuid AND role = $3
             )
        `, newID, tenantID, role); err != nil {
			return nil, false, fmt.Errorf("role insert: %w", err)
		}
	}
	op, err := loadOperator(ctx, tx, newID)
	return op, true, err
}

// ---- internal helpers (operator lookup mirrors auth.Repo but works inside a tx) ----

func findOperatorByOIDC(ctx context.Context, tx pgx.Tx, tenantID, sub string) (string, bool, error) {
	if sub == "" {
		return "", false, nil
	}
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id FROM operators WHERE tenant_id = $1::uuid AND oidc_sub = $2`,
		tenantID, sub).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func findOperatorByEmail(ctx context.Context, tx pgx.Tx, tenantID, email string) (id string, hasSub, ok bool, err error) {
	var subPtr *string
	err = tx.QueryRow(ctx,
		`SELECT id, oidc_sub FROM operators WHERE tenant_id = $1::uuid AND lower(email) = lower($2)`,
		tenantID, email).Scan(&id, &subPtr)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	return id, subPtr != nil && *subPtr != "", true, nil
}

func loadOperator(ctx context.Context, tx pgx.Tx, id string) (*auth.Operator, error) {
	var op auth.Operator
	err := tx.QueryRow(ctx, `
        SELECT id, email, COALESCE(display_name,''), COALESCE(password_hash,''), status
          FROM operators WHERE id = $1
    `, id).Scan(&op.ID, &op.Email, &op.DisplayName, &op.PasswordHash, &op.Status)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
        SELECT role, COALESCE(tenant_id::text,'') FROM operator_roles WHERE operator_id = $1
    `, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var firstTenant string
	for rows.Next() {
		var role, tenID string
		if err := rows.Scan(&role, &tenID); err != nil {
			return nil, err
		}
		op.Roles = append(op.Roles, role)
		if role == "platform_admin" {
			op.IsSuperAdmin = true
		}
		if tenID != "" && firstTenant == "" {
			firstTenant = tenID
		}
	}
	op.DefaultTenant = firstTenant
	return &op, nil
}

// ---- Stub authorize page (browser-reachable consent stand-in) -------------

// StubAuthorizeRoutes mounts at /api/oauth/stub/authorize-sso and confirm.
// Only present when the Stub provider is registered.
func (s *SSOBase) StubAuthorizeRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.stubAuthorize)
	r.Post("/confirm", s.stubAuthorizeConfirm)
	return r
}

const stubConsentHTML = `<!doctype html><html><head><meta charset="utf-8">
<title>Stub IdP — sign in</title>
<style>body{font-family:system-ui;max-width:440px;margin:8vh auto;padding:24px;color:#222}
input,button{font-size:1rem;padding:10px 12px;width:100%%;box-sizing:border-box;margin-top:8px;border:1px solid #ccc;border-radius:6px}
button{background:#0a6cff;color:#fff;border:0;font-weight:600;cursor:pointer}
.small{font-size:.85rem;color:#777;margin:8px 0}
</style></head>
<body>
<h2>Stub IdP — operator sign-in</h2>
<p class="small">This stand-in lets us validate the SSO flow end-to-end without a real Keycloak/Azure AD.</p>
<form method="POST" action="/api/oauth/stub/authorize-sso/confirm">
  <input type="hidden" name="provider" value="%s">
  <input type="hidden" name="state" value="%s">
  <input type="hidden" name="nonce" value="%s">
  <input type="hidden" name="redirect_uri" value="%s">
  <label>Email</label>
  <input name="email" type="email" required value="alice@example.com">
  <label class="small"><input type="checkbox" name="email_verified" checked> email_verified</label>
  <label>Display name</label>
  <input name="name" value="Alice Example">
  <label>Groups (comma separated, optional)</label>
  <input name="groups" placeholder="sc-admins,sc-billing">
  <button type="submit">Continue</button>
</form>
</body></html>`

func (s *SSOBase) stubAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider, state, nonce, redirectURI := q.Get("provider"), q.Get("state"), q.Get("nonce"), q.Get("redirect_uri")
	if provider == "" || state == "" || nonce == "" || redirectURI == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	// Auto-mode: bypass UI for tests.
	if q.Get("auto") == "1" && q.Get("email") != "" {
		groups := []string{}
		if g := q.Get("groups"); g != "" {
			for _, p := range strings.Split(g, ",") {
				if p = strings.TrimSpace(p); p != "" {
					groups = append(groups, p)
				}
			}
		}
		code := oidc.EncodeStubCode(oidc.Claims{
			Sub:           "stub:" + q.Get("email"),
			Email:         q.Get("email"),
			EmailVerified: q.Get("email_verified") != "false",
			Name:          q.Get("name"),
			Groups:        groups,
		}, nonce)
		http.Redirect(w, r,
			redirectURI+"?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code),
			http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, stubConsentHTML, htmlEscape(provider), htmlEscape(state),
		htmlEscape(nonce), htmlEscape(redirectURI))
}

func (s *SSOBase) stubAuthorizeConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	state := r.FormValue("state")
	nonce := r.FormValue("nonce")
	redirectURI := r.FormValue("redirect_uri")
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	if state == "" || nonce == "" || redirectURI == "" || email == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	groups := []string{}
	if g := r.FormValue("groups"); g != "" {
		for _, p := range strings.Split(g, ",") {
			if p = strings.TrimSpace(p); p != "" {
				groups = append(groups, p)
			}
		}
	}
	code := oidc.EncodeStubCode(oidc.Claims{
		Sub:           "stub:" + email,
		Email:         email,
		EmailVerified: r.FormValue("email_verified") == "on",
		Name:          r.FormValue("name"),
		Groups:        groups,
	}, nonce)
	http.Redirect(w, r,
		redirectURI+"?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code),
		http.StatusFound)
}

// ---- helpers --------------------------------------------------------------

func (s *SSOBase) renderCallbackError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	// Emit JSON if the caller asked for it (XHR), HTML otherwise.
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		Fail(w, r, status, codeForStatus(status), msg)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html><body style="font-family:system-ui;max-width:420px;margin:10vh auto;padding:24px">
<h2>SSO sign-in failed</h2><p>%s</p><p><a href="/login">Back to login</a></p></body></html>`, htmlEscape(msg))
}

func codeForStatus(s int) string {
	switch s {
	case 400:
		return CodeBadRequest
	case 403:
		return CodeForbidden
	case 404:
		return CodeNotFound
	case 409:
		return CodeConflict
	case 410:
		return CodeNotFound
	default:
		return CodeInternal
	}
}

func htmlEscape(s string) string {
	rep := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return rep.Replace(s)
}

func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host := addr[:i]
		host = strings.Trim(host, "[]")
		return host
	}
	return addr
}

func randHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

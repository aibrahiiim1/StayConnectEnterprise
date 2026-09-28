package social

// Real Microsoft identity platform (v2.0) OpenID Connect provider.
//
// Flow:
//   1. AuthorizeURL builds
//      https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize
//      with our client_id, redirect_uri, scope, state, response_mode=query,
//      prompt=select_account.
//   2. Microsoft redirects back to our callback with ?code=...&state=...
//   3. Exchange POSTs the code to .../{tenant}/oauth2/v2.0/token and receives
//      an id_token.
//   4. The id_token is verified here: RS256 signature against the tenant's
//      key set (.../{tenant}/discovery/v2.0/keys), exp/nbf/iat, aud ==
//      client_id, and iss == https://login.microsoftonline.com/{tid}/v2.0
//      where tid is the token's own tenant claim (the multi-tenant
//      endpoints — common, organizations, consumers — issue tokens whose
//      issuer names the user's home tenant, so the issuer cannot be a
//      constant).
//
// Why the id_token rather than a userinfo call (the Google shape):
//   Microsoft's userinfo lives on Graph and returns no verification signal
//   for email at all. The id_token is where the claims that justify trusting
//   an email live (see emailFromClaims), and it is signed, so verifying it is
//   both necessary and sufficient.
//
// Tenant: configured per site in social_oauth_providers.extra->>'tenant'.
//   ""/"common"      personal Microsoft accounts and any work/school tenant
//   "consumers"      personal Microsoft accounts only
//   "organizations"  any work/school tenant
//   <GUID>/<domain>  one specific work/school tenant; when it is a GUID the
//                    token's tid must equal it.
//
// EMAIL VERIFICATION — the part that differs most from Google.
//   Entra ID (work/school) does not verify the `email` claim: a tenant admin
//   can put any address on a user. Accepting it would let one tenant's admin
//   mint a guest identity for someone else's address. So an email is treated
//   as verified only when the claim set justifies it:
//     - personal Microsoft accounts (tid 9188040d-…-36a304b66dad): the account
//       email is verified by Microsoft at sign-up, so `email` — or, if absent,
//       `preferred_username` when it is shaped like an email — is accepted;
//     - `xms_edov` = true (optional claim "email domain owner verified"):
//       the tenant has proven ownership of the email's domain, so `email` is
//       accepted;
//     - `verified_primary_email` (optional claim, sourced from the user's
//       verified proxy addresses): its first entry is accepted.
//   Anything else returns ErrEmailUnverified with the UserInfo populated,
//   exactly as google.go does for email_verified=false. For work/school
//   sign-in to succeed, the app registration must therefore emit xms_edov or
//   verified_primary_email (Token configuration → Add optional claim).
//
// Endpoints are overridable through the unexported loginBase field so the
// httptest fakes in microsoft_test.go can drive the implementation without
// reaching Microsoft.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultMicrosoftLoginBase = "https://login.microsoftonline.com"
	defaultMicrosoftTenant    = "common"
	defaultMicrosoftScope     = "openid email profile"

	// msaConsumerTenant is the fixed tenant id every personal Microsoft
	// account token carries in its tid claim.
	msaConsumerTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"
)

var guidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// microsoftTenantRe is what may appear in the {tenant} path segment: the
// three well-known aliases, a GUID or a verified domain name.
var microsoftTenantRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,99}$`)

// ValidMicrosoftTenant reports whether t is usable as the {tenant} segment.
// Empty means "common". Exported for the edged configuration API.
func ValidMicrosoftTenant(t string) bool {
	return t == "" || microsoftTenantRe.MatchString(t)
}

type Microsoft struct {
	ClientID     string
	ClientSecret string
	Scopes       string // space-separated; empty = defaultMicrosoftScope
	Tenant       string // empty = "common"

	HTTPClient *http.Client

	// Endpoint override (tests). Every endpoint and the expected issuer are
	// derived from it.
	loginBase string
	now       func() time.Time

	jwksOnce sync.Once
	jwks     *jwksCache
}

// NewMicrosoft requires a client_id + secret. Scopes and tenant are optional.
func NewMicrosoft(clientID, clientSecret, scopes, tenant string) (*Microsoft, error) {
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("microsoft: client_id and client_secret are required")
	}
	tenant = strings.TrimSpace(tenant)
	if !ValidMicrosoftTenant(tenant) {
		return nil, errors.New("microsoft: tenant must be common, organizations, consumers, a tenant id or a domain")
	}
	if tenant == "" {
		tenant = defaultMicrosoftTenant
	}
	return &Microsoft{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
		Tenant:       tenant,
		HTTPClient:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (m *Microsoft) Name() string { return "microsoft" }

func (m *Microsoft) base() string {
	if m.loginBase != "" {
		return strings.TrimRight(m.loginBase, "/")
	}
	return defaultMicrosoftLoginBase
}

func (m *Microsoft) tenant() string {
	if m.Tenant == "" {
		return defaultMicrosoftTenant
	}
	return m.Tenant
}

func (m *Microsoft) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *Microsoft) keySet() *jwksCache {
	m.jwksOnce.Do(func() {
		m.jwks = newJWKSCache(m.base()+"/"+url.PathEscape(m.tenant())+"/discovery/v2.0/keys",
			func() *http.Client { return m.HTTPClient })
	})
	return m.jwks
}

func (m *Microsoft) AuthorizeURL(state, redirectURI string) string {
	scope := m.Scopes
	if scope == "" {
		scope = defaultMicrosoftScope
	}
	u, err := url.Parse(m.base() + "/" + url.PathEscape(m.tenant()) + "/oauth2/v2.0/authorize")
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", m.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("response_mode", "query") // the portal callback reads code/state from the query
	q.Set("scope", scope)
	q.Set("state", state)
	q.Set("prompt", "select_account") // shared devices: always show the account picker
	u.RawQuery = q.Encode()
	return u.String()
}

func (m *Microsoft) Exchange(ctx context.Context, code, redirectURI string) (*UserInfo, error) {
	if code == "" {
		return nil, ErrBadCode
	}
	scope := m.Scopes
	if scope == "" {
		scope = defaultMicrosoftScope
	}
	form := url.Values{}
	form.Set("client_id", m.ClientID)
	form.Set("client_secret", m.ClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	form.Set("scope", scope)
	tk, err := postTokenForm(ctx, m.HTTPClient, "microsoft",
		m.base()+"/"+url.PathEscape(m.tenant())+"/oauth2/v2.0/token", form)
	if err != nil {
		return nil, err
	}
	if tk.IDToken == "" {
		return nil, ErrBadCode
	}
	c, err := verifyIDToken(ctx, tk.IDToken, m.keySet(), m.ClientID, m.clock())
	if err != nil {
		return nil, fmt.Errorf("microsoft: %w", err)
	}

	tid := c.str("tid")
	if tid == "" {
		return nil, fmt.Errorf("microsoft: %w: no tid", errIDToken)
	}
	if want := m.base() + "/" + tid + "/v2.0"; c.Iss != want {
		return nil, fmt.Errorf("microsoft: %w: unexpected issuer", errIDToken)
	}
	// A single-tenant configuration named by id accepts that tenant only. The
	// authority already enforces this for sign-in, but a token for another
	// tenant must not verify here even if one were somehow presented.
	if t := m.tenant(); guidRe.MatchString(t) && !strings.EqualFold(t, tid) {
		return nil, fmt.Errorf("microsoft: %w: token is for another tenant", errIDToken)
	}
	switch strings.ToLower(m.tenant()) {
	case "consumers":
		if tid != msaConsumerTenant {
			return nil, fmt.Errorf("microsoft: %w: not a personal account", errIDToken)
		}
	case "organizations":
		if tid == msaConsumerTenant {
			return nil, fmt.Errorf("microsoft: %w: personal accounts are not accepted", errIDToken)
		}
	}

	// Stable subject: oid is the user's immutable id across every app in the
	// tenant; scoped by tid it is globally unique. sub (pairwise per app) is
	// the fallback when oid was not issued.
	sub := c.Sub
	if oid := c.str("oid"); oid != "" {
		sub = tid + ":" + oid
	}
	email, verified := emailFromClaims(c, tid)
	info := &UserInfo{Sub: sub, Email: email, EmailVerified: verified, Name: c.str("name")}
	if !verified {
		// Caller maps this to ErrEmailUnverified for a friendlier message at
		// the portal layer, as with Google.
		return info, ErrEmailUnverified
	}
	return info, nil
}

// emailFromClaims picks the email address and decides whether the claim set
// justifies treating it as verified (see the file comment).
func emailFromClaims(c *idClaims, tid string) (string, bool) {
	email := strings.TrimSpace(c.str("email"))
	if vpe, ok := c.raw["verified_primary_email"].([]any); ok && len(vpe) > 0 {
		if s, ok := vpe[0].(string); ok && looksLikeEmail(s) {
			return s, true
		}
	}
	if tid == msaConsumerTenant {
		if looksLikeEmail(email) {
			return email, true
		}
		if pu := strings.TrimSpace(c.str("preferred_username")); looksLikeEmail(pu) {
			return pu, true
		}
		return "", false
	}
	if edov, ok := c.boolish("xms_edov"); ok && edov && looksLikeEmail(email) {
		return email, true
	}
	if email == "" {
		if pu := strings.TrimSpace(c.str("preferred_username")); looksLikeEmail(pu) {
			email = pu
		}
	}
	return email, false
}

package social

// Microsoft identity platform v2.0 OIDC. The identity is the verified id_token (userinfo lives on Graph
// and gives no email verification signal). The issuer is https://login.microsoftonline.com/{tid}/v2.0
// with tid taken from the token, since the multi-tenant endpoints issue tokens naming the user's home tenant.
//
// Tenant comes from social_oauth_providers.extra->>'tenant': ""/"common" (personal or any work/school),
// "consumers" (personal only), "organizations" (work/school only), or a GUID/domain for one tenant (a GUID
// must equal the token's tid).
//
// Email verification differs from Google: Entra ID does not verify `email` (a tenant admin can put any
// address on a user, which would let them mint a guest identity for someone else's address). So an email
// counts as verified only for a personal account (tid 9188040d-...; `email`, or an email-shaped
// `preferred_username`), when `xms_edov` is true, or via the first `verified_primary_email`. Otherwise
// ErrEmailUnverified; work/school sign-in therefore needs the app registration to emit xms_edov or
// verified_primary_email.

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
	// profile stays because a personal account may give its email only as preferred_username (emailFromClaims).
	defaultMicrosoftScope = "openid email profile"

	// msaConsumerTenant is the fixed tid of every personal Microsoft account token.
	msaConsumerTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"
)

var guidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// microsoftTenantRe allows the well-known aliases, a GUID or a domain name in the {tenant} path segment.
var microsoftTenantRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,99}$`)

// ValidMicrosoftTenant: empty means "common". Exported for the edged configuration API.
func ValidMicrosoftTenant(t string) bool {
	return t == "" || microsoftTenantRe.MatchString(t)
}

type Microsoft struct {
	ClientID     string
	ClientSecret string
	Scopes       string // space-separated; empty = defaultMicrosoftScope
	Tenant       string // empty = "common"

	HTTPClient *http.Client

	// Endpoint override (tests); every endpoint and the expected issuer derive from it.
	loginBase string
	now       func() time.Time

	jwksOnce sync.Once
	jwks     *jwksCache
}

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

func (m *Microsoft) Binding() Binding { return Binding{PKCE: true, Nonce: true} }

func (m *Microsoft) AuthorizeURL(a AuthRequest) string {
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
	q.Set("redirect_uri", a.RedirectURI)
	q.Set("response_type", "code")
	q.Set("response_mode", "query") // the portal callback reads code/state from the query
	q.Set("scope", scope)
	q.Set("state", a.State)
	q.Set("prompt", "select_account") // always show the account picker (shared devices)
	if a.Nonce != "" {
		q.Set("nonce", a.Nonce)
	}
	if a.CodeChallenge != "" {
		q.Set("code_challenge", a.CodeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (m *Microsoft) Exchange(ctx context.Context, x ExchangeRequest) (*UserInfo, error) {
	if x.Code == "" {
		return nil, ErrBadCode
	}
	if err := requireBinding(m.Binding(), x); err != nil {
		return nil, fmt.Errorf("microsoft: %w", err)
	}
	scope := m.Scopes
	if scope == "" {
		scope = defaultMicrosoftScope
	}
	form := url.Values{}
	form.Set("client_id", m.ClientID)
	form.Set("client_secret", m.ClientSecret)
	form.Set("code", x.Code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", x.RedirectURI)
	form.Set("scope", scope)
	form.Set("code_verifier", x.CodeVerifier)
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
	// A single-tenant configuration accepts that tenant only, even if a token for another were presented.
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
	if err := checkNonce(c, x.Nonce); err != nil {
		return nil, fmt.Errorf("microsoft: %w", err)
	}

	// oid scoped by tid is a globally unique stable subject; sub (pairwise per app) is the fallback.
	sub := c.Sub
	if oid := c.str("oid"); oid != "" {
		sub = tid + ":" + oid
	}
	email, verified := emailFromClaims(c, tid)
	info := &UserInfo{Sub: sub, Email: email, EmailVerified: verified, Name: c.str("name"),
		// tid and oid come from the verified token and are read by IDP_TENANT Client Group rules.
		Claims: map[string]string{"tid": tid, "oid": c.str("oid")}}
	if !verified {
		return info, ErrEmailUnverified
	}
	return info, nil
}

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

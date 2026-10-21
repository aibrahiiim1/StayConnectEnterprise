package social

// Real Google OAuth 2.0 / OpenID Connect provider.
//
// Flow:
//   1. AuthorizeURL builds https://accounts.google.com/o/oauth2/v2/auth with
//      our client_id, redirect_uri, scope, state, prompt=select_account, the
//      attempt's OIDC nonce and its PKCE S256 code_challenge.
//   2. The browser visits, the user consents, Google redirects back to our
//      callback with ?code=...&state=...
//   3. Exchange POSTs the code, our client credentials and the attempt's
//      code_verifier to https://oauth2.googleapis.com/token and receives an
//      id_token.
//   4. THE IDENTITY IS THE VERIFIED ID TOKEN: RS256 signature against Google's
//      key set (https://www.googleapis.com/oauth2/v3/certs), iss is
//      https://accounts.google.com or accounts.google.com (Google documents
//      both), aud == client_id, exp/nbf/iat, nonce == the attempt's, and
//      email_verified == true. sub, email, name, picture and hd come from it.
//
// Why the id_token and not the userinfo endpoint any more:
//   The id_token is a statement Google SIGNED for this client and this
//   attempt (aud, nonce). A userinfo answer is only as good as the bearer
//   token presented, says nothing about which client or attempt it was issued
//   for, and costs a second round trip from a guest's sign-in. With a JWT
//   verifier already in this package for Microsoft and Apple, there is no
//   remaining reason to prefer it, so the userinfo call is gone entirely.
//
// The key-set fetch runs in scd over the appliance's own WAN, not from the
// guest's browser, so www.googleapis.com is NOT a pre-sign-in garden domain.
//
// Endpoints are overridable via AuthBase / TokenURL / JWKSURL so the httptest
// fakes in google_test.go can drive the impl without reaching Google.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultGoogleAuthBase = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultGoogleTokenURL = "https://oauth2.googleapis.com/token"
	defaultGoogleJWKSURL  = "https://www.googleapis.com/oauth2/v3/certs"
	// openid and email give the verified email and the subject: all a guest sign-in keeps (GDPR plan C9).
	defaultGoogleScope = "openid email"
)

// googleIssuers are the two issuer spellings Google documents for its id_tokens.
var googleIssuers = map[string]bool{"https://accounts.google.com": true, "accounts.google.com": true}

type Google struct {
	ClientID     string
	ClientSecret string
	Scopes       string // space-separated; empty = defaultGoogleScope

	// Endpoint overrides (tests).
	AuthBase string
	TokenURL string
	JWKSURL  string

	HTTPClient *http.Client

	now      func() time.Time
	jwksOnce sync.Once
	jwks     *jwksCache
}

// NewGoogle requires a client_id + secret. Scopes is optional.
func NewGoogle(clientID, clientSecret, scopes string) (*Google, error) {
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("google: client_id and client_secret are required")
	}
	return &Google{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
		HTTPClient:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (g *Google) Name() string { return "google" }

// Binding: Google supports PKCE for confidential web clients and honours the OIDC nonce.
func (g *Google) Binding() Binding { return Binding{PKCE: true, Nonce: true} }

func (g *Google) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func (g *Google) keySet() *jwksCache {
	g.jwksOnce.Do(func() {
		g.jwks = newJWKSCache(orDefault(g.JWKSURL, defaultGoogleJWKSURL), func() *http.Client { return g.HTTPClient })
	})
	return g.jwks
}

func (g *Google) AuthorizeURL(a AuthRequest) string {
	u, err := url.Parse(orDefault(g.AuthBase, defaultGoogleAuthBase))
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", g.ClientID)
	q.Set("redirect_uri", a.RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", orDefault(g.Scopes, defaultGoogleScope))
	q.Set("state", a.State)
	q.Set("access_type", "online")
	q.Set("prompt", "select_account") // always show the chooser; useful for shared devices
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

func (g *Google) Exchange(ctx context.Context, x ExchangeRequest) (*UserInfo, error) {
	if x.Code == "" {
		return nil, ErrBadCode
	}
	if err := requireBinding(g.Binding(), x); err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	form := url.Values{}
	form.Set("client_id", g.ClientID)
	form.Set("client_secret", g.ClientSecret)
	form.Set("code", x.Code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", x.RedirectURI)
	form.Set("code_verifier", x.CodeVerifier)
	tk, err := postTokenForm(ctx, g.HTTPClient, "google", orDefault(g.TokenURL, defaultGoogleTokenURL), form)
	if err != nil {
		return nil, err
	}
	if tk.IDToken == "" {
		return nil, ErrBadCode
	}
	c, err := verifyIDToken(ctx, tk.IDToken, g.keySet(), g.ClientID, g.clock())
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	if !googleIssuers[c.Iss] {
		return nil, fmt.Errorf("google: %w: unexpected issuer", errIDToken)
	}
	if err := checkNonce(c, x.Nonce); err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	email := strings.TrimSpace(c.str("email"))
	verified, _ := c.boolish("email_verified")
	info := &UserInfo{Sub: c.Sub, Email: email, EmailVerified: verified && looksLikeEmail(email),
		Name: c.str("name"), Picture: c.str("picture")}
	// A Google Workspace account carries its organisation's hosted domain; a consumer account carries none.
	// It is what an IDP_HOSTED_DOMAIN Client Group rule reads, and it came out of a token Google signed.
	if hd := c.str("hd"); hd != "" {
		info.Claims = map[string]string{"hd": hd}
	}
	if !info.EmailVerified {
		// Caller maps this to a friendlier "email not verified" refusal at the portal layer.
		return info, ErrEmailUnverified
	}
	return info, nil
}

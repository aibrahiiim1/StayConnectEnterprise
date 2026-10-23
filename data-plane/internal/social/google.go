package social

// Google OIDC. The identity is the verified id_token (RS256 against Google's key set; iss accepted as
// https://accounts.google.com or accounts.google.com; aud, exp/nbf/iat, nonce, email_verified), not the
// userinfo endpoint: the id_token is signed for this client and attempt, userinfo says nothing about either.
//
// The key-set fetch runs in scd over the appliance's WAN, so www.googleapis.com is not a pre-sign-in garden domain.

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
	// openid and email give the verified email and subject: all a guest sign-in keeps.
	defaultGoogleScope = "openid email"
)

// googleIssuers are the two issuer spellings Google documents.
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
	q.Set("prompt", "select_account") // always show the chooser (shared devices)
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
	// hd (Workspace hosted domain) comes from the signed token and is read by IDP_HOSTED_DOMAIN Client Group rules.
	if hd := c.str("hd"); hd != "" {
		info.Claims = map[string]string{"hd": hd}
	}
	if !info.EmailVerified {
		return info, ErrEmailUnverified
	}
	return info, nil
}

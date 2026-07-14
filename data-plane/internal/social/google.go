package social

// Real Google OAuth 2.0 provider.
//
// Flow:
//   1. AuthorizeURL builds https://accounts.google.com/o/oauth2/v2/auth
//      with our client_id, redirect_uri, scope, state, prompt=select_account.
//   2. The browser visits, the user consents, Google redirects back to
//      our callback with ?code=...&state=...
//   3. Exchange POSTs to https://oauth2.googleapis.com/token to swap the
//      code for an access_token (+ id_token, but we don't use it here —
//      see the userinfo call below for why).
//   4. We GET https://openidconnect.googleapis.com/v1/userinfo with the
//      access_token and return the normalised UserInfo.
//
// Why userinfo over decoding the id_token JWT:
//   - Calling userinfo proves the access_token is valid (a defence-in-depth
//     check on top of the code → token exchange).
//   - It avoids us pulling a JWT-verification library just for one signed
//     token; the userinfo response is plain JSON over TLS to a
//     known-good Google endpoint.
//
// Endpoints are overridable via AuthBase / TokenURL / UserInfoURL so the
// httptest fakes in google_test.go can drive the impl without hitting
// Google.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultGoogleAuthBase     = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultGoogleTokenURL     = "https://oauth2.googleapis.com/token"
	defaultGoogleUserInfoURL  = "https://openidconnect.googleapis.com/v1/userinfo"
	defaultGoogleScope        = "openid email profile"
)

type Google struct {
	ClientID     string
	ClientSecret string
	Scopes       string // space-separated; empty = defaultGoogleScope

	// Endpoint overrides (tests).
	AuthBase     string
	TokenURL     string
	UserInfoURL  string

	HTTPClient *http.Client
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

func (g *Google) AuthorizeURL(state, redirectURI string) string {
	base := g.AuthBase
	if base == "" {
		base = defaultGoogleAuthBase
	}
	scope := g.Scopes
	if scope == "" {
		scope = defaultGoogleScope
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", g.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", scope)
	q.Set("state", state)
	q.Set("access_type", "online")
	q.Set("prompt", "select_account") // always show the chooser; useful for shared devices
	u.RawQuery = q.Encode()
	return u.String()
}

func (g *Google) Exchange(ctx context.Context, code, redirectURI string) (*UserInfo, error) {
	tokenURL := g.TokenURL
	if tokenURL == "" {
		tokenURL = defaultGoogleTokenURL
	}
	form := url.Values{}
	form.Set("client_id", g.ClientID)
	form.Set("client_secret", g.ClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google: token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Google's error responses are {error, error_description}; surface
		// the description so operators can debug invalid_grant etc.
		var e googleError
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("google token: %s — %s", e.Error, e.Description)
		}
		return nil, fmt.Errorf("google token: status=%d body=%s", resp.StatusCode, string(b))
	}
	var tk googleTokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tk); err != nil {
		return nil, fmt.Errorf("google token decode: %w", err)
	}
	if tk.AccessToken == "" {
		return nil, ErrBadCode
	}

	uiURL := g.UserInfoURL
	if uiURL == "" {
		uiURL = defaultGoogleUserInfoURL
	}
	uir, err := http.NewRequestWithContext(ctx, http.MethodGet, uiURL, nil)
	if err != nil {
		return nil, err
	}
	uir.Header.Set("Authorization", "Bearer "+tk.AccessToken)
	uir.Header.Set("Accept", "application/json")
	uiResp, err := g.HTTPClient.Do(uir)
	if err != nil {
		return nil, fmt.Errorf("google userinfo: %w", err)
	}
	defer uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(uiResp.Body, 4096))
		return nil, fmt.Errorf("google userinfo: status=%d body=%s", uiResp.StatusCode, string(b))
	}
	var ui googleUserInfo
	if err := json.NewDecoder(uiResp.Body).Decode(&ui); err != nil {
		return nil, fmt.Errorf("google userinfo decode: %w", err)
	}
	if ui.Sub == "" || ui.Email == "" {
		return nil, errors.New("google userinfo: missing sub or email")
	}
	if !ui.EmailVerified {
		// Caller maps this to ErrEmailUnverified for a friendlier message
		// at the portal layer.
		return &UserInfo{
			Sub: ui.Sub, Email: ui.Email, EmailVerified: false,
			Name: ui.Name, Picture: ui.Picture,
		}, ErrEmailUnverified
	}
	return &UserInfo{
		Sub: ui.Sub, Email: ui.Email, EmailVerified: true,
		Name: ui.Name, Picture: ui.Picture,
	}, nil
}

type googleTokenResp struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type googleUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

type googleError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

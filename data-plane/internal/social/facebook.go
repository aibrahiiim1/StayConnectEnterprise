package social

// Real Facebook Login provider.
//
// Flow:
//   1. AuthorizeURL builds https://www.facebook.com/v19.0/dialog/oauth with
//      our app id as client_id, redirect_uri, scope "email public_profile",
//      state and response_type=code.
//   2. Facebook redirects back to our callback with ?code=...&state=...
//   3. Exchange POSTs the code to
//      https://graph.facebook.com/v19.0/oauth/access_token (as a form, so the
//      app secret never appears in a URL that a proxy or access log could
//      keep) and receives a user access token.
//   4. We GET https://graph.facebook.com/v19.0/me?fields=id,name,email,picture
//      with the token as a Bearer credential and
//      appsecret_proof = hex(HMAC-SHA256(key=app secret, msg=access token)).
//      The proof makes Graph refuse the call unless it comes from the holder
//      of the app secret, so a token lifted from elsewhere is useless here.
//
// Facebook Login is plain OAuth 2.0, not OIDC: there is no id_token to verify
// on this flow. The Graph call with the proof is what establishes the
// identity, as Google's userinfo call does in google.go.
//
// EMAIL VERIFICATION — Facebook returns no email_verified field. Graph
//   returns `email` only when the user has a CONFIRMED primary address and
//   granted the email permission; an unconfirmed address, a phone-only
//   account or a declined permission all yield no `email` at all. So a
//   returned email is treated as verified, and a missing one is
//   ErrEmailUnverified (with UserInfo populated, as google.go does), which the
//   portal shows as "email not verified by provider".
//
// Endpoints are overridable through unexported fields so the httptest fakes
// in facebook_test.go can drive the implementation without reaching Facebook.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	defaultFacebookAuthBase = "https://www.facebook.com/v19.0/dialog/oauth"
	defaultFacebookTokenURL = "https://graph.facebook.com/v19.0/oauth/access_token"
	defaultFacebookMeURL    = "https://graph.facebook.com/v19.0/me"
	defaultFacebookScope    = "email public_profile"
)

type Facebook struct {
	ClientID     string // App ID
	ClientSecret string // App Secret
	Scopes       string // comma- or space-separated; empty = defaultFacebookScope

	HTTPClient *http.Client

	// Endpoint overrides (tests).
	authBase string
	tokenURL string
	meURL    string
}

// NewFacebook requires the app id + app secret. Scopes is optional.
func NewFacebook(clientID, clientSecret, scopes string) (*Facebook, error) {
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("facebook: client_id (App ID) and client_secret (App Secret) are required")
	}
	return &Facebook{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
		HTTPClient:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (f *Facebook) Name() string { return "facebook" }

func (f *Facebook) AuthorizeURL(state, redirectURI string) string {
	u, err := url.Parse(orDefault(f.authBase, defaultFacebookAuthBase))
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", f.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", orDefault(f.Scopes, defaultFacebookScope))
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String()
}

// appSecretProof is hex(HMAC-SHA256(app secret, access token)).
func appSecretProof(accessToken, appSecret string) string {
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(accessToken))
	return hex.EncodeToString(mac.Sum(nil))
}

func (f *Facebook) Exchange(ctx context.Context, code, redirectURI string) (*UserInfo, error) {
	if code == "" {
		return nil, ErrBadCode
	}
	form := url.Values{}
	form.Set("client_id", f.ClientID)
	form.Set("client_secret", f.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	tk, err := postTokenForm(ctx, f.HTTPClient, "facebook", orDefault(f.tokenURL, defaultFacebookTokenURL), form)
	if err != nil {
		return nil, err
	}
	if tk.AccessToken == "" {
		return nil, ErrBadCode
	}

	u, err := url.Parse(orDefault(f.meURL, defaultFacebookMeURL))
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("fields", "id,name,email,picture")
	q.Set("appsecret_proof", appSecretProof(tk.AccessToken, f.ClientSecret))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tk.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("facebook me: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error.Type != "" {
			return nil, fmt.Errorf("facebook me: %s — %s", e.Error.Type, e.Error.Message)
		}
		return nil, fmt.Errorf("facebook me: status=%d", resp.StatusCode)
	}
	var me struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Email   string `json:"email"`
		Picture struct {
			Data struct {
				URL string `json:"url"`
			} `json:"data"`
		} `json:"picture"`
	}
	if err := json.Unmarshal(b, &me); err != nil {
		return nil, fmt.Errorf("facebook me decode: %w", err)
	}
	if me.ID == "" {
		return nil, errors.New("facebook me: missing id")
	}
	email := strings.TrimSpace(me.Email)
	info := &UserInfo{
		Sub: me.ID, Email: email, EmailVerified: looksLikeEmail(email),
		Name: me.Name, Picture: me.Picture.Data.URL,
	}
	if !info.EmailVerified {
		return info, ErrEmailUnverified
	}
	return info, nil
}

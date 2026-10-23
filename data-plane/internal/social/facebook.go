package social

// Facebook Login is plain OAuth 2.0, not OIDC: there is no id_token. The token is exchanged by form POST
// (so the app secret never appears in a URL a proxy or log could keep), and the Graph /me call carries
// appsecret_proof = hex(HMAC-SHA256(app secret, access token)), which makes Graph refuse a token lifted
// from elsewhere. That call establishes the identity.
//
// Facebook returns no email_verified. Graph returns `email` only for a confirmed primary address with the
// email permission granted, so a returned email is treated as verified and a missing one is
// ErrEmailUnverified (UserInfo populated, as in google.go).

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
	// email only; public_profile is granted anyway and not asked for.
	defaultFacebookScope = "email"
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

// Binding is none beyond `state`: there is no id_token for a nonce, and appsecret_proof authenticates the Graph call.
func (f *Facebook) Binding() Binding { return Binding{} }

func (f *Facebook) AuthorizeURL(a AuthRequest) string {
	u, err := url.Parse(orDefault(f.authBase, defaultFacebookAuthBase))
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", f.ClientID)
	q.Set("redirect_uri", a.RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", orDefault(f.Scopes, defaultFacebookScope))
	q.Set("state", a.State)
	u.RawQuery = q.Encode()
	return u.String()
}

func appSecretProof(accessToken, appSecret string) string {
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(accessToken))
	return hex.EncodeToString(mac.Sum(nil))
}

func (f *Facebook) Exchange(ctx context.Context, x ExchangeRequest) (*UserInfo, error) {
	if x.Code == "" {
		return nil, ErrBadCode
	}
	form := url.Values{}
	form.Set("client_id", f.ClientID)
	form.Set("client_secret", f.ClientSecret)
	form.Set("code", x.Code)
	form.Set("redirect_uri", x.RedirectURI)
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
	q.Set("fields", "id,email") // name and picture are not kept, so not fetched
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

package social

// Sign in with Apple. No PKCE (Apple does not document it for web); the nonce plus the signed client-secret
// JWT bind the exchange to this client and attempt.
//
// response_mode=form_post is forced: Apple refuses query mode when any scope is requested, and the email
// scope is required because guest sign-in is keyed on email. So the portal callback also accepts POST. The
// scd state row still binds the callback to the starting device, and no cookie is used (a cross-site POST
// would not carry a SameSite=Lax one and captive browsers drop them).
//
// Apple has no static client secret: each token request carries a fresh short-lived ES256 JWT signed with
// the .p8 key (iss = Team ID, sub = Services ID, kid = Key ID). Config: client_id = Services ID,
// client_secret = PEM of the .p8 (sealed by scd), extra team_id and key_id.
//
// Both a real and a private-relay (@privaterelay.appleid.com) address are accepted when email_verified is
// true; Apple sends email_verified as a JSON bool or the string "true", and both are read.

import (
	"context"
	"crypto/ecdsa"
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
	defaultAppleAuthBase = "https://appleid.apple.com/auth/authorize"
	defaultAppleTokenURL = "https://appleid.apple.com/auth/token"
	defaultAppleJWKSURL  = "https://appleid.apple.com/auth/keys"
	appleIssuer          = "https://appleid.apple.com"
	// email only: the name is deliberately not kept.
	defaultAppleScope = "email"

	// appleClientSecretTTL is far below Apple's 6-month ceiling; one exchange needs seconds.
	appleClientSecretTTL = 10 * time.Minute
)

var appleIDRe = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// ValidAppleID reports whether s has the shape of a Team ID or Key ID (used by the edged configuration API).
func ValidAppleID(s string) bool { return appleIDRe.MatchString(s) }

type Apple struct {
	ClientID string // Services ID
	TeamID   string
	KeyID    string
	Scopes   string // space-separated; empty = defaultAppleScope

	HTTPClient *http.Client

	key *ecdsa.PrivateKey

	// Endpoint overrides (tests).
	authBase string
	tokenURL string
	jwksURL  string
	issuer   string
	now      func() time.Time

	jwksOnce sync.Once
	jwks     *jwksCache
}

// NewApple parses the key here so a bad key fails at load, not at a guest's first sign-in.
func NewApple(servicesID, privateKeyPEM, teamID, keyID, scopes string) (*Apple, error) {
	if servicesID == "" || privateKeyPEM == "" {
		return nil, errors.New("apple: client_id (Services ID) and client_secret (.p8 private key) are required")
	}
	if !ValidAppleID(teamID) || !ValidAppleID(keyID) {
		return nil, errors.New("apple: team_id and key_id must each be 10 uppercase letters or digits")
	}
	key, err := ParseP256PrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("apple: %w", err)
	}
	return &Apple{
		ClientID:   servicesID,
		TeamID:     teamID,
		KeyID:      keyID,
		Scopes:     scopes,
		key:        key,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (a *Apple) Name() string { return "apple" }

func (a *Apple) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (a *Apple) keySet() *jwksCache {
	a.jwksOnce.Do(func() {
		a.jwks = newJWKSCache(orDefault(a.jwksURL, defaultAppleJWKSURL),
			func() *http.Client { return a.HTTPClient })
	})
	return a.jwks
}

func (a *Apple) Binding() Binding { return Binding{Nonce: true} }

func (a *Apple) AuthorizeURL(r AuthRequest) string {
	u, err := url.Parse(orDefault(a.authBase, defaultAppleAuthBase))
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", a.ClientID)
	q.Set("redirect_uri", r.RedirectURI)
	q.Set("response_type", "code")
	q.Set("response_mode", "form_post")
	q.Set("scope", orDefault(a.Scopes, defaultAppleScope))
	q.Set("state", r.State)
	if r.Nonce != "" {
		q.Set("nonce", r.Nonce)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (a *Apple) clientSecret() (string, error) {
	now := a.clock()
	return signES256(a.key,
		map[string]any{"alg": "ES256", "kid": a.KeyID, "typ": "JWT"},
		map[string]any{
			"iss": a.TeamID,
			"iat": now.Unix(),
			"exp": now.Add(appleClientSecretTTL).Unix(),
			"aud": appleIssuer,
			"sub": a.ClientID,
		})
}

func (a *Apple) Exchange(ctx context.Context, x ExchangeRequest) (*UserInfo, error) {
	if x.Code == "" {
		return nil, ErrBadCode
	}
	if err := requireBinding(a.Binding(), x); err != nil {
		return nil, fmt.Errorf("apple: %w", err)
	}
	secret, err := a.clientSecret()
	if err != nil {
		return nil, fmt.Errorf("apple: client secret: %w", err)
	}
	form := url.Values{}
	form.Set("client_id", a.ClientID)
	form.Set("client_secret", secret)
	form.Set("code", x.Code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", x.RedirectURI)
	tk, err := postTokenForm(ctx, a.HTTPClient, "apple", orDefault(a.tokenURL, defaultAppleTokenURL), form)
	if err != nil {
		return nil, err
	}
	if tk.IDToken == "" {
		return nil, ErrBadCode
	}
	c, err := verifyIDToken(ctx, tk.IDToken, a.keySet(), a.ClientID, a.clock())
	if err != nil {
		return nil, fmt.Errorf("apple: %w", err)
	}
	if c.Iss != orDefault(a.issuer, appleIssuer) {
		return nil, fmt.Errorf("apple: %w: unexpected issuer", errIDToken)
	}
	if err := checkNonce(c, x.Nonce); err != nil {
		return nil, fmt.Errorf("apple: %w", err)
	}
	email := strings.TrimSpace(c.str("email"))
	verified, _ := c.boolish("email_verified")
	info := &UserInfo{Sub: c.Sub, Email: email, EmailVerified: verified && looksLikeEmail(email)}
	if !info.EmailVerified {
		return info, ErrEmailUnverified
	}
	return info, nil
}

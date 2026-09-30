package social

// Real Sign in with Apple provider.
//
// Flow:
//   1. AuthorizeURL builds https://appleid.apple.com/auth/authorize with our
//      Services ID as client_id, redirect_uri, scope "name email", state and
//      response_mode=form_post.
//   2. Apple POSTs code + state (and, on the very first authorization only, a
//      `user` JSON blob with the name) back to our callback as a form.
//   3. Exchange POSTs the code to https://appleid.apple.com/auth/token and
//      receives an id_token.
//   4. The id_token is verified: RS256 signature against Apple's key set
//      (https://appleid.apple.com/auth/keys), iss == https://appleid.apple.com,
//      aud == our Services ID, exp/iat.
//
// WHY form_post, and why the portal callback accepts POST:
//   Apple refuses response_mode=query whenever any scope is requested, and
//   without the `email` scope the id_token carries no email — which is the
//   identity every guest sign-in is keyed on. So the email scope is required,
//   form_post follows from it, and portald's /auth/social/callback accepts a
//   POST form alongside the GET query every other provider uses. The state
//   row in scd still binds the callback to the device that started it, so a
//   cross-site POST is no weaker than the GET leg.
//
// THE CLIENT SECRET IS A JWT WE SIGN.
//   Apple issues no static client secret. The developer downloads a private
//   key (AuthKey_<KEYID>.p8, EC P-256) and every token request carries an
//   ES256 JWT signed with it: iss = Team ID, sub = Services ID, aud =
//   https://appleid.apple.com, kid = Key ID, exp ≤ 6 months after iat. Config
//   mapping on social_oauth_providers:
//     client_id          the Services ID
//     client_secret      the PEM text of the .p8 key (write-only, as for
//                        every provider)
//     extra->>'team_id'  the 10-character Team ID
//     extra->>'key_id'   the 10-character Key ID
//   The JWT is minted fresh for each exchange with a short lifetime
//   (appleClientSecretTTL): signing is cheap, and a short-lived secret that
//   never leaves the process is strictly better than a long-lived one.
//
// Email: Apple returns either the user's real address or a private relay
// address (…@privaterelay.appleid.com). Both are addresses Apple verifies and
// delivers to, so both are accepted when email_verified is true. Apple encodes
// email_verified as a JSON bool or as the string "true" depending on the
// account; both are read. A missing or unverified email is ErrEmailUnverified.
//
// Endpoints are overridable through unexported fields so the httptest fakes
// in apple_test.go can drive the implementation without reaching Apple.

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
	defaultAppleScope    = "name email"

	// appleClientSecretTTL is how long each minted client-secret JWT is valid.
	// Apple's ceiling is 6 months; one exchange needs seconds.
	appleClientSecretTTL = 10 * time.Minute
)

// appleIDRe matches Apple's Team ID and Key ID format: ten uppercase
// alphanumerics.
var appleIDRe = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// ValidAppleID reports whether s has the shape of an Apple Team ID or Key ID.
// Exported for the edged configuration API.
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

// NewApple requires the Services ID, the PEM .p8 private key, the Team ID and
// the Key ID. The key is parsed here so a bad key fails at load, not at a
// guest's first sign-in.
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

func (a *Apple) AuthorizeURL(state, redirectURI string) string {
	u, err := url.Parse(orDefault(a.authBase, defaultAppleAuthBase))
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", a.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("response_mode", "form_post") // required by Apple once any scope is requested
	q.Set("scope", orDefault(a.Scopes, defaultAppleScope))
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String()
}

// clientSecret mints the ES256 client-secret JWT for one token request.
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

func (a *Apple) Exchange(ctx context.Context, code, redirectURI string) (*UserInfo, error) {
	if code == "" {
		return nil, ErrBadCode
	}
	secret, err := a.clientSecret()
	if err != nil {
		return nil, fmt.Errorf("apple: client secret: %w", err)
	}
	form := url.Values{}
	form.Set("client_id", a.ClientID)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
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
	email := strings.TrimSpace(c.str("email"))
	verified, _ := c.boolish("email_verified")
	info := &UserInfo{Sub: c.Sub, Email: email, EmailVerified: verified && looksLikeEmail(email)}
	if !info.EmailVerified {
		return info, ErrEmailUnverified
	}
	return info, nil
}

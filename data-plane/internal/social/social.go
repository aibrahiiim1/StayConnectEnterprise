// Package social abstracts the OAuth2 / OIDC providers used for guest sign-in.
package social

import (
	"context"
	"errors"
)

// UserInfo is best-effort; a stable Sub plus verified Email is the minimum relied on.
type UserInfo struct {
	Sub           string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
	// Claims are verified organisation claims only (Google "hd", Microsoft "tid" and "oid"), read by Client Group rules.
	Claims map[string]string
}

// PreAuthDomains lists the hosts a guest browser must reach for a provider's consent page before it has
// internet access (the token exchange is server-side). It is the documented minimum; providers change
// their asset hosts, which a site adds on the Admin Console's Allowed sites page.
func PreAuthDomains(provider string) []string {
	switch provider {
	case "google":
		return []string{"accounts.google.com", "accounts.youtube.com", "ssl.gstatic.com", "www.gstatic.com",
			"fonts.gstatic.com", "fonts.googleapis.com", "apis.google.com", "play.google.com", "lh3.googleusercontent.com"}
	case "microsoft":
		return []string{"login.microsoftonline.com", "login.live.com", "aadcdn.msftauth.net", "aadcdn.msauth.net",
			"logincdn.msftauth.net", "login.microsoft.com", "account.live.com"}
	case "apple":
		return []string{"appleid.apple.com", "appleid.cdn-apple.com", "idmsa.apple.com", "gsa.apple.com"}
	case "facebook":
		return []string{"www.facebook.com", "m.facebook.com", "facebook.com", "static.xx.fbcdn.net", "connect.facebook.net"}
	}
	return nil
}

type AuthRequest struct {
	// State is the anti-CSRF value, a single-use scd row bound to the device that started.
	State string
	// RedirectURI is decided from the portal's canonical HTTPS hostname, never the browser's Host header.
	RedirectURI string
	// Nonce applies when Binding().Nonce is true.
	Nonce string
	// CodeChallenge applies when Binding().PKCE is true.
	CodeChallenge string
}

// ExchangeRequest carries the attempt's own redirect URI, PKCE verifier and nonce exactly as scd stored them.
type ExchangeRequest struct {
	Code         string
	RedirectURI  string
	CodeVerifier string
	Nonce        string
}

// Binding names the attempt bindings a provider uses beyond `state`; the provider refuses an exchange without them.
type Binding struct {
	PKCE  bool // an intercepted code is useless without the verifier only scd holds
	Nonce bool // the id_token names the one attempt it was issued for
}

type Provider interface {
	Name() string
	Binding() Binding
	AuthorizeURL(a AuthRequest) string
	Exchange(ctx context.Context, x ExchangeRequest) (*UserInfo, error)
}

var (
	ErrUnknownProvider = errors.New("social: unknown provider")
	ErrEmailUnverified = errors.New("social: email not verified by provider")
	ErrBadCode         = errors.New("social: invalid code")
	// ErrAttemptBinding: a missing PKCE verifier or nonce, or an id_token nonce that is not the attempt's.
	ErrAttemptBinding = errors.New("social: sign-in attempt binding missing or mismatched")
)

type Registry struct {
	providers map[string]Provider
}

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }

func (r *Registry) Register(p Provider) { r.providers[p.Name()] = p }

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}

func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrUnknownProvider
	}
	return p, nil
}

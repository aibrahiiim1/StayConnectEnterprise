// Package social abstracts OAuth2 / OIDC providers used for guest sign-in.
//
// Each provider implements a small two-method interface: hand back the URL
// the browser should be sent to (AuthorizeURL), and exchange the redirect's
// `code` for verified user info (Exchange).
//
// Real implementations: Google (google.go), Microsoft (microsoft.go), Apple
// (apple.go) and Facebook (facebook.go). The Stub provider remains for
// end-to-end validation without a real OAuth client.
package social

import (
	"context"
	"errors"
)

// UserInfo is the normalized output every provider returns. Fields are
// best-effort: stable Sub + verified Email is the minimum we rely on.
type UserInfo struct {
	Sub           string // provider's stable subject id
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
	// Claims are verified organisation claims the provider asserted and nothing else: Google "hd" (Workspace
	// hosted domain), Microsoft "tid" (directory tenant) and "oid". They are what Client Group rules read.
	Claims map[string]string
}

// PreAuthDomains lists the host names a client browser must reach to complete a provider's consent page
// before it has internet access. The token exchange is server-side and needs none of these. Providers pull
// page assets from a changing set of hosts; this is the documented minimum and the Admin Console's Allowed
// sites page is where a site adds what a provider changes.
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

type Provider interface {
	Name() string
	// AuthorizeURL constructs the URL the browser must visit to obtain consent.
	// state is our CSRF nonce; redirectURI is where the provider will send the
	// browser back with code + state.
	AuthorizeURL(state, redirectURI string) string
	// Exchange swaps the callback `code` for verified UserInfo (server-to-server
	// for real providers; pure decode for the stub).
	Exchange(ctx context.Context, code, redirectURI string) (*UserInfo, error)
}

var (
	ErrUnknownProvider = errors.New("social: unknown provider")
	ErrEmailUnverified = errors.New("social: email not verified by provider")
	ErrBadCode         = errors.New("social: invalid code")
)

// Registry resolves a provider by name. Wire what's available in main().
type Registry struct {
	providers map[string]Provider
}

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }

func (r *Registry) Register(p Provider) { r.providers[p.Name()] = p }

func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrUnknownProvider
	}
	return p, nil
}

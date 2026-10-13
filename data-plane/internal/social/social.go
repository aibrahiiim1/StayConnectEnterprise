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

// AuthRequest is what one sign-in attempt sends to the provider's consent page.
type AuthRequest struct {
	// State is the anti-CSRF value; scd keeps it as a single-use row bound to the device that started.
	State string
	// RedirectURI is where the provider sends the browser back with code + state. scd decides it from the
	// portal's canonical HTTPS hostname, never from the Host header a browser sent.
	RedirectURI string
	// Nonce is the OIDC nonce, for a provider whose Binding().Nonce is true; empty otherwise.
	Nonce string
	// CodeChallenge is BASE64URL(SHA256(code_verifier)), for a provider whose Binding().PKCE is true.
	CodeChallenge string
}

// ExchangeRequest is what the callback leg hands to the provider: the code, and the attempt's own redirect
// URI, PKCE verifier and expected nonce, exactly as scd stored them when the attempt started.
type ExchangeRequest struct {
	Code         string
	RedirectURI  string
	CodeVerifier string
	Nonce        string
}

// Binding names the attempt bindings a provider uses beyond `state`. scd generates and stores exactly these
// for each attempt, and the provider refuses an exchange that does not carry them.
type Binding struct {
	PKCE  bool // RFC 7636 S256: an intercepted code is useless without the verifier only scd holds
	Nonce bool // OIDC nonce: the id_token names the one attempt it was issued for
}

type Provider interface {
	Name() string
	Binding() Binding
	// AuthorizeURL constructs the URL the browser must visit to obtain consent.
	AuthorizeURL(a AuthRequest) string
	// Exchange swaps the callback `code` for verified UserInfo (server-to-server for real providers; pure
	// decode for the stub).
	Exchange(ctx context.Context, x ExchangeRequest) (*UserInfo, error)
}

var (
	ErrUnknownProvider = errors.New("social: unknown provider")
	ErrEmailUnverified = errors.New("social: email not verified by provider")
	ErrBadCode         = errors.New("social: invalid code")
	// ErrAttemptBinding: the exchange lacked the PKCE verifier or nonce this provider binds an attempt with,
	// or the id_token's nonce is not the attempt's. Either way the code is not accepted.
	ErrAttemptBinding = errors.New("social: sign-in attempt binding missing or mismatched")
)

// Registry resolves a provider by name. Wire what's available in main().
type Registry struct {
	providers map[string]Provider
}

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }

func (r *Registry) Register(p Provider) { r.providers[p.Name()] = p }

// Names lists the registered provider names, in no particular order.
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

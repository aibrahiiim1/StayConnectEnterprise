// Package social abstracts OAuth2 / OIDC providers used for guest sign-in.
//
// Each provider implements a small two-method interface: hand back the URL
// the browser should be sent to (AuthorizeURL), and exchange the redirect's
// `code` for verified user info (Exchange).
//
// Phase 4.3 ships a Stub provider for end-to-end validation without a real
// OAuth client. Real Google / Apple / Facebook implementations slot in by
// implementing the same interface.
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

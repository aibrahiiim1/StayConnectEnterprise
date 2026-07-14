// Package oidc abstracts OpenID Connect IdPs used for operator (admin) sign-in.
//
// Phase 4.4 ships only a Stub implementation for end-to-end validation of the
// flow + account-linking + auto-provisioning code paths. Real Keycloak / Azure
// AD / Okta / Google Workspace plug in by implementing the same Provider
// interface; no other code in the control plane needs to change.
package oidc

import (
	"context"
	"errors"
)

// Claims is the normalized user info every provider returns. Only Sub +
// Email is required; Groups feeds the role-mapping layer when present.
type Claims struct {
	Sub           string // stable subject id at the IdP
	Email         string
	EmailVerified bool
	Name          string
	Groups        []string       // raw groups/roles claim, used by ClaimsMapper
	Raw           map[string]any // full claims for advanced mapping later
}

type Provider interface {
	Name() string
	// AuthorizeURL builds the URL the browser must visit. nonce is the
	// per-flow value that the IdP echoes in the id_token; we verify it on
	// callback to prevent token-replay attacks.
	AuthorizeURL(state, nonce, redirectURI string) string
	// Exchange swaps the callback `code` for verified Claims. Returns
	// ErrNonceMismatch if the id_token nonce doesn't match expected.
	Exchange(ctx context.Context, code, redirectURI, expectedNonce string) (*Claims, error)
}

var (
	ErrUnknownProvider = errors.New("oidc: unknown provider")
	ErrEmailUnverified = errors.New("oidc: email not verified by provider")
	ErrBadCode         = errors.New("oidc: invalid code")
	ErrNonceMismatch   = errors.New("oidc: nonce mismatch (possible replay)")
)

// Registry resolves a provider by name. Wire what's available in main().
type Registry struct {
	providers map[string]Provider
}

func NewRegistry() *Registry            { return &Registry{providers: map[string]Provider{}} }
func (r *Registry) Register(p Provider) { r.providers[p.Name()] = p }
func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrUnknownProvider
	}
	return p, nil
}

package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
)

// Stub is a fake OIDC provider that points the browser at a local consent
// page hosted by ctrlapi. The "id_token" is just the base64 of a Claims+nonce
// blob so the test can drive the flow end-to-end without a real IdP.
type Stub struct {
	ProviderName  string
	AuthorizeBase string // e.g. "/api/oauth/stub/authorize-sso"
}

// stubCodePayload is what the consent page packs into the `code` query param.
// We embed the nonce so Exchange can verify it (mirroring real OIDC's
// id_token.nonce check).
type stubCodePayload struct {
	Claims Claims `json:"claims"`
	Nonce  string `json:"nonce"`
}

func (s *Stub) Name() string { return s.ProviderName }

func (s *Stub) AuthorizeURL(state, nonce, redirectURI string) string {
	u, err := url.Parse(s.AuthorizeBase)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("provider", s.ProviderName)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("redirect_uri", redirectURI)
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Stub) Exchange(_ context.Context, code, _ /*redirectURI*/, expectedNonce string) (*Claims, error) {
	if code == "" {
		return nil, ErrBadCode
	}
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadCode, err)
	}
	var p stubCodePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadCode, err)
	}
	if p.Nonce != expectedNonce {
		return nil, ErrNonceMismatch
	}
	if p.Claims.Sub == "" {
		p.Claims.Sub = "stub:" + p.Claims.Email
	}
	return &p.Claims, nil
}

// EncodeStubCode is used by the stub consent handler to build the code value
// it round-trips back to the callback.
func EncodeStubCode(claims Claims, nonce string) string {
	b, _ := json.Marshal(stubCodePayload{Claims: claims, Nonce: nonce})
	return base64.RawURLEncoding.EncodeToString(b)
}

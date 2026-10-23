package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
)

// Stub is a fake provider whose code base64-encodes the chosen UserInfo. It mints whatever identity its code
// names, so scd registers it in development builds only; in production a stub standing in for an
// unconfigured provider would let anyone sign in as anyone.
type Stub struct {
	ProviderName  string
	AuthorizeBase string
}

func (s *Stub) Name() string { return s.ProviderName }

func (s *Stub) Binding() Binding { return Binding{} }

func (s *Stub) AuthorizeURL(a AuthRequest) string {
	u, err := url.Parse(s.AuthorizeBase)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("provider", s.ProviderName)
	q.Set("state", a.State)
	q.Set("redirect_uri", a.RedirectURI)
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Stub) Exchange(_ context.Context, x ExchangeRequest) (*UserInfo, error) {
	code := x.Code
	if code == "" {
		return nil, ErrBadCode
	}
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadCode, err)
	}
	var u UserInfo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadCode, err)
	}
	if u.Sub == "" {
		u.Sub = "stub:" + u.Email
	}
	return &u, nil
}

// EncodeStubCode is exported for portald's stub authorize handler.
func EncodeStubCode(info UserInfo) string {
	b, _ := json.Marshal(info)
	return base64.RawURLEncoding.EncodeToString(b)
}

package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
)

// Stub is a fake OAuth provider. It points the browser at a local
// "consent" page hosted by portald which round-trips back to the real
// callback with a code that base64-encodes the chosen UserInfo.
//
// Wire it as Stub{Name: "google", AuthorizeBase: "http://portal.stayconnect.local:8380/api/oauth/stub/authorize"}
// to simulate Google without a real OAuth client.
//
// THE STUB MINTS WHATEVER IDENTITY ITS CODE NAMES, so it is a development tool
// only: scd registers it in a development build (buildprofile) and never in a
// production one, where a stub left standing in for an unconfigured provider
// would let anyone sign in as anyone.
type Stub struct {
	ProviderName  string
	AuthorizeBase string // typically http://portal.stayconnect.local:8380/api/oauth/stub/authorize
}

func (s *Stub) Name() string { return s.ProviderName }

// Binding: none. The stub has no token endpoint to bind a verifier or a nonce to.
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

// Exchange decodes the stub code (base64url JSON of UserInfo). Real providers
// would POST to the token endpoint here.
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
		// Synthesize a stable id from email so re-logins look like the same user.
		u.Sub = "stub:" + u.Email
	}
	return &u, nil
}

// EncodeStubCode builds the code value the stub authorize page sends back.
// Exposed so the stub authorize handler in portald can build it.
func EncodeStubCode(info UserInfo) string {
	b, _ := json.Marshal(info)
	return base64.RawURLEncoding.EncodeToString(b)
}

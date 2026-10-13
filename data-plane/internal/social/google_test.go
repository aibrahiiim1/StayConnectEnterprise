package social

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Google is driven against the shared fake OpenID Provider (oidc_fake_test.go): a JWKS endpoint publishing
// one test RSA key and a token endpoint answering with the id_token the test signed. The fake fails the test
// on any other path, so a userinfo call -- which Google no longer needs -- would be caught.

func newGoogleAt(t *testing.T) (*Google, *fakeIdP) {
	t.Helper()
	f := newFakeIdP(t, "/oauth2/v3/certs", "/token")
	g, err := NewGoogle("client-id.apps.googleusercontent.com", "client-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	g.TokenURL = f.srv.URL + "/token"
	g.JWKSURL = f.srv.URL + "/oauth2/v3/certs"
	g.now = func() time.Time { return fixedNow }
	g.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	return g, f
}

func googleClaims(extra map[string]any) map[string]any {
	c := map[string]any{
		"iss":            "https://accounts.google.com",
		"azp":            "client-id.apps.googleusercontent.com",
		"aud":            "client-id.apps.googleusercontent.com",
		"sub":            "110169484474386276334",
		"email":          "alice@example.com",
		"email_verified": true,
		"name":           "Alice",
		"picture":        "https://lh3.googleusercontent.com/a/x",
		"iat":            fixedNow.Add(-time.Minute).Unix(),
		"exp":            fixedNow.Add(time.Hour).Unix(),
		"nonce":          testNonce,
	}
	for k, v := range extra {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

func TestGoogleAuthorizeURL(t *testing.T) {
	g, _ := NewGoogle("cid", "sec", "")
	u, err := url.Parse(g.AuthorizeURL(AuthRequest{State: "state-abc", RedirectURI: "https://wifi.hotel.example/auth/social/callback",
		Nonce: testNonce, CodeChallenge: PKCEChallengeS256(testVerifier)}))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "accounts.google.com" || u.Path != "/o/oauth2/v2/auth" {
		t.Errorf("unexpected auth host/path: %s%s", u.Host, u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": "cid", "redirect_uri": "https://wifi.hotel.example/auth/social/callback",
		"response_type": "code", "state": "state-abc",
		"scope": "openid email profile", "prompt": "select_account",
		"nonce": testNonce, "code_challenge_method": "S256",
		// RFC 7636 Appendix B: the challenge of testVerifier.
		"code_challenge": "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
	} {
		if q.Get(k) != want {
			t.Errorf("query[%s] = %q, want %q", k, q.Get(k), want)
		}
	}
	if b := g.Binding(); !b.PKCE || !b.Nonce {
		t.Errorf("google binds with PKCE and the nonce, got %+v", b)
	}
}

func TestGoogleIdentityComesFromTheVerifiedIDToken(t *testing.T) {
	for _, iss := range []string{"https://accounts.google.com", "accounts.google.com"} {
		t.Run(iss, func(t *testing.T) {
			g, f := newGoogleAt(t)
			key, _ := rsaKeys(t)
			f.respondIDToken(signRS256(t, key, testKid, googleClaims(map[string]any{"iss": iss, "hd": "hotel.example"})))
			info, err := g.Exchange(context.Background(), xreq("code-xyz", "https://wifi.hotel.example/auth/social/callback"))
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if info.Sub != "110169484474386276334" || info.Email != "alice@example.com" || !info.EmailVerified ||
				info.Name != "Alice" || info.Picture != "https://lh3.googleusercontent.com/a/x" {
				t.Errorf("identity wrong: %+v", info)
			}
			if info.Claims["hd"] != "hotel.example" {
				t.Errorf("hosted domain not taken from the token: %+v", info.Claims)
			}
			form := f.form()
			for k, want := range map[string]string{
				"client_id": "client-id.apps.googleusercontent.com", "client_secret": "client-secret",
				"code": "code-xyz", "grant_type": "authorization_code",
				"redirect_uri": "https://wifi.hotel.example/auth/social/callback", "code_verifier": testVerifier,
			} {
				if form.Get(k) != want {
					t.Errorf("token form[%s] = %q, want %q", k, form.Get(k), want)
				}
			}
		})
	}
}

func TestGoogleConsumerAccountCarriesNoHostedDomain(t *testing.T) {
	g, f := newGoogleAt(t)
	key, _ := rsaKeys(t)
	f.respondIDToken(signRS256(t, key, testKid, googleClaims(nil)))
	info, err := g.Exchange(context.Background(), xreq("c", "https://p/cb"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Claims != nil {
		t.Errorf("a consumer account asserts no organisation: %+v", info.Claims)
	}
}

func TestGoogleUnverifiedEmail(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"email_verified false":  {"email_verified": false},
		"email_verified absent": {"email_verified": nil},
		"no email":              {"email": nil},
	} {
		t.Run(name, func(t *testing.T) {
			g, f := newGoogleAt(t)
			key, _ := rsaKeys(t)
			f.respondIDToken(signRS256(t, key, testKid, googleClaims(extra)))
			info, err := g.Exchange(context.Background(), xreq("c", "https://p/cb"))
			if !errors.Is(err, ErrEmailUnverified) {
				t.Fatalf("expected ErrEmailUnverified, got %v", err)
			}
			if info == nil || info.EmailVerified {
				t.Errorf("expected info populated but EmailVerified=false: %+v", info)
			}
		})
	}
}

func TestGoogleRejectsBadTokens(t *testing.T) {
	key, other := rsaKeys(t)
	for name, tok := range map[string]func() string{
		"wrong audience": func() string {
			return signRS256(t, key, testKid, googleClaims(map[string]any{"aud": "another-client.apps.googleusercontent.com"}))
		},
		"wrong issuer": func() string {
			return signRS256(t, key, testKid, googleClaims(map[string]any{"iss": "https://accounts.google.com.evil.example"}))
		},
		"expired": func() string {
			return signRS256(t, key, testKid, googleClaims(map[string]any{"exp": fixedNow.Add(-10 * time.Minute).Unix()}))
		},
		"issued in the future": func() string {
			return signRS256(t, key, testKid, googleClaims(map[string]any{"iat": fixedNow.Add(10 * time.Minute).Unix()}))
		},
		"bad signature": func() string { return signRS256(t, other, testKid, googleClaims(nil)) },
		"unknown kid":   func() string { return signRS256(t, key, "rotated-away", googleClaims(nil)) },
		// A token Google really signed for this client -- but for another sign-in attempt.
		"nonce of another attempt": func() string {
			return signRS256(t, key, testKid, googleClaims(map[string]any{"nonce": "another-attempts-nonce"}))
		},
		"no nonce": func() string { return signRS256(t, key, testKid, googleClaims(map[string]any{"nonce": nil})) },
	} {
		t.Run(name, func(t *testing.T) {
			g, f := newGoogleAt(t)
			f.respondIDToken(tok())
			info, err := g.Exchange(context.Background(), xreq("c", "https://p/cb"))
			if err == nil || info != nil || errors.Is(err, ErrEmailUnverified) {
				t.Fatalf("expected hard rejection, got info=%+v err=%v", info, err)
			}
		})
	}
}

func TestGoogleRefusesAnExchangeWithoutTheAttemptBinding(t *testing.T) {
	for name, edit := range map[string]func(*ExchangeRequest){
		"no verifier": func(x *ExchangeRequest) { x.CodeVerifier = "" },
		"no nonce":    func(x *ExchangeRequest) { x.Nonce = "" },
	} {
		t.Run(name, func(t *testing.T) {
			g, f := newGoogleAt(t)
			key, _ := rsaKeys(t)
			f.respondIDToken(signRS256(t, key, testKid, googleClaims(nil)))
			x := xreq("c", "https://p/cb")
			edit(&x)
			if _, err := g.Exchange(context.Background(), x); !errors.Is(err, ErrAttemptBinding) {
				t.Fatalf("want ErrAttemptBinding, got %v", err)
			}
			if f.form() != nil {
				t.Fatal("the code reached the token endpoint")
			}
		})
	}
}

func TestGoogleTokenError(t *testing.T) {
	g, f := newGoogleAt(t)
	f.respondError(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Bad authorization code."}`)
	_, err := g.Exchange(context.Background(), xreq("c", "https://p/cb"))
	if err == nil {
		t.Fatal("expected error on token 400")
	}
	if !strings.Contains(err.Error(), "invalid_grant") || !strings.Contains(err.Error(), "Bad authorization code") {
		t.Errorf("err didn't include upstream details: %v", err)
	}
	if strings.Contains(err.Error(), "client-secret") {
		t.Fatal("client secret leaked into the error")
	}
}

func TestGoogleConstructorValidation(t *testing.T) {
	if _, err := NewGoogle("", "secret", ""); err == nil {
		t.Error("missing client_id should fail")
	}
	if _, err := NewGoogle("cid", "", ""); err == nil {
		t.Error("missing client_secret should fail")
	}
}

// The key set is fetched by scd from the appliance's own WAN; the guest's browser never needs it, so it is
// not opened in the walled garden before sign-in.
func TestGoogleKeySetHostIsNotAPreSignInDomain(t *testing.T) {
	for _, d := range PreAuthDomains("google") {
		if d == "www.googleapis.com" || d == "oauth2.googleapis.com" {
			t.Errorf("%s is a server-side endpoint and must not be opened to guests before sign-in", d)
		}
	}
}

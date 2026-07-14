package social

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeGoogle stands in for accounts.google.com + oauth2.googleapis.com +
// openidconnect.googleapis.com. It serves both /token and /userinfo on
// the same host, dispatched by URL path.
func fakeGoogle(t *testing.T, tokenStatus int, tokenBody string,
	userStatus int, userBody string,
	captured *url.Values) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.Method != http.MethodPost {
				t.Errorf("token: wrong method %s", r.Method)
			}
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("token: wrong content-type %q", r.Header.Get("Content-Type"))
			}
			_ = r.ParseForm()
			*captured = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tokenStatus)
			_, _ = w.Write([]byte(tokenBody))
		case "/userinfo":
			if r.Method != http.MethodGet {
				t.Errorf("userinfo: wrong method %s", r.Method)
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("userinfo: missing bearer auth")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(userStatus)
			_, _ = w.Write([]byte(userBody))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}

func newGoogleAt(t *testing.T, base string) *Google {
	g, err := NewGoogle("client-id", "client-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	g.TokenURL = base + "/token"
	g.UserInfoURL = base + "/userinfo"
	g.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	return g
}

func TestGoogleAuthorizeURL(t *testing.T) {
	g, _ := NewGoogle("cid", "sec", "")
	u, err := url.Parse(g.AuthorizeURL("state-abc", "https://portal/cb"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "accounts.google.com" || u.Path != "/o/oauth2/v2/auth" {
		t.Errorf("unexpected auth host/path: %s%s", u.Host, u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": "cid", "redirect_uri": "https://portal/cb",
		"response_type": "code", "state": "state-abc",
		"scope": "openid email profile", "prompt": "select_account",
	} {
		if q.Get(k) != want {
			t.Errorf("query[%s] = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestGoogleExchangeSuccess(t *testing.T) {
	var captured url.Values
	srv := fakeGoogle(t,
		http.StatusOK, `{"access_token":"at-1","id_token":"it-1","expires_in":3600,"token_type":"Bearer"}`,
		http.StatusOK, `{"sub":"u-42","email":"alice@example.com","email_verified":true,"name":"Alice","picture":"https://x/y.png"}`,
		&captured)
	defer srv.Close()

	g := newGoogleAt(t, srv.URL)
	info, err := g.Exchange(context.Background(), "code-xyz", "https://portal/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	// Token POST sent expected fields.
	for k, want := range map[string]string{
		"client_id": "client-id", "client_secret": "client-secret",
		"code": "code-xyz", "grant_type": "authorization_code",
		"redirect_uri": "https://portal/cb",
	} {
		if captured.Get(k) != want {
			t.Errorf("token form[%s] = %q, want %q", k, captured.Get(k), want)
		}
	}
	if info.Sub != "u-42" || info.Email != "alice@example.com" || !info.EmailVerified {
		t.Errorf("userinfo wrong: %+v", info)
	}
	if info.Name != "Alice" || info.Picture != "https://x/y.png" {
		t.Errorf("userinfo missing optional fields: %+v", info)
	}
}

func TestGoogleExchangeUnverifiedEmail(t *testing.T) {
	var captured url.Values
	srv := fakeGoogle(t,
		http.StatusOK, `{"access_token":"at-1","token_type":"Bearer"}`,
		http.StatusOK, `{"sub":"u-77","email":"shady@example.com","email_verified":false}`,
		&captured)
	defer srv.Close()

	g := newGoogleAt(t, srv.URL)
	info, err := g.Exchange(context.Background(), "c", "https://portal/cb")
	if !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("expected ErrEmailUnverified, got %v", err)
	}
	if info == nil || info.EmailVerified {
		t.Errorf("expected info populated but EmailVerified=false")
	}
}

func TestGoogleTokenError(t *testing.T) {
	srv := fakeGoogle(t,
		http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Bad authorization code."}`,
		http.StatusOK, "", new(url.Values))
	defer srv.Close()

	g := newGoogleAt(t, srv.URL)
	_, err := g.Exchange(context.Background(), "c", "https://portal/cb")
	if err == nil {
		t.Fatal("expected error on token 400")
	}
	if !strings.Contains(err.Error(), "invalid_grant") || !strings.Contains(err.Error(), "Bad authorization code") {
		t.Errorf("err didn't include upstream details: %v", err)
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

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

// fakeFacebook stands in for www.facebook.com + graph.facebook.com: the token
// endpoint and Graph /me on one host, dispatched by path.
func fakeFacebook(t *testing.T, tokenStatus int, tokenBody string, meStatus int, meBody string,
	tokenForm *url.Values, meQuery *url.Values) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/access_token":
			if r.Method != http.MethodPost {
				t.Errorf("token: wrong method %s", r.Method)
			}
			_ = r.ParseForm()
			*tokenForm = r.PostForm
			w.WriteHeader(tokenStatus)
			_, _ = w.Write([]byte(tokenBody))
		case "/me":
			if r.Header.Get("Authorization") != "Bearer fb-at-1" {
				t.Errorf("me: wrong auth %q", r.Header.Get("Authorization"))
			}
			*meQuery = r.URL.Query()
			w.WriteHeader(meStatus)
			_, _ = w.Write([]byte(meBody))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newFacebookAt(t *testing.T, base string) *Facebook {
	t.Helper()
	f, err := NewFacebook("fb-app", "fb-app-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	f.tokenURL = base + "/oauth/access_token"
	f.meURL = base + "/me"
	f.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	return f
}

const fbToken = `{"access_token":"fb-at-1","token_type":"bearer","expires_in":5183944}`

func TestFacebookAuthorizeURL(t *testing.T) {
	f, _ := NewFacebook("app", "sec", "")
	u, err := url.Parse(f.AuthorizeURL("state-abc", "https://portal/auth/social/callback"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "www.facebook.com" || u.Path != "/v19.0/dialog/oauth" {
		t.Errorf("unexpected auth host/path: %s%s", u.Host, u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": "app", "redirect_uri": "https://portal/auth/social/callback",
		"response_type": "code", "state": "state-abc", "scope": "email public_profile",
	} {
		if q.Get(k) != want {
			t.Errorf("query[%s] = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestFacebookExchangeSuccess(t *testing.T) {
	var form, meQ url.Values
	srv := fakeFacebook(t, http.StatusOK, fbToken, http.StatusOK,
		`{"id":"10222","name":"Alice","email":"alice@example.com","picture":{"data":{"url":"https://x/p.jpg"}}}`,
		&form, &meQ)
	f := newFacebookAt(t, srv.URL)
	info, err := f.Exchange(context.Background(), "code-1", "https://portal/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if info.Sub != "10222" || info.Email != "alice@example.com" || !info.EmailVerified ||
		info.Name != "Alice" || info.Picture != "https://x/p.jpg" {
		t.Errorf("userinfo wrong: %+v", info)
	}
	for k, want := range map[string]string{
		"client_id": "fb-app", "client_secret": "fb-app-secret", "code": "code-1", "redirect_uri": "https://portal/cb",
	} {
		if form.Get(k) != want {
			t.Errorf("token form[%s] = %q, want %q", k, form.Get(k), want)
		}
	}
	// appsecret_proof = hex(HMAC-SHA256(key=app secret, msg=access token)),
	// computed independently here.
	if got, want := meQ.Get("appsecret_proof"), appSecretProof("fb-at-1", "fb-app-secret"); got != want || len(got) != 64 {
		t.Errorf("appsecret_proof = %q, want %q", got, want)
	}
	if meQ.Get("fields") != "id,name,email,picture" {
		t.Errorf("fields = %q", meQ.Get("fields"))
	}
	if meQ.Get("access_token") != "" {
		t.Error("the access token must travel in the Authorization header, not the URL")
	}
}

func TestFacebookAppSecretProofKnownVector(t *testing.T) {
	// HMAC-SHA256(key="key", msg="The quick brown fox jumps over the lazy dog").
	const want = "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if got := appSecretProof("The quick brown fox jumps over the lazy dog", "key"); got != want {
		t.Errorf("proof = %s, want %s", got, want)
	}
}

func TestFacebookMissingEmailIsUnverified(t *testing.T) {
	var form, meQ url.Values
	srv := fakeFacebook(t, http.StatusOK, fbToken, http.StatusOK, `{"id":"10333","name":"Phone Only"}`, &form, &meQ)
	f := newFacebookAt(t, srv.URL)
	info, err := f.Exchange(context.Background(), "c", "https://portal/cb")
	if !errors.Is(err, ErrEmailUnverified) {
		t.Fatalf("expected ErrEmailUnverified, got %v", err)
	}
	if info == nil || info.EmailVerified || info.Sub != "10333" {
		t.Errorf("expected info populated but EmailVerified=false: %+v", info)
	}
}

func TestFacebookTokenError(t *testing.T) {
	var form, meQ url.Values
	srv := fakeFacebook(t, http.StatusBadRequest,
		`{"error":{"message":"This authorization code has expired.","type":"OAuthException","code":100}}`,
		http.StatusOK, "", &form, &meQ)
	f := newFacebookAt(t, srv.URL)
	_, err := f.Exchange(context.Background(), "c", "https://portal/cb")
	if err == nil || !strings.Contains(err.Error(), "OAuthException") || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err didn't include upstream details: %v", err)
	}
	if strings.Contains(err.Error(), "fb-app-secret") {
		t.Fatal("app secret leaked into the error")
	}
}

func TestFacebookGraphError(t *testing.T) {
	var form, meQ url.Values
	srv := fakeFacebook(t, http.StatusOK, fbToken, http.StatusBadRequest,
		`{"error":{"message":"Invalid appsecret_proof provided in the API argument","type":"GraphMethodException","code":100}}`,
		&form, &meQ)
	f := newFacebookAt(t, srv.URL)
	info, err := f.Exchange(context.Background(), "c", "https://portal/cb")
	if err == nil || info != nil || !strings.Contains(err.Error(), "appsecret_proof") {
		t.Fatalf("expected Graph refusal, got info=%+v err=%v", info, err)
	}
}

func TestFacebookConstructorValidation(t *testing.T) {
	if _, err := NewFacebook("", "s", ""); err == nil {
		t.Error("missing app id should fail")
	}
	if _, err := NewFacebook("a", "", ""); err == nil {
		t.Error("missing app secret should fail")
	}
}

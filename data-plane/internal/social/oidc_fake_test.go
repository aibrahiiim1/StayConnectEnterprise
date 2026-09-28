package social

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Shared fake OpenID Provider for the Microsoft and Apple tests: a JWKS
// endpoint publishing one test RSA key, and a token endpoint that answers with
// whatever id_token (or error) the test configured. Everything is local; no
// test reaches a real IdP.

var (
	testRSAOnce sync.Once
	testRSAKey  *rsa.PrivateKey
	otherRSAKey *rsa.PrivateKey
)

func rsaKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	testRSAOnce.Do(func() {
		var err error
		if testRSAKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if otherRSAKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return testRSAKey, otherRSAKey
}

const testKid = "test-kid-1"

// signRS256 builds a compact JWS the way an IdP would.
func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	cb, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	d := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

type fakeIdP struct {
	srv *httptest.Server

	mu          sync.Mutex
	tokenStatus int
	tokenBody   string
	tokenForm   url.Values
	jwksHits    int
}

// newFakeIdP serves the JWKS at jwksPath and the token endpoint at tokenPath.
func newFakeIdP(t *testing.T, jwksPath, tokenPath string) *fakeIdP {
	t.Helper()
	key, _ := rsaKeys(t)
	f := &fakeIdP{tokenStatus: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case jwksPath:
			f.jwksHits++
			e := big.NewInt(int64(key.PublicKey.E)).Bytes()
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKid,
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(e),
			}}})
		case tokenPath:
			if r.Method != http.MethodPost {
				t.Errorf("token: wrong method %s", r.Method)
			}
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("token: wrong content-type %q", r.Header.Get("Content-Type"))
			}
			_ = r.ParseForm()
			f.tokenForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.tokenStatus)
			_, _ = w.Write([]byte(f.tokenBody))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) respondIDToken(idToken string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
	f.tokenStatus, f.tokenBody = http.StatusOK, string(b)
}

func (f *fakeIdP) respondError(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenStatus, f.tokenBody = status, body
}

func (f *fakeIdP) form() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenForm
}

// fixedNow is the clock every provider test runs at.
var fixedNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

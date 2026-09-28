package social

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testTeamID = "TEAM123456"
	testKeyID  = "KEYABC1234"
)

func testP8(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), k
}

func newAppleAt(t *testing.T) (*Apple, *fakeIdP, *ecdsa.PrivateKey) {
	t.Helper()
	f := newFakeIdP(t, "/auth/keys", "/auth/token")
	p8, k := testP8(t)
	a, err := NewApple("com.hotel.wifi", p8, testTeamID, testKeyID, "")
	if err != nil {
		t.Fatal(err)
	}
	a.tokenURL = f.srv.URL + "/auth/token"
	a.jwksURL = f.srv.URL + "/auth/keys"
	a.now = func() time.Time { return fixedNow }
	a.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	return a, f, k
}

func appleClaims(extra map[string]any) map[string]any {
	c := map[string]any{
		"iss":            "https://appleid.apple.com",
		"aud":            "com.hotel.wifi",
		"sub":            "001234.abcdef.1234",
		"iat":            fixedNow.Add(-time.Minute).Unix(),
		"exp":            fixedNow.Add(time.Hour).Unix(),
		"email":          "x7y8z9@privaterelay.appleid.com",
		"email_verified": "true",
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

func TestAppleAuthorizeURL(t *testing.T) {
	p8, _ := testP8(t)
	a, err := NewApple("com.hotel.wifi", p8, testTeamID, testKeyID, "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(a.AuthorizeURL("state-abc", "https://portal/auth/social/callback"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "appleid.apple.com" || u.Path != "/auth/authorize" {
		t.Errorf("unexpected auth host/path: %s%s", u.Host, u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": "com.hotel.wifi", "redirect_uri": "https://portal/auth/social/callback",
		"response_type": "code", "response_mode": "form_post", "state": "state-abc",
		"scope": "name email",
	} {
		if q.Get(k) != want {
			t.Errorf("query[%s] = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestAppleClientSecretJWT(t *testing.T) {
	a, f, k := newAppleAt(t)
	key, _ := rsaKeys(t)
	f.respondIDToken(signRS256(t, key, testKid, appleClaims(nil)))
	if _, err := a.Exchange(context.Background(), "code-1", "https://portal/cb"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	secret := f.form().Get("client_secret")
	parts := strings.Split(secret, ".")
	if len(parts) != 3 {
		t.Fatalf("client_secret is not a compact JWS: %q", secret)
	}
	var hdr, cl map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(hb, &hdr)
	_ = json.Unmarshal(cb, &cl)
	if hdr["alg"] != "ES256" || hdr["kid"] != testKeyID {
		t.Errorf("header = %v", hdr)
	}
	if cl["iss"] != testTeamID || cl["sub"] != "com.hotel.wifi" || cl["aud"] != "https://appleid.apple.com" {
		t.Errorf("claims = %v", cl)
	}
	iat, exp := int64(cl["iat"].(float64)), int64(cl["exp"].(float64))
	if iat != fixedNow.Unix() || exp <= iat || exp-iat > int64((183*24*time.Hour).Seconds()) {
		t.Errorf("iat/exp not sensible: iat=%d exp=%d", iat, exp)
	}
	// The signature verifies with the public half of the .p8 key, in the JWS
	// r||s encoding.
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("ES256 signature must be 64 bytes, got %d", len(sig))
	}
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&k.PublicKey, d[:], r, s) {
		t.Error("client_secret signature does not verify with the .p8 public key")
	}
	form := f.form()
	for kf, want := range map[string]string{
		"client_id": "com.hotel.wifi", "code": "code-1",
		"grant_type": "authorization_code", "redirect_uri": "https://portal/cb",
	} {
		if form.Get(kf) != want {
			t.Errorf("token form[%s] = %q, want %q", kf, form.Get(kf), want)
		}
	}
}

func TestAppleExchangeSuccess(t *testing.T) {
	for name, ev := range map[string]any{"string true": "true", "bool true": true} {
		t.Run(name, func(t *testing.T) {
			a, f, _ := newAppleAt(t)
			key, _ := rsaKeys(t)
			f.respondIDToken(signRS256(t, key, testKid, appleClaims(map[string]any{"email_verified": ev})))
			info, err := a.Exchange(context.Background(), "c", "https://portal/cb")
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if info.Sub != "001234.abcdef.1234" || info.Email != "x7y8z9@privaterelay.appleid.com" || !info.EmailVerified {
				t.Errorf("userinfo wrong: %+v", info)
			}
		})
	}
}

func TestAppleUnverifiedOrMissingEmail(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"email_verified false":  {"email_verified": "false"},
		"email_verified absent": {"email_verified": nil},
		"no email":              {"email": nil},
	} {
		t.Run(name, func(t *testing.T) {
			a, f, _ := newAppleAt(t)
			key, _ := rsaKeys(t)
			f.respondIDToken(signRS256(t, key, testKid, appleClaims(extra)))
			info, err := a.Exchange(context.Background(), "c", "https://portal/cb")
			if !errors.Is(err, ErrEmailUnverified) {
				t.Fatalf("expected ErrEmailUnverified, got %v", err)
			}
			if info == nil || info.EmailVerified {
				t.Errorf("expected info populated but EmailVerified=false: %+v", info)
			}
		})
	}
}

func TestAppleRejectsBadTokens(t *testing.T) {
	key, other := rsaKeys(t)
	for name, tok := range map[string]func() string{
		"wrong audience": func() string {
			return signRS256(t, key, testKid, appleClaims(map[string]any{"aud": "com.someone.else"}))
		},
		"expired": func() string {
			return signRS256(t, key, testKid, appleClaims(map[string]any{"exp": fixedNow.Add(-time.Hour).Unix()}))
		},
		"bad signature": func() string { return signRS256(t, other, testKid, appleClaims(nil)) },
		"wrong issuer": func() string {
			return signRS256(t, key, testKid, appleClaims(map[string]any{"iss": "https://evil.example"}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, f, _ := newAppleAt(t)
			f.respondIDToken(tok())
			info, err := a.Exchange(context.Background(), "c", "https://portal/cb")
			if err == nil || info != nil || errors.Is(err, ErrEmailUnverified) {
				t.Fatalf("expected hard rejection, got info=%+v err=%v", info, err)
			}
		})
	}
}

func TestAppleTokenError(t *testing.T) {
	a, f, _ := newAppleAt(t)
	f.respondError(http.StatusBadRequest, `{"error":"invalid_client"}`)
	_, err := a.Exchange(context.Background(), "c", "https://portal/cb")
	if err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("err didn't include upstream details: %v", err)
	}
	if strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatal("key material leaked into the error")
	}
}

func TestAppleConstructorValidation(t *testing.T) {
	p8, _ := testP8(t)
	if _, err := NewApple("", p8, testTeamID, testKeyID, ""); err == nil {
		t.Error("missing Services ID should fail")
	}
	if _, err := NewApple("svc", "", testTeamID, testKeyID, ""); err == nil {
		t.Error("missing key should fail")
	}
	if _, err := NewApple("svc", p8, "team", testKeyID, ""); err == nil {
		t.Error("malformed team id should fail")
	}
	if _, err := NewApple("svc", p8, testTeamID, "", ""); err == nil {
		t.Error("missing key id should fail")
	}
	if _, err := NewApple("svc", "not a key", testTeamID, testKeyID, ""); err == nil {
		t.Error("non-PEM key should fail")
	}
	rsaKey, _ := rsaKeys(t)
	der, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	rsaPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := NewApple("svc", rsaPEM, testTeamID, testKeyID, ""); err == nil {
		t.Error("an RSA key should fail: Apple keys are EC P-256")
	} else if strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Error("key material leaked into the error")
	}
}

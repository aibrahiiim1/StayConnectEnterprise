package social

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const workTenant = "11111111-2222-3333-4444-555555555555"

func newMicrosoftAt(t *testing.T, tenant string) (*Microsoft, *fakeIdP) {
	t.Helper()
	tn := tenant
	if tn == "" {
		tn = "common"
	}
	f := newFakeIdP(t, "/"+tn+"/discovery/v2.0/keys", "/"+tn+"/oauth2/v2.0/token")
	m, err := NewMicrosoft("ms-client", "ms-secret", "", tenant)
	if err != nil {
		t.Fatal(err)
	}
	m.loginBase = f.srv.URL
	m.now = func() time.Time { return fixedNow }
	m.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	return m, f
}

func msClaims(base, tid string, extra map[string]any) map[string]any {
	c := map[string]any{
		"iss":  base + "/" + tid + "/v2.0",
		"aud":  "ms-client",
		"sub":  "pairwise-sub",
		"oid":  "object-id-1",
		"tid":  tid,
		"iat":  fixedNow.Add(-time.Minute).Unix(),
		"nbf":  fixedNow.Add(-time.Minute).Unix(),
		"exp":  fixedNow.Add(time.Hour).Unix(),
		"name": "Alice",
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

func TestMicrosoftAuthorizeURL(t *testing.T) {
	m, _ := NewMicrosoft("cid", "sec", "", "")
	u, err := url.Parse(m.AuthorizeURL("state-abc", "https://portal/auth/social/callback"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "login.microsoftonline.com" || u.Path != "/common/oauth2/v2.0/authorize" {
		t.Errorf("unexpected auth host/path: %s%s", u.Host, u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id": "cid", "redirect_uri": "https://portal/auth/social/callback",
		"response_type": "code", "response_mode": "query", "state": "state-abc",
		"scope": "openid email profile", "prompt": "select_account",
	} {
		if q.Get(k) != want {
			t.Errorf("query[%s] = %q, want %q", k, q.Get(k), want)
		}
	}
	m2, _ := NewMicrosoft("cid", "sec", "", workTenant)
	if !strings.Contains(m2.AuthorizeURL("s", "https://p/cb"), "/"+workTenant+"/oauth2/v2.0/authorize") {
		t.Error("configured tenant not used in the authorize path")
	}
}

func TestMicrosoftConsumerAccountSuccess(t *testing.T) {
	m, f := newMicrosoftAt(t, "")
	key, _ := rsaKeys(t)
	f.respondIDToken(signRS256(t, key, testKid, msClaims(f.srv.URL, msaConsumerTenant, map[string]any{"email": "alice@outlook.com"})))

	info, err := m.Exchange(context.Background(), "code-1", "https://portal/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if info.Email != "alice@outlook.com" || !info.EmailVerified || info.Name != "Alice" {
		t.Errorf("userinfo wrong: %+v", info)
	}
	if info.Sub != msaConsumerTenant+":object-id-1" {
		t.Errorf("sub = %q, want tid:oid", info.Sub)
	}
	form := f.form()
	for k, want := range map[string]string{
		"client_id": "ms-client", "client_secret": "ms-secret", "code": "code-1",
		"grant_type": "authorization_code", "redirect_uri": "https://portal/cb",
	} {
		if form.Get(k) != want {
			t.Errorf("token form[%s] = %q, want %q", k, form.Get(k), want)
		}
	}
}

func TestMicrosoftConsumerPreferredUsernameFallback(t *testing.T) {
	m, f := newMicrosoftAt(t, "consumers")
	key, _ := rsaKeys(t)
	f.respondIDToken(signRS256(t, key, testKid, msClaims(f.srv.URL, msaConsumerTenant,
		map[string]any{"preferred_username": "bob@hotmail.com", "oid": nil})))
	info, err := m.Exchange(context.Background(), "c", "https://portal/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if info.Email != "bob@hotmail.com" || !info.EmailVerified || info.Sub != "pairwise-sub" {
		t.Errorf("userinfo wrong: %+v", info)
	}
}

func TestMicrosoftWorkAccountEmailVerification(t *testing.T) {
	cases := []struct {
		name     string
		extra    map[string]any
		wantErr  error
		wantMail string
	}{
		{"edov bool", map[string]any{"email": "a@contoso.com", "xms_edov": true}, nil, "a@contoso.com"},
		{"edov string", map[string]any{"email": "a@contoso.com", "xms_edov": "true"}, nil, "a@contoso.com"},
		{"verified_primary_email", map[string]any{"verified_primary_email": []any{"v@contoso.com"}}, nil, "v@contoso.com"},
		{"email without edov", map[string]any{"email": "a@contoso.com"}, ErrEmailUnverified, "a@contoso.com"},
		{"edov false", map[string]any{"email": "a@contoso.com", "xms_edov": false}, ErrEmailUnverified, "a@contoso.com"},
		{"no email at all", map[string]any{"preferred_username": "+201000000000"}, ErrEmailUnverified, ""},
		{"upn only", map[string]any{"preferred_username": "a@contoso.com"}, ErrEmailUnverified, "a@contoso.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, f := newMicrosoftAt(t, "")
			key, _ := rsaKeys(t)
			f.respondIDToken(signRS256(t, key, testKid, msClaims(f.srv.URL, workTenant, tc.extra)))
			info, err := m.Exchange(context.Background(), "c", "https://portal/cb")
			if !errors.Is(err, tc.wantErr) && !(tc.wantErr == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if info == nil || info.Email != tc.wantMail || info.EmailVerified != (tc.wantErr == nil) {
				t.Errorf("userinfo wrong: %+v", info)
			}
		})
	}
}

func TestMicrosoftRejectsBadTokens(t *testing.T) {
	key, other := rsaKeys(t)
	cases := []struct {
		name   string
		tenant string
		token  func(base string) string
	}{
		{"wrong audience", "", func(b string) string {
			return signRS256(t, key, testKid, msClaims(b, msaConsumerTenant, map[string]any{"email": "a@outlook.com", "aud": "someone-else"}))
		}},
		{"expired", "", func(b string) string {
			return signRS256(t, key, testKid, msClaims(b, msaConsumerTenant, map[string]any{"email": "a@outlook.com", "exp": fixedNow.Add(-10 * time.Minute).Unix()}))
		}},
		{"not yet valid", "", func(b string) string {
			return signRS256(t, key, testKid, msClaims(b, msaConsumerTenant, map[string]any{"email": "a@outlook.com", "nbf": fixedNow.Add(10 * time.Minute).Unix()}))
		}},
		{"bad signature", "", func(b string) string {
			return signRS256(t, other, testKid, msClaims(b, msaConsumerTenant, map[string]any{"email": "a@outlook.com"}))
		}},
		{"unknown kid", "", func(b string) string {
			return signRS256(t, key, "rotated-away", msClaims(b, msaConsumerTenant, map[string]any{"email": "a@outlook.com"}))
		}},
		{"issuer does not match tid", "", func(b string) string {
			return signRS256(t, key, testKid, msClaims(b, msaConsumerTenant, map[string]any{"email": "a@outlook.com", "iss": "https://evil.example/" + msaConsumerTenant + "/v2.0"}))
		}},
		{"other tenant for single-tenant config", workTenant, func(b string) string {
			return signRS256(t, key, testKid, msClaims(b, "99999999-2222-3333-4444-555555555555", map[string]any{"email": "a@contoso.com", "xms_edov": true}))
		}},
		{"work account on consumers", "consumers", func(b string) string {
			return signRS256(t, key, testKid, msClaims(b, workTenant, map[string]any{"email": "a@contoso.com", "xms_edov": true}))
		}},
		{"alg none", "", func(b string) string {
			good := signRS256(t, key, testKid, msClaims(b, msaConsumerTenant, map[string]any{"email": "a@outlook.com"}))
			parts := strings.Split(good, ".")
			return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"test-kid-1"}`)) + "." + parts[1] + "."
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, f := newMicrosoftAt(t, tc.tenant)
			f.respondIDToken(tc.token(f.srv.URL))
			info, err := m.Exchange(context.Background(), "c", "https://portal/cb")
			if err == nil || info != nil {
				t.Fatalf("expected rejection, got info=%+v err=%v", info, err)
			}
			if errors.Is(err, ErrEmailUnverified) {
				t.Fatalf("a rejected token must not look like an unverified email: %v", err)
			}
		})
	}
}

func TestMicrosoftTokenError(t *testing.T) {
	m, f := newMicrosoftAt(t, "")
	f.respondError(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"AADSTS70008: The provided authorization code has expired."}`)
	_, err := m.Exchange(context.Background(), "c", "https://portal/cb")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") || !strings.Contains(err.Error(), "AADSTS70008") {
		t.Fatalf("err didn't include upstream details: %v", err)
	}
	if strings.Contains(err.Error(), "ms-secret") {
		t.Fatal("client secret leaked into the error")
	}
}

func TestMicrosoftConstructorValidation(t *testing.T) {
	if _, err := NewMicrosoft("", "s", "", ""); err == nil {
		t.Error("missing client_id should fail")
	}
	if _, err := NewMicrosoft("c", "", "", ""); err == nil {
		t.Error("missing client_secret should fail")
	}
	if _, err := NewMicrosoft("c", "s", "", "bad/tenant"); err == nil {
		t.Error("tenant with a slash should fail")
	}
	m, err := NewMicrosoft("c", "s", "", "  ")
	if err != nil || m.Tenant != "common" {
		t.Errorf("blank tenant should default to common: %v %+v", err, m)
	}
}

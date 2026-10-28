package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

// Audit Phase 1 L12: the payload stays valid JSON whatever the reason holds.
func TestAuthFailPayloadEscapesControlCharacters(t *testing.T) {
	p := authFailPayload("bad\x01\ttoken\"\\\n", "GET", "/v1/appliance/hello\x7f")
	var m map[string]string
	if err := json.Unmarshal([]byte(p), &m); err != nil || m["reason"] != "bad\x01\ttoken\"\\\n" {
		t.Fatalf("payload %q is not valid JSON or lost the reason: %v", p, err)
	}
}

func signedToken(t *testing.T, priv ed25519.PrivateKey, iss string, method, path string) string {
	t.Helper()
	jti := make([]byte, 12)
	_, _ = rand.Read(jti)
	now := time.Now()
	tok, err := applianceauth.Encode(priv, applianceauth.Claims{
		Iss: iss, Kid: applianceauth.KeyID(priv.Public().(ed25519.PublicKey)), Iat: now.Unix(), Exp: now.Add(30 * time.Second).Unix(),
		Jti: base64.RawURLEncoding.EncodeToString(jti), Aud: applianceauth.Audience, Mth: method, Pth: path,
		Bsh: applianceauth.BodyHash(nil)})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// Audit Phase 1 L12: a non-uuid issuer is an audited 401 "not enrolled", never an unaudited 500.
// Perf audit D9-L3: last_seen_at is written at most once a minute per appliance.
func TestRequireApplianceIssuerAndLastSeen(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	h := RequireAppliance(pool, applianceauth.NewReplayCache(time.Minute, 100))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	call := func(iss string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/appliance/hello", nil)
		req.RemoteAddr = "198.51.100.41:5555"
		req.Header.Set("Authorization", "Bearer "+signedToken(t, priv, iss, http.MethodGet, "/v1/appliance/hello"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := call("not-a-uuid")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "not enrolled") {
		t.Fatalf("non-uuid issuer = %d %s, want 401 not enrolled", rec.Code, rec.Body.String())
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='appliance.auth_failed' AND host(ip)='198.51.100.41'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the refusal must be audited once, got %d (%v)", n, err)
	}

	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO appliances (serial, public_key, lifecycle_state)
        VALUES ('SC-TEST-LASTSEEN', $1, 'pending_approval') RETURNING id::text`,
		base64.RawStdEncoding.EncodeToString(pub)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if rec := call(id); rec.Code != http.StatusNoContent {
		t.Fatalf("signed call: %d %s", rec.Code, rec.Body.String())
	}
	var first, second time.Time
	_ = pool.QueryRow(ctx, `SELECT last_seen_at FROM appliances WHERE id=$1`, id).Scan(&first)
	time.Sleep(20 * time.Millisecond)
	if rec := call(id); rec.Code != http.StatusNoContent {
		t.Fatalf("second signed call: %d", rec.Code)
	}
	_ = pool.QueryRow(ctx, `SELECT last_seen_at FROM appliances WHERE id=$1`, id).Scan(&second)
	if first.IsZero() || !second.Equal(first) {
		t.Fatalf("last_seen_at must be written once a minute: %v then %v", first, second)
	}
	// An older value is refreshed.
	_, _ = pool.Exec(ctx, `UPDATE appliances SET last_seen_at = now() - interval '5 minutes' WHERE id=$1`, id)
	call(id)
	var third time.Time
	_ = pool.QueryRow(ctx, `SELECT last_seen_at FROM appliances WHERE id=$1`, id).Scan(&third)
	if time.Since(third) > time.Minute {
		t.Fatalf("a stale last_seen_at must be refreshed, got %v", third)
	}
}

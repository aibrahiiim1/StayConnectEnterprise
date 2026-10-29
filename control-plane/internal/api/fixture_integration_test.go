package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
	lic "github.com/stayconnect/enterprise/license"
)

type fixture struct {
	// header is added to every operatorCall request (the offline import reads X-Activation-Code).
	header   map[string]string
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	base     *Base
	identity *IdentityBase
	session  *auth.Session
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	_, assignKey, _ := ed25519.GenerateKey(rand.Reader)
	_, vendorKey, _ := ed25519.GenerateKey(rand.Reader)
	b := &Base{DB: pool, AssignKey: assignKey, ClientValid: 90 * 24 * time.Hour,
		Lic: &licensing.Service{DB: pool, Signer: lic.NewSigner(vendorKey)}}
	if err := RegisterActiveKey(ctx, b, assignKey.Public().(ed25519.PublicKey), "test"); err != nil {
		t.Fatal(err)
	}
	var opID string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, display_name, password_hash, status)
        VALUES ('owner@central.test','Owner','x','active') RETURNING id::text`).Scan(&opID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'platform_owner')`, opID); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: ctx, pool: pool, base: b,
		identity: &IdentityBase{Base: b, ReplayCache: applianceauth.NewReplayCache(2*time.Minute, 8192)},
		session: &auth.Session{OperatorID: opID, Email: "owner@central.test", IsSuperAdmin: true,
			Roles: []string{"platform_owner"}, CreatedAt: time.Now()}}
}

func (f *fixture) customerSite(name string) (string, string) {
	f.t.Helper()
	cust, _, err := createCustomer(f.ctx, f.pool, name, "")
	if err != nil {
		f.t.Fatal(err)
	}
	site, err := createSite(f.ctx, f.pool, cust, name+" site", "", "UTC", "", "")
	if err != nil {
		f.t.Fatal(err)
	}
	return cust, site
}

type box struct {
	priv   ed25519.PrivateKey
	pubB64 string
	serial string
	hwf    string
}

func newBox(serial, hwf string) box {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	// The fingerprint is hex, as the appliance sends it (registration refuses anything else, audit Phase 1 M11).
	return box{priv: priv, pubB64: base64.RawStdEncoding.EncodeToString(pub), serial: serial, hwf: hex.EncodeToString([]byte(hwf))}
}

func registerToken(t *testing.T, bx box, body []byte) string {
	t.Helper()
	pub := bx.priv.Public().(ed25519.PublicKey)
	now := time.Now()
	jti := make([]byte, 12)
	_, _ = rand.Read(jti)
	tok, err := applianceauth.Encode(bx.priv, applianceauth.Claims{
		Iss: applianceauth.KeyID(pub), Kid: applianceauth.KeyID(pub), Iat: now.Unix(), Exp: now.Add(30 * time.Second).Unix(),
		Jti: base64.RawURLEncoding.EncodeToString(jti), Aud: "stayconnect-cloud-api", Mth: http.MethodPost,
		Pth: "/v1/appliances/register", Bsh: applianceauth.BodyHash(body)})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func registerBody(bx box) []byte {
	body, _ := json.Marshal(map[string]string{"serial": bx.serial, "hardware_fingerprint": bx.hwf,
		"public_key": bx.pubB64, "wan_mac": "00:11:22:33:44:55", "hostname": "onegate"})
	return body
}

func (f *fixture) register(bx box) *httptest.ResponseRecorder {
	f.t.Helper()
	body := registerBody(bx)
	return f.registerWith(body, registerToken(f.t, bx, body))
}

func (f *fixture) registerWith(body []byte, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/appliances/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "198.51.100.7:40000"
	rec := httptest.NewRecorder()
	f.identity.RegisterHandler(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return m
}

func (f *fixture) operatorCall(h http.HandlerFunc, method, id string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.operatorCallAs(f.session, h, method, map[string]string{"id": id}, body)
}

func (f *fixture) operatorCallAs(s *auth.Session, h http.HandlerFunc, method string, params map[string]string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "/cloud/v1/x", bytes.NewReader(raw))
	req.RemoteAddr = "203.0.113.9:50000"
	for k, v := range f.header {
		req.Header.Set(k, v)
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	if s != nil {
		ctx = auth.ContextWithSession(ctx, s)
	}
	rec := httptest.NewRecorder()
	h(rec, req.WithContext(ctx))
	return rec
}

// codeFor is the activation code the appliance would show for its stored identity key (AD-1).
func (f *fixture) codeFor(appID string) string {
	f.t.Helper()
	var pub string
	if err := f.pool.QueryRow(f.ctx, `SELECT COALESCE(public_key,'') FROM appliances WHERE id=$1`, appID).Scan(&pub); err != nil {
		f.t.Fatal(err)
	}
	return codeOfPub(f.t, pub)
}

func codeOfPub(t *testing.T, pubB64 string) string {
	t.Helper()
	raw, err := base64.RawStdEncoding.DecodeString(pubB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		t.Fatalf("bad public key %q", pubB64)
	}
	return applianceauth.ActivationCode(ed25519.PublicKey(raw))
}

func (f *fixture) activate(appID, cust, site string, cap int) {
	f.t.Helper()
	rec := f.operatorCall(f.base.activate, http.MethodPost, appID, map[string]any{
		"customer_id": cust, "site_id": site, "reason": "test", "activation_code": f.codeFor(appID),
		"license": map[string]any{"max_concurrent_online_guests": cap, "valid_days": 30}})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("activate: %d %s", rec.Code, rec.Body.String())
	}
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

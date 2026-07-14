// Command nats-acctest proves the NATS mTLS + Auth Callout security matrix.
// It enrolls isolated test appliances (A1,A2 in Tenant A; B1 in Tenant B),
// issues each a client certificate, then connects each to the parallel mTLS
// NATS listener and verifies per-appliance subject isolation, cross-tenant
// denial, and revocation. Runs ON central. Fixtures are cleaned by the caller.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
)

var (
	base    = env("BASE", "http://127.0.0.1:8080")
	natsURL = env("NATS", "tls://127.0.0.1:4223")
	caFile  = env("CA", "/etc/stayconnect/nats-authz/server-ca.crt")
	pass    = env("PASS", "AcceptTest!2026")
	pa      = env("PA", "accept-pa@stayconnect.local")
	tenA    = env("TA", "aaaaaaaa-0000-0000-0000-00000000000a")
	siteA   = env("SA", "a5170000-0000-0000-0000-0000000000a1")
	tenB    = env("TB", "bbbbbbbb-0000-0000-0000-00000000000b")
	siteB   = env("SB", "b5170000-0000-0000-0000-0000000000b1")
	npass   = 0
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func ok(m string, a ...any)   { fmt.Printf("  PASS: "+m+"\n", a...); npass++ }
func fail(m string, a ...any) { fmt.Printf("  FAIL: "+m+"\n", a...); os.Exit(1) }

type appl struct {
	id      string
	certPEM string
	key     ed25519.PrivateKey
}

func main() {
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	login(cl)
	reauth(cl)

	a1 := enrollAndCert(cl, tenA, siteA, "ACPT-N1")
	a2 := enrollAndCert(cl, tenA, siteA, "ACPT-N2")
	b1 := enrollAndCert(cl, tenB, siteB, "ACPT-NB1")
	ok("enrolled + certified A1,A2 (Tenant A), B1 (Tenant B)")

	caPEM, _ := os.ReadFile(caFile)

	// 1. valid mTLS connect, no user/pass
	nc1 := connect(a1, caPEM)
	if nc1 == nil {
		fail("A1 mTLS connect failed")
	}
	ok("A1 connects via mTLS (no username/password)")

	// 2. A1 publishes its own heartbeat + telemetry (allowed)
	if pubDenied(nc1, "appliances."+a1.id+".heartbeat") {
		fail("A1 own heartbeat pub was denied")
	}
	ok("A1 publishes appliances.A1.heartbeat")
	if pubDenied(nc1, "appliances."+a1.id+".telemetry") {
		fail("A1 own telemetry pub was denied")
	}
	ok("A1 publishes appliances.A1.telemetry")

	// 3. A1 CANNOT publish to A2 (permission violation, delivered async)
	if !pubDenied(nc1, "appliances."+a2.id+".heartbeat") {
		fail("A1 was allowed to publish to A2 (isolation breach)")
	}
	ok("A1 cannot publish to A2 subjects")

	// 4. A1 CANNOT publish to B1 (cross-tenant)
	if !pubDenied(nc1, "appliances."+b1.id+".heartbeat") {
		fail("A1 was allowed to publish to B1 (cross-tenant breach)")
	}
	ok("A1 cannot publish to B1 (cross-tenant denied)")

	// 5. A1 CANNOT subscribe to A2 commands
	if !subDenied(nc1, "appliances."+a2.id+".commands") {
		fail("A1 was allowed to subscribe to A2 commands (isolation breach)")
	}
	ok("A1 cannot subscribe to A2 commands")

	// 6. A1 CANNOT access JetStream admin / system subjects
	if !pubDenied(nc1, "$JS.API.STREAM.CREATE.foo") {
		fail("A1 was allowed to hit JetStream admin subject")
	}
	ok("A1 cannot access JetStream admin subjects")

	// sanity: A1 CAN still publish its own (proves denials aren't false-positives)
	if pubDenied(nc1, "appliances."+a1.id+".heartbeat") {
		fail("A1 own heartbeat wrongly denied")
	}
	ok("A1 own subjects still allowed (denials are specific, not blanket)")
	nc1.Close()

	// 7. B1 isolated from Tenant A
	ncB := connect(b1, caPEM)
	if ncB == nil {
		fail("B1 mTLS connect failed")
	}
	if !pubDenied(ncB, "appliances."+a1.id+".heartbeat") {
		fail("B1 was allowed to publish to A1 (cross-tenant breach)")
	}
	ok("B1 is isolated from Tenant A subjects")
	ncB.Close()

	// 8. missing client cert rejected
	if nc, err := nats.Connect(natsURL, nats.RootCAs(caFile), nats.Timeout(4*time.Second)); err == nil {
		nc.Close()
		fail("connection WITHOUT client cert was accepted")
	}
	ok("missing client certificate is rejected")

	// 9. revoked cert rejected on NEW connection
	revoke(cl, a2.id)
	time.Sleep(1 * time.Second)
	if nc := tryConnect(a2, caPEM); nc != nil {
		nc.Close()
		fail("revoked cert (A2) was allowed to connect")
	}
	ok("revoked certificate is rejected on new connection")

	// 10. ACTIVE connection revocation (Phase 5A): fresh A3,A4 both connected
	// with auto-reconnect. Revoke A3 → its LIVE connection must terminate
	// (short-lived auth expiry → reconnect denied) while A4 stays connected.
	if os.Getenv("SKIP_ACTIVE_REVOKE") == "" {
		a3 := enrollAndCert(cl, tenA, siteA, "ACPT-N3")
		a4 := enrollAndCert(cl, tenA, siteA, "ACPT-N4")
		nc3 := connectRC(a3, caPEM)
		nc4 := connectRC(a4, caPEM)
		if nc3 == nil || nc4 == nil || !nc3.IsConnected() || !nc4.IsConnected() {
			fail("A3/A4 not both connected before active-revoke test")
		}
		ok("A3 and A4 both connected (reconnect enabled)")
		start := time.Now()
		revoke(cl, a3.id)
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if !nc3.IsConnected() {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if nc3.IsConnected() {
			fail("A3 live connection NOT terminated after revocation")
		}
		ok("A3 active connection terminated after revocation (latency ~%ds)", int(time.Since(start).Seconds()))
		// A4 may briefly blip on its own auth re-expiry; tolerate a short recovery.
		a4ok := false
		for i := 0; i < 20; i++ {
			if nc4.IsConnected() {
				a4ok = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !a4ok {
			fail("A4 (unrelated) was disconnected")
		}
		ok("A4 (unrelated appliance) remained connected")
		nc3.Close()
		nc4.Close()
	}

	fmt.Printf("\nNATS mTLS acceptance: %d checks passed.\n", npass)
}

// connectRC connects with auto-reconnect enabled (for active-revocation tests).
func connectRC(a *appl, caPEM []byte) *nats.Conn {
	nc, err := nats.Connect(natsURL, nats.Secure(tlsCfg(a, caPEM)), nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1), nats.ReconnectWait(500*time.Millisecond))
	if err != nil {
		return nil
	}
	return nc
}

// ---- HTTP fixture helpers ----

func login(cl *http.Client) {
	do(cl, "POST", base+"/v1/auth/login", jb(map[string]string{"email": pa, "password": pass}), 200)
}
func reauth(cl *http.Client) {
	do(cl, "POST", base+"/v1/auth/reauth", jb(map[string]string{"password": pass}), 200)
}
func revoke(cl *http.Client, appID string) {
	reauth(cl)
	list := do(cl, "GET", base+"/cloud/v1/certificates", nil, 200)
	var m struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal([]byte(list), &m)
	for _, c := range m.Data {
		if c["appliance_id"] == appID && c["status"] == "active" {
			do(cl, "POST", base+"/cloud/v1/certificates/"+c["id"].(string)+"/revoke", jb(map[string]any{"reason": "acctest"}), 200)
			return
		}
	}
}

func enrollAndCert(cl *http.Client, tenant, site, serial string) *appl {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	reauth(cl)
	tok := gj(do(cl, "POST", base+"/cloud/v1/appliance-bootstrap-tokens?tenant_id="+tenant,
		jb(map[string]any{"site_id": site, "expected_serial": serial, "ttl_hours": 1}), 201), "token")
	enr := do(nil, "POST", base+"/v1/appliances/enroll",
		jb(map[string]any{"bootstrap_token": tok, "serial": serial, "public_key": base64.RawStdEncoding.EncodeToString(pub)}), 200)
	appID := gj(enr, "appliance_id")
	do(cl, "POST", base+"/cloud/v1/appliances-admin/"+appID+"/claim", jb(map[string]any{}), 200)
	reauth(cl)
	do(cl, "POST", base+"/cloud/v1/appliances-admin/"+appID+"/assign",
		jb(map[string]any{"tenant_id": tenant, "site_id": site, "reason": "acctest"}), 200)
	// CSR with a SEPARATE mtls key
	_, mtlsPriv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: appID}}, mtlsPriv)
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	signedPost(priv, appID, "/v1/appliance/csr", jb(map[string]any{"csr_pem": string(csr)}))
	reauth(cl)
	iss := do(cl, "POST", base+"/cloud/v1/certificates/"+appID+"/issue", jb(map[string]any{}), 201)
	_ = iss
	fetch := signedGet(priv, appID, "/v1/appliance/certificate")
	return &appl{id: appID, certPEM: gj(fetch, "certificate_pem"), key: mtlsPriv}
}

// ---- NATS helpers ----

func tlsCfg(a *appl, caPEM []byte) *tls.Config {
	keyDER, _ := x509.MarshalPKCS8PrivateKey(a.key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair([]byte(a.certPEM), keyPEM)
	if err != nil {
		fail("keypair for %s: %v", a.id, err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool}
}

func connect(a *appl, caPEM []byte) *nats.Conn {
	nc := tryConnect(a, caPEM)
	if nc == nil {
		fail("%s could not connect via mTLS", a.id)
	}
	return nc
}

var (
	permMu   sync.Mutex
	permErrS string // last async permission-violation error text
)

func tryConnect(a *appl, caPEM []byte) *nats.Conn {
	nc, err := nats.Connect(natsURL, nats.Secure(tlsCfg(a, caPEM)), nats.Timeout(5*time.Second), nats.MaxReconnects(0),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, e error) {
			permMu.Lock()
			permErrS = e.Error()
			permMu.Unlock()
		}))
	if err != nil {
		return nil
	}
	return nc
}

func resetPerm() { permMu.Lock(); permErrS = ""; permMu.Unlock() }
func sawPermViolation() bool {
	permMu.Lock()
	defer permMu.Unlock()
	return strings.Contains(strings.ToLower(permErrS), "permission")
}

// pubDenied returns true iff publishing to subj triggers a permission violation.
func pubDenied(nc *nats.Conn, subj string) bool {
	resetPerm()
	_ = nc.Publish(subj, []byte("x"))
	_ = nc.Flush()
	time.Sleep(350 * time.Millisecond)
	return sawPermViolation()
}

// subDenied returns true iff subscribing to subj triggers a permission violation.
func subDenied(nc *nats.Conn, subj string) bool {
	resetPerm()
	_, _ = nc.SubscribeSync(subj)
	_ = nc.Flush()
	time.Sleep(350 * time.Millisecond)
	return sawPermViolation()
}

// ---- low-level ----

func signedTok(priv ed25519.PrivateKey, appID, method, path string, body []byte) string {
	pub := priv.Public().(ed25519.PublicKey)
	var nonce [16]byte
	rand.Read(nonce[:])
	now := time.Now().UTC()
	c := applianceauth.Claims{Iss: appID, Kid: applianceauth.KeyID(pub), Iat: now.Unix(), Exp: now.Add(30 * time.Second).Unix(),
		Jti: fmt.Sprintf("%x", nonce), Aud: applianceauth.Audience, Mth: method, Pth: path, Bsh: applianceauth.BodyHash(body), Ver: "acctest"}
	t, _ := applianceauth.Encode(priv, c)
	return t
}
func signedPost(priv ed25519.PrivateKey, appID, path string, body []byte) {
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+signedTok(priv, appID, "POST", path, body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("signed POST %s: %v", path, err)
	}
	resp.Body.Close()
}
func signedGet(priv ed25519.PrivateKey, appID, path string) string {
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("Authorization", "Bearer "+signedTok(priv, appID, "GET", path, nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("signed GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
func do(cl *http.Client, method, url string, body []byte, want int) string {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := cl
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		fail("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		fail("%s %s want %d got %d: %s", method, url, want, resp.StatusCode, b)
	}
	return string(b)
}
func jb(v any) []byte { b, _ := json.Marshal(v); return b }
func gj(s, k string) string {
	var m map[string]any
	json.Unmarshal([]byte(s), &m)
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

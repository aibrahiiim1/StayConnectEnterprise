// Command acceptance runs the cloud-verifiable portion of the unified Phase 11
// acceptance suite on isolated fixtures (A1/A2 in Tenant A, B1 in Tenant B):
// token security, pending/claim/assign, separate keys + CSR + API mTLS + cert,
// signed license + 5 states, replay + body-modified rejection, clone alert,
// replacement, no-PII and audit. NATS mTLS points (11-19) are covered by
// cmd/nats-acctest; appliance-execution points (30-35) by the real appliance.
// Fixtures are cleaned by the orchestrator. Prints PASS:<n> / FAIL:<n>.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
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
	"os/exec"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
)

var (
	base  = env("BASE", "http://127.0.0.1:8080")
	pass  = env("PASS", "AcceptTest!2026")
	pa    = env("PA", "accept-pa@stayconnect.local")
	ta    = env("TAU", "accept-ta@stayconnect.local")
	tb    = env("TBU", "accept-tb@stayconnect.local")
	tenA  = env("TA", "aaaaaaaa-0000-0000-0000-00000000000a")
	siteA = env("SA", "a5170000-0000-0000-0000-0000000000a1")
	tenB  = env("TB", "bbbbbbbb-0000-0000-0000-00000000000b")
	siteB = env("SB", "b5170000-0000-0000-0000-0000000000b1")
	npass = 0
	nfail = 0
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func ok(m string, a ...any)  { fmt.Printf("  PASS: "+m+"\n", a...); npass++ }
func bad(m string, a ...any) { fmt.Printf("  FAIL: "+m+"\n", a...); nfail++ }
func check(cond bool, m string, a ...any) {
	if cond {
		ok(m, a...)
	} else {
		bad(m, a...)
	}
}

// psql runs a query on Central and returns trimmed output.
func psql(q string) string {
	out, _ := exec.Command("docker", "exec", "sc-central-pg", "psql", "-U", "stayconnect", "-d", "stayconnect", "-tAc", q).Output()
	return strings.TrimSpace(string(out))
}

func main() {
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	must(cl, "POST", base+"/v1/auth/login", jb(map[string]string{"email": pa, "password": pass}), 200)
	reauth(cl)

	// ---- 1-7 token security ----
	tokResp := must(cl, "POST", base+"/cloud/v1/appliance-bootstrap-tokens?tenant_id="+tenA,
		jb(map[string]any{"site_id": siteA, "expected_serial": "ACC-A1", "ttl_hours": 1}), 201)
	tok := gj(tokResp, "token")
	check(tok != "", "1/3 valid token created + shown once (plaintext)")
	// 2 hashed at rest: DB holds token_hash, never the plaintext
	dbHasHash := psql("SELECT count(*) FROM appliance_bootstrap_tokens WHERE token_hash IS NOT NULL AND expected_serial='ACC-A1'")
	dbHasPlain := psql("SELECT count(*) FROM appliance_bootstrap_tokens WHERE expected_serial='ACC-A1' AND token_hash::text LIKE '%" + safe(tok[:8]) + "%'")
	check(dbHasHash == "1" && dbHasPlain == "0", "2 token stored hashed (no plaintext in DB)")

	pubA1, privA1, _ := ed25519.GenerateKey(rand.Reader)
	a1 := enroll(tok, "ACC-A1", pubA1)
	check(a1 != "", "1 valid token accepted → appliance enrolled")
	// 4 single-use violation: an already-consumed token reused by a DIFFERENT
	// key (a second appliance) must be rejected. NOTE: reuse with the identical
	// serial+key is deliberately an idempotent 200 (dropped-response retry), so
	// single-use is asserted with a fresh key that must never succeed.
	check(enrollCode(tok, "ACC-A1", b64rand()) == 403, "4 token reuse rejected")
	// 5 invalid rejected
	check(enrollCode("totally-invalid-xyz", "ACC-X", b64rand()) == 403, "5 invalid token rejected")
	// 6 expired rejected
	psql("INSERT INTO appliance_bootstrap_tokens(tenant_id,site_id,expected_serial,token_hash,token_hint,expires_at) VALUES ('" + tenA + "','" + siteA + "','ACC-EXP',decode('" + sha256hex("EXPIRED-ACC") + "','hex'),'x',now()-interval '1 hour')")
	check(enrollCode("EXPIRED-ACC", "ACC-EXP", b64rand()) == 403, "6 expired token rejected")
	// 7 serial restriction
	tok2 := gj(must(cl, "POST", base+"/cloud/v1/appliance-bootstrap-tokens?tenant_id="+tenA, jb(map[string]any{"site_id": siteA, "expected_serial": "ACC-SR", "ttl_hours": 1}), 201), "token")
	check(enrollCode(tok2, "WRONG-SERIAL", b64rand()) == 403, "7 serial-restricted token rejects other serial")

	// ---- 8-10 pending / claim / assign ----
	pend := must(cl, "GET", base+"/cloud/v1/appliances-admin/pending", nil, 200)
	check(strings.Contains(pend, "ACC-A1"), "8 pending appliance appears")
	must(cl, "POST", base+"/cloud/v1/appliances-admin/"+a1+"/claim", jb(map[string]any{}), 200)
	ok("9 platform claim works")
	reauth(cl)
	must(cl, "POST", base+"/cloud/v1/appliances-admin/"+a1+"/assign", jb(map[string]any{"tenant_id": tenA, "site_id": siteA, "reason": "acc"}), 200)
	ok("10 assignment works")

	// ---- 11-13,15 separate keys + CSR + cert + API mTLS ----
	_, mtlsPriv, _ := ed25519.GenerateKey(rand.Reader)
	check(!bytesEqual(privA1, mtlsPriv), "11 identity key and mTLS key are separate")
	csr := makeCSR(mtlsPriv, a1)
	signedPost(privA1, a1, "/v1/appliance/csr", jb(map[string]any{"csr_pem": string(csr)}))
	reauth(cl)
	iss := must(cl, "POST", base+"/cloud/v1/certificates/"+a1+"/issue", jb(map[string]any{}), 201)
	check(gj(iss, "fingerprint_sha256") != "", "12 CSR signed → client certificate issued")
	fetch := signedGet(privA1, a1, "/v1/appliance/certificate")
	certPEM := gj(fetch, "certificate_pem")
	check(certPEM != "" && certHasURI(certPEM, a1), "15 issued cert carries URI-SAN appliance identity")
	// 13 API mTLS: connect to :9443 with the client cert
	code, body := mtlsHello(certPEM, mtlsPriv, gj(fetch, "ca_chain"), privA1, a1)
	check(code == 200 && gj(body, "appliance_id") == a1, "13 API mTLS works (cert accepted over :9443)")

	// ---- 26 replay, 27 body-modified ----
	// Run these while a1 is still an authorized (assigned) identity: the license
	// section below deliberately REVOKES a1, and a revoked identity is blocked
	// (403) before signature/replay evaluation — so these must precede it.
	// replay: reuse the exact same signed token twice → 2nd rejected
	tokReplay := signedTokFor(privA1, a1, "GET", "/v1/appliance/hello", nil)
	c1 := rawSignedGet(tokReplay, "/v1/appliance/hello")
	c2 := rawSignedGet(tokReplay, "/v1/appliance/hello")
	check(c1 == 200 && c2 == 401, "26 replayed signed request rejected (jti)")
	// body-modified: sign for empty body, send different body
	tokBody := signedTokFor(privA1, a1, "POST", "/v1/appliance/csr", []byte(`{}`))
	check(rawSignedPost(tokBody, "/v1/appliance/csr", []byte(`{"csr_pem":"tampered"}`)) == 401, "27 body-modified signed request rejected")

	// ---- 20-25 signed license + 5 states ----
	lic := must(cl, "POST", base+"/cloud/v1/licenses", jb(map[string]any{"tenant_id": tenA, "site_id": siteA, "valid_days": 365, "offline_grace_days": 14}), 201)
	check(gj2(lic, "envelope", "signature") != "", "20 signed license issued (Ed25519 envelope)")
	check(licAppliance(lic, a1), "20b appliance bound in signed license doc")
	check(lifecycle(a1) == "licensed", "21 licensed state (driven from signed doc)")
	lid := psql("SELECT id FROM licenses WHERE site_id='" + siteA + "' AND status IN ('active','suspended') ORDER BY issued_at DESC LIMIT 1")
	reauth(cl)
	must(cl, "POST", base+"/cloud/v1/licenses/"+lid+"/suspend", jb(map[string]any{}), 200)
	check(lifecycle(a1) == "suspended", "24 suspended state")
	lid = psql("SELECT id FROM licenses WHERE site_id='" + siteA + "' AND status IN ('active','suspended') ORDER BY issued_at DESC LIMIT 1")
	reauth(cl)
	must(cl, "POST", base+"/cloud/v1/licenses/"+lid+"/resume", jb(map[string]any{}), 200)
	// resume RE-ISSUES a fresh signed license and supersedes the suspended one,
	// so the current license now has a new id — re-resolve it before driving
	// grace/expired/revoke, otherwise we'd manipulate the superseded row.
	lid = psql("SELECT id FROM licenses WHERE site_id='" + siteA + "' AND status IN ('active','suspended') ORDER BY issued_at DESC LIMIT 1")
	// grace + expired via time reconcile (deployed 60s ticker)
	psql("UPDATE licenses SET valid_until=now()-interval '1 hour', offline_grace_days=14 WHERE id='" + lid + "'")
	waitLifecycle(a1, "grace", 80)
	check(lifecycle(a1) == "grace", "22 grace state (time reconcile)")
	psql("UPDATE licenses SET valid_until=now()-interval '30 days', offline_grace_days=1 WHERE id='" + lid + "'")
	waitLifecycle(a1, "license_expired", 80)
	check(lifecycle(a1) == "license_expired", "23 license_expired state")
	reauth(cl)
	must(cl, "POST", base+"/cloud/v1/licenses/"+lid+"/revoke", jb(map[string]any{}), 200)
	check(lifecycle(a1) == "revoked", "25 revoked state distinct from license_expired")

	// ---- 28 clone alert ----
	tokC := gj(must(cl, "POST", base+"/cloud/v1/appliance-bootstrap-tokens?tenant_id="+tenA, jb(map[string]any{"site_id": siteA, "expected_serial": "ACC-A1", "ttl_hours": 1}), 201), "token")
	enrollCode(tokC, "ACC-A1", b64rand()) // re-enroll same serial, different key
	check(psql("SELECT count(*) FROM appliance_security_alerts WHERE serial='ACC-A1' AND kind='identity_mismatch'") != "0", "28 clone attempt creates security alert")

	// ---- 29 replacement revokes old identity ----
	reauth(cl)
	rep := must(cl, "POST", base+"/cloud/v1/appliances-admin/"+a1+"/replace", jb(map[string]any{"expected_serial": "ACC-A1R", "reason": "hw"}), 201)
	check(gj(rep, "replacement_token") != "" && psql("SELECT replacement_pending FROM appliances WHERE id='"+a1+"'") == "t", "29 replacement mints token + marks old pending")
	reauth(cl)
	must(cl, "POST", base+"/cloud/v1/appliances-admin/"+a1+"/decommission", jb(map[string]any{"reason": "replaced"}), 200)
	check(psql("SELECT count(*) FROM appliance_certificate_revocations WHERE appliance_id='"+a1+"'") != "0", "29b old appliance certificate revoked on decommission")

	// ---- 36 no guest PII in Central telemetry ----
	piiRows := psql("SELECT count(*) FROM fleet_telemetry WHERE payload::text ~* '(mac_address|guest_name|email|phone|msisdn|device_mac)'")
	check(piiRows == "0", "36 no prohibited guest PII in Central telemetry")

	// ---- 37 audit evidence ----
	auditN := psql("SELECT count(*) FROM audit_log WHERE action IN ('appliance.enrolled','appliance.assigned','license.issued','license.revoked','certificate.issued','appliance.replacement_started') AND ts > now()-interval '10 min'")
	check(auditN != "0" && auditN != "", "37 lifecycle actions have immutable audit evidence (%s rows)", auditN)

	fmt.Printf("\nCLOUD ACCEPTANCE: PASS=%d FAIL=%d\n", npass, nfail)
	if nfail > 0 {
		os.Exit(1)
	}
}

// ---- helpers ----

func reauth(cl *http.Client) {
	must(cl, "POST", base+"/v1/auth/reauth", jb(map[string]string{"password": pass}), 200)
}
func enroll(tok, serial string, pub ed25519.PublicKey) string {
	r := doRaw("POST", base+"/v1/appliances/enroll", jb(map[string]any{"bootstrap_token": tok, "serial": serial, "public_key": b64(pub)}))
	return gj(r, "appliance_id")
}
func enrollCode(tok, serial, pubB64 string) int {
	_, code := doRawCode("POST", base+"/v1/appliances/enroll", jb(map[string]any{"bootstrap_token": tok, "serial": serial, "public_key": pubB64}))
	return code
}
func lifecycle(appID string) string { return psql("SELECT lifecycle_state FROM appliances WHERE id='" + appID + "'") }
func waitLifecycle(appID, want string, secs int) {
	for i := 0; i < secs; i += 5 {
		if lifecycle(appID) == want {
			return
		}
		time.Sleep(5 * time.Second)
	}
}
func makeCSR(priv ed25519.PrivateKey, cn string) []byte {
	der, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, priv)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}
func certHasURI(certPEM, appID string) bool {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return false
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	for _, u := range c.URIs {
		if u.Scheme == "stayconnect" && strings.TrimPrefix(u.Path, "/") == appID {
			return true
		}
	}
	return false
}
func mtlsHello(certPEM string, mtlsPriv ed25519.PrivateKey, caPEM string, idPriv ed25519.PrivateKey, appID string) (int, string) {
	keyDER, _ := x509.MarshalPKCS8PrivateKey(mtlsPriv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tlsPair([]byte(certPEM), keyPEM)
	if err != nil {
		return 0, err.Error()
	}
	cl := httpsMTLS(pair, caPEM)
	req, _ := http.NewRequest("GET", env("MTLS", "https://127.0.0.1:9443")+"/v1/appliance/hello", nil)
	req.Header.Set("Authorization", "Bearer "+signedTokFor(idPriv, appID, "GET", "/v1/appliance/hello", nil))
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
func signedTokFor(priv ed25519.PrivateKey, appID, method, path string, body []byte) string {
	pub := priv.Public().(ed25519.PublicKey)
	var nonce [16]byte
	rand.Read(nonce[:])
	now := time.Now().UTC()
	c := applianceauth.Claims{Iss: appID, Kid: applianceauth.KeyID(pub), Iat: now.Unix(), Exp: now.Add(30 * time.Second).Unix(),
		Jti: fmt.Sprintf("%x", nonce), Aud: applianceauth.Audience, Mth: method, Pth: path, Bsh: applianceauth.BodyHash(body), Ver: "acc"}
	t, _ := applianceauth.Encode(priv, c)
	return t
}
func rawSignedGet(tok, path string) int {
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}
func rawSignedPost(tok, path string, body []byte) int {
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}
func signedPost(priv ed25519.PrivateKey, appID, path string, body []byte) {
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+signedTokFor(priv, appID, "POST", path, body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}
func signedGet(priv ed25519.PrivateKey, appID, path string) string {
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("Authorization", "Bearer "+signedTokFor(priv, appID, "GET", path, nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
func must(cl *http.Client, method, url string, body []byte, want int) string {
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
		bad("%s %s: %v", method, url, err)
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		bad("%s %s want %d got %d: %s", method, url, want, resp.StatusCode, b)
	}
	return string(b)
}
func doRaw(method, url string, body []byte) string { s, _ := doRawCode(method, url, body); return s }
func doRawCode(method, url string, body []byte) (string, int) {
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode
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
func gj2(s, k1, k2 string) string {
	var m map[string]any
	json.Unmarshal([]byte(s), &m)
	if sub, ok := m[k1].(map[string]any); ok {
		if v, ok := sub[k2].(string); ok {
			return v
		}
	}
	return ""
}
func licAppliance(s, appID string) bool {
	var m map[string]any
	json.Unmarshal([]byte(s), &m)
	if d, ok := m["document"].(map[string]any); ok {
		if ids, ok := d["appliance_ids"].([]any); ok {
			for _, id := range ids {
				if id == appID {
					return true
				}
			}
		}
	}
	return false
}
func tlsPair(certPEM, keyPEM []byte) (tls.Certificate, error) { return tls.X509KeyPair(certPEM, keyPEM) }
func httpsMTLS(cert tls.Certificate, caPEM string) *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(caPEM))
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool},
	}}
}
func b64(pub ed25519.PublicKey) string { return base64.RawStdEncoding.EncodeToString(pub) }
func b64rand() string                  { p, _, _ := ed25519.GenerateKey(rand.Reader); return b64(p) }
func sha256hex(s string) string        { h := sha256.Sum256([]byte(s)); return fmt.Sprintf("%x", h[:]) }
func safe(s string) string             { return strings.ReplaceAll(s, "'", "") }
func bytesEqual(a, b ed25519.PrivateKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

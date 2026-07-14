// Command pkitest is a Phase-3 acceptance client. It plays the appliance role
// end-to-end against a live ctrlapi: enroll → submit CSR → (platform issues) →
// fetch cert → mTLS hello (accepted) → (platform revokes) → mTLS hello
// (rejected). It reuses the real applianceauth signer so it exercises the
// exact production request-binding. Intended to run ON the central server.
//
// It also drives the platform-side actions via a password login + reauth, so a
// single invocation proves the whole PKI/mTLS path. All fixtures it creates use
// the ACPT-PKI serial and are cleaned by the caller.
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
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
)

var (
	base  = env("PKITEST_BASE", "http://127.0.0.1:8080")
	mtls  = env("PKITEST_MTLS", "https://127.0.0.1:9443")
	tenA  = env("PKITEST_TENANT", "aaaaaaaa-0000-0000-0000-00000000000a")
	siteA = env("PKITEST_SITE", "a5170000-0000-0000-0000-0000000000a1")
	pass  = env("PKITEST_PASS", "AcceptTest!2026")
	email = env("PKITEST_EMAIL", "accept-pa@stayconnect.local")
	pass2 = 0
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fail(msg string, args ...any) { fmt.Printf("  FAIL: "+msg+"\n", args...); os.Exit(1) }
func ok(msg string, args ...any)   { fmt.Printf("  PASS: "+msg+"\n", args...); pass2++ }

func main() {
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 15 * time.Second}

	// 1. Platform login.
	must(cl, "POST", base+"/v1/auth/login", jbody(map[string]string{"email": email, "password": pass}), 200)
	// reauth for sensitive actions.
	must(cl, "POST", base+"/v1/auth/reauth", jbody(map[string]string{"password": pass}), 200)

	// 2. Mint a Site-A token + enroll a synthetic appliance.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.RawStdEncoding.EncodeToString(pub)
	tokResp := must(cl, "POST", base+"/cloud/v1/appliance-bootstrap-tokens?tenant_id="+tenA,
		jbody(map[string]any{"site_id": siteA, "expected_serial": "ACPT-PKI", "ttl_hours": 1}), 201)
	token := gjson(tokResp, "token")
	enr := must(nil, "POST", base+"/v1/appliances/enroll",
		jbody(map[string]any{"bootstrap_token": token, "serial": "ACPT-PKI", "public_key": pubB64}), 200)
	appID := gjson(enr, "appliance_id")
	if appID == "" {
		fail("enroll returned no appliance_id: %s", enr)
	}
	ok("synthetic appliance enrolled id=%s", appID)

	// 3. Platform claim + assign.
	must(cl, "POST", base+"/cloud/v1/appliances-admin/"+appID+"/claim", jbody(map[string]any{}), 200)
	must(cl, "POST", base+"/cloud/v1/appliances-admin/"+appID+"/assign",
		jbody(map[string]any{"tenant_id": tenA, "site_id": siteA, "reason": "pki test"}), 200)
	ok("assigned to Site A")

	// 4. Appliance generates CSR (same ed25519 identity key) and submits it,
	//    authenticated with a bound signed request.
	csrPEM := makeCSR(priv, appID)
	csrBody := jbody(map[string]any{"csr_pem": string(csrPEM)})
	signedPost(priv, appID, "POST", "/v1/appliance/csr", csrBody, 202)
	ok("CSR submitted (signed-auth)")

	// 5. Platform issues the certificate.
	iss := must(cl, "POST", base+"/cloud/v1/certificates/"+appID+"/issue", jbody(map[string]any{}), 201)
	fpr := gjson(iss, "fingerprint_sha256")
	if fpr == "" {
		fail("issue returned no fingerprint: %s", iss)
	}
	ok("certificate issued fpr=%s…", fpr[:16])

	// 6. Appliance fetches the certificate + CA chain.
	fetch := signedGet(priv, appID, "/v1/appliance/certificate")
	certPEM := gjson(fetch, "certificate_pem")
	caPEM := gjson(fetch, "ca_chain")
	if certPEM == "" || caPEM == "" {
		fail("fetch missing cert/ca: %s", fetch)
	}
	ok("certificate + CA chain fetched")

	// 7. mTLS hello with the issued client cert — must be ACCEPTED.
	mc := mtlsClient(certPEM, priv, caPEM)
	code, body := rawGet(mc, mtls+"/mtls/hello")
	if code != 200 {
		fail("mTLS hello expected 200, got %d %s", code, body)
	}
	if gjson(body, "appliance_id") != appID {
		fail("mTLS identity mismatch: %s", body)
	}
	ok("mTLS hello accepted, server bound appliance_id=%s", appID)

	// 8. Find the cert id, platform revokes it.
	certList := must(cl, "GET", base+"/cloud/v1/certificates", nil, 200)
	certID := firstCertIDForFpr(certList, fpr)
	if certID == "" {
		fail("could not find issued cert id in list")
	}
	must(cl, "POST", base+"/cloud/v1/certificates/"+certID+"/revoke", jbody(map[string]any{"reason": "pki test revoke"}), 200)
	ok("certificate revoked")

	// 9. mTLS hello again — must be REJECTED (revoked cert).
	code, body = rawGet(mc, mtls+"/mtls/hello")
	if code == 200 {
		fail("revoked cert still accepted by mTLS! %s", body)
	}
	ok("revoked certificate rejected by mTLS (code=%d)", code)

	fmt.Printf("\nPKI/mTLS: %d checks passed. appliance_id=%s\n", pass2, appID)
}

// ---- helpers ----

func makeCSR(priv ed25519.PrivateKey, cn string) []byte {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn},
	}, priv)
	if err != nil {
		fail("make CSR: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func signedTok(priv ed25519.PrivateKey, appID, method, path string, body []byte) string {
	pub := priv.Public().(ed25519.PublicKey)
	now := time.Now().UTC()
	var nonce [16]byte
	rand.Read(nonce[:])
	c := applianceauth.Claims{
		Iss: appID, Kid: applianceauth.KeyID(pub), Iat: now.Unix(), Exp: now.Add(30 * time.Second).Unix(),
		Jti: fmt.Sprintf("%x", nonce), Aud: applianceauth.Audience, Mth: method, Pth: path,
		Bsh: applianceauth.BodyHash(body), Ver: "pkitest",
	}
	tok, err := applianceauth.Encode(priv, c)
	if err != nil {
		fail("sign: %v", err)
	}
	return tok
}

func signedPost(priv ed25519.PrivateKey, appID, method, path string, body []byte, want int) string {
	req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+signedTok(priv, appID, method, path, body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		fail("%s %s expected %d got %d: %s", method, path, want, resp.StatusCode, b)
	}
	return string(b)
}

func signedGet(priv ed25519.PrivateKey, appID, path string) string {
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("Authorization", "Bearer "+signedTok(priv, appID, "GET", path, nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		fail("GET %s expected 200 got %d: %s", path, resp.StatusCode, b)
	}
	return string(b)
}

func mtlsClient(certPEM string, priv ed25519.PrivateKey, caPEM string) *http.Client {
	keyDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair([]byte(certPEM), keyPEM)
	if err != nil {
		fail("client keypair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(caPEM))
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool},
	}}
}

func rawGet(cl *http.Client, url string) (int, string) {
	resp, err := cl.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func must(cl *http.Client, method, url string, body []byte, want int) string {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, rdr)
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
		fail("%s %s expected %d got %d: %s", method, url, want, resp.StatusCode, b)
	}
	return string(b)
}

func jbody(v any) []byte { b, _ := json.Marshal(v); return b }

func gjson(s, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if str, ok := v.(string); ok {
			return str
		}
	}
	return ""
}

func firstCertIDForFpr(list, fpr string) string {
	var m struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal([]byte(list), &m)
	for _, c := range m.Data {
		if c["fingerprint_sha256"] == fpr {
			if id, ok := c["id"].(string); ok {
				return id
			}
		}
	}
	return ""
}

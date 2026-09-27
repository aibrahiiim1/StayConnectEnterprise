// Command pkitest plays the appliance role end-to-end against a live ctrlapi to prove the PKI/mTLS path:
// token-less registration → operator activation → CSR (auto-signed because the appliance is activated) →
// fetch certificate → hello over mTLS (accepted) → operator reissue-certificate with no waiting CSR, which
// revokes the certificate → hello over mTLS (rejected). It reuses the production request signer.
//
// Fixtures: serial ACPT-PKI-<n> in a customer/site it creates. Needs a platform_admin login.
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
	must(cl, "POST", base+"/v1/auth/login", jbody(map[string]string{"email": email, "password": pass}), 200)
	must(cl, "POST", base+"/v1/auth/reauth", jbody(map[string]string{"password": pass}), 200)

	// 1. Token-less, self-signed registration → WAITING.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	serial := fmt.Sprintf("ACPT-PKI-%d", time.Now().Unix()%100000)
	regBody := jbody(map[string]any{"serial": serial, "public_key": base64.RawStdEncoding.EncodeToString(pub),
		"hardware_fingerprint": "HWF-" + serial, "wan_mac": "02:00:00:00:00:01"})
	reg := signedCall(priv, applianceauth.KeyID(pub), "POST", "/v1/appliances/register", regBody, 200)
	appID := gjson(reg, "appliance_id")
	if appID == "" {
		fail("register returned no appliance_id: %s", reg)
	}
	ok("appliance registered id=%s", appID)

	// 2. Operator activates it (new customer + site, licence terms).
	must(cl, "POST", base+"/cloud/v1/appliances/"+appID+"/activate", jbody(map[string]any{
		"new_customer": map[string]any{"name": "PKI test " + serial},
		"new_site":     map[string]any{"name": "PKI site", "timezone": "UTC"},
		"license":      map[string]any{"max_concurrent_online_guests": 10, "valid_days": 30},
	}), 200)
	ok("activated")

	// 3. The appliance submits a CSR with a SEPARATE transport key; it is signed on arrival.
	_, tlsPriv, _ := ed25519.GenerateKey(rand.Reader)
	signedCall(priv, appID, "POST", "/v1/appliance/csr", jbody(map[string]any{"csr_pem": string(makeCSR(tlsPriv, appID))}), 201)
	ok("CSR auto-signed for an activated appliance")

	// 4. Fetch the certificate + CA chain.
	fetch := signedGet(priv, appID, "/v1/appliance/certificate")
	certPEM, caPEM := gjson(fetch, "certificate_pem"), gjson(fetch, "ca_chain")
	if certPEM == "" || caPEM == "" {
		fail("fetch missing cert/ca: %s", fetch)
	}
	ok("certificate + CA chain fetched")

	// 5. hello over mTLS — ACCEPTED.
	mc := mtlsClient(certPEM, tlsPriv, caPEM)
	code, body := mtlsHello(mc, priv, appID)
	if code != 200 || gjson(body, "appliance_id") != appID {
		fail("mTLS hello expected 200 for %s, got %d %s", appID, code, body)
	}
	ok("mTLS hello accepted, appliance_id=%s", appID)

	// 6. Operator reissues the certificate with no CSR waiting → the current one is revoked.
	must(cl, "POST", base+"/v1/auth/reauth", jbody(map[string]string{"password": pass}), 200)
	must(cl, "POST", base+"/cloud/v1/appliances/"+appID+"/reissue-certificate", jbody(map[string]any{"reason": "pki test"}), 200)
	ok("certificate revoked by reissue")

	// 7. hello over mTLS — REJECTED.
	if code, body = mtlsHello(mc, priv, appID); code == 200 {
		fail("revoked certificate still accepted by mTLS! %s", body)
	}
	ok("revoked certificate rejected by mTLS (code=%d)", code)

	fmt.Printf("\nPKI/mTLS: %d checks passed. appliance_id=%s serial=%s\n", pass2, appID, serial)
}

// ---- helpers ----

func mtlsHello(cl *http.Client, idPriv ed25519.PrivateKey, appID string) (int, string) {
	req, _ := http.NewRequest("GET", mtls+"/v1/appliance/hello", nil)
	req.Header.Set("Authorization", "Bearer "+signedTok(idPriv, appID, "GET", "/v1/appliance/hello", nil))
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func signedCall(priv ed25519.PrivateKey, iss, method, path string, body []byte, want int) string {
	req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+signedTok(priv, iss, method, path, body))
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

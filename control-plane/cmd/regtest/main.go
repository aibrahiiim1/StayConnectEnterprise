// Command regtest is a minimal token-less appliance-registration client used by
// the lifecycle/factory-reset regression suite. It generates a fresh Ed25519
// identity, self-signs the registration request, and POSTs /v1/appliances/register
// with a chosen serial — so a test can simulate a factory reset (same serial, new
// identity). It is a TEST helper only; it is never part of the production runtime.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "control-plane base URL")
	serial := flag.String("serial", "", "hardware serial to register")
	hwf := flag.String("hwf", "ZZZ-HWF", "hardware fingerprint")
	flag.Parse()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	body, _ := json.Marshal(map[string]any{
		"serial": *serial, "wan_mac": "02:00:00:aa:bb:01", "lan_mac": "02:00:00:aa:bb:02",
		"hardware_fingerprint": *hwf, "hostname": "regtest", "model": "regtest",
		"public_key": base64.RawStdEncoding.EncodeToString(pub),
	})
	const path = "/v1/appliances/register"
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	now := time.Now().UTC()
	tok, err := applianceauth.Encode(priv, applianceauth.Claims{
		Iss: applianceauth.KeyID(pub), Kid: applianceauth.KeyID(pub),
		Iat: now.Unix(), Exp: now.Add(30 * time.Second).Unix(),
		Jti: fmt.Sprintf("%x", nonce), Aud: applianceauth.Audience,
		Mth: "POST", Pth: path, Bsh: applianceauth.BodyHash(body), Ver: "regtest",
	})
	if err != nil {
		fmt.Println("SIGN-ERR", err)
		return
	}
	req, _ := http.NewRequest("POST", *base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("HTTP-ERR", err)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	fmt.Printf("HTTP %d %s\n", resp.StatusCode, string(b))
}

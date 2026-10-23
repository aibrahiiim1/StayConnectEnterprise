// Command regtest is a test-only token-less appliance-registration client; re-registering a serial with a new
// identity simulates a factory reset.
//
// With -signin it instead signs an operator in through both steps (password from $ADMIN_PASS, then the
// authenticator code) and writes the session to a curl cookie jar:
//
//	ADMIN_PASS=... regtest -signin -base $API -email $ADMIN_EMAIL -jar $CJ
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
	"net/http/cookiejar"
	"os"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/opsignin"
)

// signIn takes the password from the environment so it never shows in a process list.
func signIn(base, email, jarPath string) error {
	pass := os.Getenv("ADMIN_PASS")
	if email == "" || jarPath == "" || pass == "" {
		return fmt.Errorf("-email, -jar and $ADMIN_PASS are required")
	}
	kr, err := opsignin.Open(opsignin.DefaultKeyringPath())
	if err != nil {
		return err
	}
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	if err := kr.SignIn(cl, base, email, pass); err != nil {
		return err
	}
	return opsignin.WriteCurlJar(cl, base, jarPath)
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "control-plane base URL")
	serial := flag.String("serial", "", "hardware serial to register")
	hwf := flag.String("hwf", "ZZZ-HWF", "hardware fingerprint")
	signin := flag.Bool("signin", false, "sign an operator in and write a curl cookie jar instead")
	email := flag.String("email", "", "-signin: the operator's email")
	jarPath := flag.String("jar", "", "-signin: the curl cookie jar to write")
	flag.Parse()
	if *signin {
		if err := signIn(*base, *email, *jarPath); err != nil {
			fmt.Fprintln(os.Stderr, "SIGNIN-ERR", err)
			os.Exit(1)
		}
		return
	}

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

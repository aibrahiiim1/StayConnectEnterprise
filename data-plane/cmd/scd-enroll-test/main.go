// scd-enroll-test is a standalone helper for the phase5 smoke script: it runs the identity resolve + signed /hello
// loop without touching nft/tc/postgres, so it can run beside a live scd.
//
// Env (both required): SCD_IDENTITY_DIR, SCD_CTRLAPI_BASE. A factory-clean identity dir is registered token-less.
// Exits 0 on success; prints the resolved identity and hello response.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

func fatal(msg string, args ...any) {
	fmt.Fprintf(os.Stderr, "scd-enroll-test: "+msg+"\n", args...)
	os.Exit(1)
}

func main() {
	dir := os.Getenv("SCD_IDENTITY_DIR")
	base := os.Getenv("SCD_CTRLAPI_BASE")
	if dir == "" || base == "" {
		fatal("SCD_IDENTITY_DIR and SCD_CTRLAPI_BASE are required")
	}

	// --replay: sign one JWT, call /hello twice, print both status codes (exercises the replay cache).
	replay := len(os.Args) > 1 && os.Args[1] == "--replay"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Reuse a registered identity, otherwise register this keypair with Central token-less.
	store := &identity.Store{Dir: dir}
	ident, err := store.Register(ctx, base, nil)
	if err != nil {
		fatal("register: %v", err)
	}

	jwt, err := applianceauth.Sign(ident.PrivateKey(), ident.ApplianceID)
	if err != nil {
		fatal("sign: %v", err)
	}

	if replay {
		c1 := callHello(ctx, base, jwt)
		c2 := callHello(ctx, base, jwt)
		fmt.Printf("call1=%d call2=%d\n", c1, c2)
		return
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/appliance/hello", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("hello call: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		fatal("hello status=%d body=%s", resp.StatusCode, string(body))
	}

	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"appliance_id": ident.ApplianceID,
		"tenant_id":    ident.TenantID,
		"site_id":      ident.SiteID,
		"serial":       ident.Serial,
		"public_key":   ident.PublicKeyB64,
		"hello":        json.RawMessage(body),
	})
}

func callHello(ctx context.Context, base, jwt string) int {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/appliance/hello", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

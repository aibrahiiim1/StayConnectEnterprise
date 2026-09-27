// scd-enroll-test is a standalone helper used by the phase5 smoke script.
// It runs the identity resolve + signed /hello loop without touching
// nft/tc/postgres, so it can run side-by-side with a live scd for tests.
//
// Env vars:
//
//	SCD_IDENTITY_DIR   (required)
//	SCD_CTRLAPI_BASE   (required)
//
// A factory-clean identity dir is registered token-less (POST /v1/appliances/register).
//
// Exits 0 on success; prints the resolved identity + hello response to stdout.
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

	// --replay: sign one JWT, call /hello twice, print the two status codes.
	// Used by the smoke script to exercise the replay cache.
	replay := len(os.Args) > 1 && os.Args[1] == "--replay"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Token-less registration (enrollment tokens no longer exist): reuse a registered identity, otherwise
	// register this keypair with Central.
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

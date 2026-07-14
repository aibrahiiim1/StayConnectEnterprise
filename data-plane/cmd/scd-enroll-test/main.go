// scd-enroll-test is a standalone helper used by the phase5 smoke script.
// It runs the identity resolve + signed /hello loop without touching
// nft/tc/postgres, so it can run side-by-side with a live scd for tests.
//
// Env vars:
//
//	SCD_IDENTITY_DIR   (required)
//	SCD_CTRLAPI_BASE   (required)
//	SCD_BOOTSTRAP_TOKEN, SCD_SERIAL (required on first run)
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

	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)


func fatal(msg string, args ...any) {
	fmt.Fprintf(os.Stderr, "scd-enroll-test: "+msg+"\n", args...)
	os.Exit(1)
}

func main() {
	// Early dispatch: NATS-only subcommands don't need an identity.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--nft-publish":
			natsPublishNFT()
			return
		case "--nft-await":
			natsAwaitNFT()
			return
		}
	}

	dir := os.Getenv("SCD_IDENTITY_DIR")
	base := os.Getenv("SCD_CTRLAPI_BASE")
	token := os.Getenv("SCD_BOOTSTRAP_TOKEN")
	serial := os.Getenv("SCD_SERIAL")
	if dir == "" || base == "" {
		fatal("SCD_IDENTITY_DIR and SCD_CTRLAPI_BASE are required")
	}

	// --replay: sign one JWT, call /hello twice, print the two status codes.
	// Used by the smoke script to exercise the replay cache.
	replay := len(os.Args) > 1 && os.Args[1] == "--replay"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	store := &identity.Store{Dir: dir}
	ident, err := store.LoadOrEnroll(ctx, base, token, serial, false)
	if err != nil {
		fatal("enroll: %v", err)
	}
	if ident == nil {
		fatal("no identity and no bootstrap token")
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

// --nft-publish: impersonate a peer scd and publish an "add" op. Env:
//   NATS_URL    (default nats://127.0.0.1:4222)
//   SITE_ID     required
//   IP          required
//   TTL_SECONDS default 600
//   SENDER_ID   default "peer-test"
func natsPublishNFT() {
	url := envOr("NATS_URL", "nats://127.0.0.1:4222")
	site := os.Getenv("SITE_ID")
	ip := os.Getenv("IP")
	if site == "" || ip == "" {
		fatal("SITE_ID and IP required")
	}
	ttl := envOr("TTL_SECONDS", "600")
	sender := envOr("SENDER_ID", "peer-test")
	nc, err := nats.Connect(url)
	if err != nil {
		fatal("nats connect: %v", err)
	}
	defer nc.Drain()
	body := fmt.Sprintf(`{"op":"add","ip":"%s","ttl_seconds":%s,"sender":"%s"}`, ip, ttl, sender)
	if err := nc.Publish("nft."+site, []byte(body)); err != nil {
		fatal("publish: %v", err)
	}
	// Flush so the message is in flight before we exit.
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		fatal("flush: %v", err)
	}
	fmt.Println("ok")
}

// --nft-await: subscribe to nft.{SITE_ID} and print up to N messages.
// Used by tests to capture what scd publishes during an auth flow.
//   SITE_ID required
//   WAIT_SECONDS default 5
//   N default 1
func natsAwaitNFT() {
	url := envOr("NATS_URL", "nats://127.0.0.1:4222")
	site := os.Getenv("SITE_ID")
	if site == "" {
		fatal("SITE_ID required")
	}
	wait := parseIntOr(os.Getenv("WAIT_SECONDS"), 5)
	n := parseIntOr(os.Getenv("N"), 1)
	nc, err := nats.Connect(url)
	if err != nil {
		fatal("nats connect: %v", err)
	}
	defer nc.Drain()
	got := 0
	done := make(chan struct{})
	_, err = nc.Subscribe("nft."+site, func(m *nats.Msg) {
		fmt.Println(string(m.Data))
		got++
		if got >= n {
			close(done)
		}
	})
	if err != nil {
		fatal("subscribe: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Duration(wait) * time.Second):
	}
	if got == 0 {
		fatal("no messages within %ds", wait)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func parseIntOr(s string, d int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return d
	}
	return n
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

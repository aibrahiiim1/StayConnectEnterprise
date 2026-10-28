// keybootstrap creates appliance-local key material (throttle, OTP, DEKs) and its DB lifecycle metadata at deployment.
// Run once per appliance BEFORE scd starts, with a privileged database role (never svc_scd). Idempotent and fail-closed; prints only non-secret status.
//
// Env:
//
//	KEYBOOTSTRAP_DSN      operational/migration DSN (role permitted to write otp_hmac_key_generations)
//	                      also creates the voucher code-encryption key (DEK) that a production build
//	                      requires, because IAM-v2 VOUCHER cannot be configured off there
//	SCD_SECRETS_DIR       directory for appliance-local key files (default /etc/stayconnect/secrets)
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/keybootstrap"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(0)
	dsn := os.Getenv("KEYBOOTSTRAP_DSN")
	if dsn == "" {
		log.Fatal("keybootstrap: KEYBOOTSTRAP_DSN is required (operational/migration role)")
	}
	secretsDir := envOr("SCD_SECRETS_DIR", "/etc/stayconnect/secrets")
	throttlePath := filepath.Join(secretsDir, "throttle.key")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1) Durable-throttle key (created once; runtime is load-only).
	if err := keybootstrap.BootstrapThrottleKey(throttlePath); err != nil {
		log.Fatalf("keybootstrap: throttle key: %v", err)
	}
	log.Printf("keybootstrap: throttle key ready at %s", throttlePath)

	// 2) Sealing and HMAC keys. Each is created once here and is load-only at runtime: a runtime-minted key would orphan
	// what the previous one sealed or indexed.
	for _, k := range []struct{ file, label, ready string }{
		// Voucher code-encryption key: production locks IAM-v2 VOUCHER on, so scd refuses to start without it.
		{"voucher_dek.key", "voucher DEK", "voucher code-encryption key"},
		// Seals the credential half of iam_v2.sign_in_attempts; without it attempts are recorded with those columns NULL.
		{"signin_attempts_dek.key", "sign-in attempt DEK", "sign-in attempt sealing key"},
		// Seals card provider credentials; without it card payment is "not ready".
		{"payment_dek.key", "payment key", "card payment sealing key"},
		// Keys the resume-token and recovery-code HMACs of open selection; without it open selection is unavailable.
		{"anonymous_access.key", "anonymous-access key", "anonymous-access key"},
		// Seals email, SMS and WhatsApp sender secrets; without it a sealed sender cannot send.
		{"notify_dek.key", "notification-secret key", "notification-secret sealing key"},
		// Seals social app client secrets; without it no social provider is built (never a plaintext fallback).
		{"social_dek.key", "social sign-in key", "social sign-in sealing key"},
		// Seals operators' TOTP secrets and MACs recovery codes; without it edged refuses Admin Console sign-in.
		{"operator_totp_dek.key", "admin console authenticator key", "admin console authenticator sealing key"},
	} {
		path := filepath.Join(secretsDir, k.file)
		if _, err := localkeys.CreateKeyIfAbsent(path); err != nil {
			log.Fatalf("keybootstrap: %s: %v", k.label, err)
		}
		log.Printf("keybootstrap: %s ready at %s", k.ready, path)
	}

	// 3) OTP generation-1 key + DB lifecycle metadata, validated together.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("keybootstrap: db connect: %v", err)
	}
	defer pool.Close()

	res, err := keybootstrap.BootstrapOTP(ctx, pool, secretsDir)
	if err != nil {
		log.Fatalf("keybootstrap: OTP bootstrap FAILED (scd must not start): %v", err)
	}
	log.Printf("keybootstrap: OTP active_generation=%d referenced=%v key_generations=%v created_key=%t inserted_row=%t",
		res.ActiveGeneration, res.Referenced, res.KeyGenerations, res.CreatedKey, res.InsertedRow)
	log.Printf("keybootstrap: OK — key material and DB metadata agree; scd may start")
}

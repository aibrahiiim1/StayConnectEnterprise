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

	// 2) Voucher code-encryption key (DEK). Production locks IAM-v2 VOUCHER on, so scd refuses to start without it and names this tool.
	// Runtime is load-only; creation belongs to deployment.
	dekPath := filepath.Join(secretsDir, "voucher_dek.key")
	if _, err := localkeys.CreateKeyIfAbsent(dekPath); err != nil {
		log.Fatalf("keybootstrap: voucher DEK: %v", err)
	}
	log.Printf("keybootstrap: voucher code-encryption key ready at %s", dekPath)

	// 2b) Guest sign-in attempt DEK: seals the credential half of iam_v2.sign_in_attempts. Its absence must not cost a guest their internet:
	// scd records attempts with those columns NULL, logs loudly and keeps authenticating. Creation stays here because a runtime-minted key would orphan sealed rows.
	attemptsDEKPath := filepath.Join(secretsDir, "signin_attempts_dek.key")
	if _, err := localkeys.CreateKeyIfAbsent(attemptsDEKPath); err != nil {
		log.Fatalf("keybootstrap: sign-in attempt DEK: %v", err)
	}
	log.Printf("keybootstrap: sign-in attempt sealing key ready at %s", attemptsDEKPath)

	// 2c) Card payment key: seals provider credentials in iam_v2.payment_provider_secret_generations. Absence only makes card payment "not ready";
	// load-only at runtime (a runtime-minted key would orphan sealed rows).
	paymentKeyPath := filepath.Join(secretsDir, "payment_dek.key")
	if _, err := localkeys.CreateKeyIfAbsent(paymentKeyPath); err != nil {
		log.Fatalf("keybootstrap: payment key: %v", err)
	}
	log.Printf("keybootstrap: card payment sealing key ready at %s", paymentKeyPath)

	// 2d) Anonymous-access key: keys the HMACs of the resume tokens and recovery codes of clients who choose a
	// package without signing in. Without it open selection is unavailable; nothing else is affected.
	anonPath := filepath.Join(secretsDir, "anonymous_access.key")
	if _, err := localkeys.CreateKeyIfAbsent(anonPath); err != nil {
		log.Fatalf("keybootstrap: anonymous-access key: %v", err)
	}
	log.Printf("keybootstrap: anonymous-access key ready at %s", anonPath)

	// 2e) Notification-secret key: seals SMTP passwords, API keys and access tokens of email, SMS and WhatsApp senders.
	// Without it a sealed sender cannot send; load-only at runtime.
	notifyPath := filepath.Join(secretsDir, "notify_dek.key")
	if _, err := localkeys.CreateKeyIfAbsent(notifyPath); err != nil {
		log.Fatalf("keybootstrap: notification-secret key: %v", err)
	}
	log.Printf("keybootstrap: notification-secret sealing key ready at %s", notifyPath)

	// 2f) Social sign-in key: seals client secrets (and Apple .p8 keys) of the hotel's social apps. Without it scd builds no social provider
	// and never falls back to a plaintext column; load-only at runtime.
	socialPath := filepath.Join(secretsDir, "social_dek.key")
	if _, err := localkeys.CreateKeyIfAbsent(socialPath); err != nil {
		log.Fatalf("keybootstrap: social sign-in key: %v", err)
	}
	log.Printf("keybootstrap: social sign-in sealing key ready at %s", socialPath)

	// 2g) Admin Console authenticator key: seals each operator's TOTP secret and MACs recovery codes (public.operator_mfa*).
	// Without it scd checks no code and edged refuses Admin Console sign-in; replacing it forces every operator to re-enrol.
	mfaPath := filepath.Join(secretsDir, "operator_totp_dek.key")
	if _, err := localkeys.CreateKeyIfAbsent(mfaPath); err != nil {
		log.Fatalf("keybootstrap: admin console authenticator key: %v", err)
	}
	log.Printf("keybootstrap: admin console authenticator sealing key ready at %s", mfaPath)

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

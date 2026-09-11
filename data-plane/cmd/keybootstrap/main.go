// keybootstrap — deployment-time bootstrap of appliance-local HMAC secret material and its database
// lifecycle metadata for the durable throttle (D4) and keyed-HMAC OTP (D7).
//
// Run ONCE per appliance during deployment, BEFORE scd starts, using a privileged (migration/
// operational) database role — never svc_scd. It is idempotent and fail-closed: it creates the
// throttle key and OTP generation-1 key if absent, records generation 1 active, and refuses to start
// scd unless key material and DB metadata agree. It prints only non-secret status.
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

	// 2) Voucher code-encryption key (DEK). On a production build IAM-v2 VOUCHER is locked ON, so scd
	// refuses to start without this key and its own error names this tool: "run keybootstrap at deploy".
	// It was not created here, so a factory-clean production appliance crash-looped on first boot with a
	// message pointing at a step that did not do it. Runtime is load-only by design; creation belongs at
	// deployment, which is here.
	dekPath := filepath.Join(secretsDir, "voucher_dek.key")
	if _, err := localkeys.CreateKeyIfAbsent(dekPath); err != nil {
		log.Fatalf("keybootstrap: voucher DEK: %v", err)
	}
	log.Printf("keybootstrap: voucher code-encryption key ready at %s", dekPath)

	// 2b) Guest sign-in attempt DEK. It seals the credential half of iam_v2.sign_in_attempts: what a guest
	// typed, and the values the mirror would have accepted.
	//
	// ITS ABSENCE MUST NOT COST A GUEST THEIR INTERNET, which is why scd treats this key differently from the
	// voucher DEK. A missing voucher key means scd cannot issue a credential at all and refusing to start is
	// correct. A missing key here means only that the DIAGNOSTIC half of an attempt record cannot be sealed,
	// so scd records the attempt with those columns NULL, logs it loudly, and keeps authenticating. Creating
	// it is still deployment's job — runtime is load-only, and a service that could mint its own key would
	// silently orphan every row sealed under the previous one.
	attemptsDEKPath := filepath.Join(secretsDir, "signin_attempts_dek.key")
	if _, err := localkeys.CreateKeyIfAbsent(attemptsDEKPath); err != nil {
		log.Fatalf("keybootstrap: sign-in attempt DEK: %v", err)
	}
	log.Printf("keybootstrap: sign-in attempt sealing key ready at %s", attemptsDEKPath)

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

package keybootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
)

// otpDB connects the disposable OTP-metadata database (public.otp_hmac_key_generations + auth_otps
// with 0008 applied). Set OTPBOOT_TEST_DSN to run; skipped otherwise.
func otpDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OTPBOOT_TEST_DSN")
	if dsn == "" {
		t.Skip("OTPBOOT_TEST_DSN not set; skipping OTP bootstrap integration")
	}
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// clean state each test
	if _, err := db.Exec(context.Background(),
		`TRUNCATE public.auth_otps; DELETE FROM public.otp_hmac_key_generations;`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	return db
}

func countActive(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM public.otp_hmac_key_generations WHERE active`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBootstrapOTPFreshCreatesGenerationOne(t *testing.T) {
	db := otpDB(t)
	dir := t.TempDir()
	ctx := context.Background()

	res, err := BootstrapOTP(ctx, db, dir)
	if err != nil {
		t.Fatalf("fresh bootstrap: %v", err)
	}
	if !res.CreatedKey || !res.InsertedRow || res.ActiveGeneration != 1 {
		t.Fatalf("expected created gen-1 key+row active=1, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "otp_hmac_1.key")); err != nil {
		t.Fatalf("gen-1 key file missing: %v", err)
	}
	if countActive(t, db) != 1 {
		t.Fatalf("want exactly 1 active, got %d", countActive(t, db))
	}
	// idempotent re-run: no new key, no new row, still one active gen 1
	res2, err := BootstrapOTP(ctx, db, dir)
	if err != nil {
		t.Fatalf("idempotent re-run: %v", err)
	}
	if res2.CreatedKey || res2.InsertedRow || res2.ActiveGeneration != 1 {
		t.Fatalf("re-run must be a no-op, got %+v", res2)
	}
	if countActive(t, db) != 1 {
		t.Fatal("re-run changed active count")
	}
}

func TestBootstrapOTPDBMetaButNoKeyFails(t *testing.T) {
	db := otpDB(t)
	dir := t.TempDir() // empty: no key files
	ctx := context.Background()
	// DB says generation 1 is active, but the key file is absent -> must fail closed, never fabricate.
	if _, err := db.Exec(ctx,
		`INSERT INTO public.otp_hmac_key_generations (generation, active) VALUES (1, true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := BootstrapOTP(ctx, db, dir); err == nil {
		t.Fatal("DB metadata with missing key must fail closed")
	}
}

func TestBootstrapOTPMoreThanOneActiveFails(t *testing.T) {
	db := otpDB(t)
	dir := t.TempDir()
	ctx := context.Background()
	// Force two active rows by dropping the guard index only for this insert, then restore.
	if _, err := db.Exec(ctx, `DROP INDEX IF EXISTS public.otp_hmac_key_generations_one_active`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO public.otp_hmac_key_generations (generation, active) VALUES (1,true),(2,true)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(context.Background(), `DELETE FROM public.otp_hmac_key_generations`)
		db.Exec(context.Background(),
			`CREATE UNIQUE INDEX IF NOT EXISTS otp_hmac_key_generations_one_active ON public.otp_hmac_key_generations ((active)) WHERE active`)
	})
	if _, err := BootstrapOTP(ctx, db, dir); err == nil {
		t.Fatal(">1 active generation must fail")
	}
}

func TestBootstrapOTPKeyPresentNoDBReconciles(t *testing.T) {
	db := otpDB(t)
	dir := t.TempDir()
	ctx := context.Background()
	// A lone generation-1 key exists but DB has no generation -> controlled reconcile inserts gen 1.
	if _, err := localkeys.EnsureGeneration(dir, OTPPrefix, 1); err != nil {
		t.Fatal(err)
	}
	res, err := BootstrapOTP(ctx, db, dir)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.CreatedKey || !res.InsertedRow || res.ActiveGeneration != 1 {
		t.Fatalf("expected reconcile insert of gen 1 (no new key), got %+v", res)
	}
	if countActive(t, db) != 1 {
		t.Fatal("reconcile must leave exactly one active")
	}
}

func TestBootstrapOTPAmbiguousKeysNoDBFails(t *testing.T) {
	db := otpDB(t)
	dir := t.TempDir()
	ctx := context.Background()
	// Two key files, no DB rows -> ambiguous, refuse to reconcile.
	if _, err := localkeys.EnsureGeneration(dir, OTPPrefix, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := localkeys.EnsureGeneration(dir, OTPPrefix, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := BootstrapOTP(ctx, db, dir); err == nil {
		t.Fatal("multiple key files with empty DB must refuse ambiguous reconcile")
	}
}

func TestBootstrapOTPReferencedGenMissingKeyFails(t *testing.T) {
	db := otpDB(t)
	dir := t.TempDir()
	ctx := context.Background()
	// gen 1 bootstrapped normally...
	if _, err := BootstrapOTP(ctx, db, dir); err != nil {
		t.Fatalf("initial bootstrap: %v", err)
	}
	// ...then a gen-2 metadata row exists (retired, inactive) and an UNEXPIRED OTP pins gen 2, but no
	// gen-2 key file: those OTPs could never verify -> bootstrap must fail closed.
	if _, err := db.Exec(ctx,
		`INSERT INTO public.otp_hmac_key_generations (generation, active, retired_at) VALUES (2, false, now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO public.auth_otps (otp_key_generation, expires_at) VALUES (2, now() + interval '5 min')`); err != nil {
		t.Fatal(err)
	}
	if _, err := BootstrapOTP(ctx, db, dir); err == nil {
		t.Fatal("referenced-but-keyless generation must fail closed")
	}
}

package otp

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/otpkey"
)

// Needs OTP_TEST_DSN.
func otpDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OTP_TEST_DSN")
	if dsn == "" {
		t.Skip("OTP_TEST_DSN not set; skipping keyed-OTP integration")
	}
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(context.Background(), `TRUNCATE auth_otps CASCADE`); err != nil {
		t.Fatalf("reset (schema applied?): %v", err)
	}
	// The full schema requires the tenant and the active key generation the rows reference.
	if _, err := db.Exec(context.Background(), `INSERT INTO tenants (id, slug, name) VALUES ($1::uuid, 'otp-test', 'OTP test')
	     ON CONFLICT (id) DO NOTHING`, otpTenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO otp_hmac_key_generations (generation, active) VALUES (1, true)
	     ON CONFLICT (generation) DO NOTHING`); err != nil {
		t.Fatalf("seed key generation: %v", err)
	}
	return db
}

func testRing(t *testing.T) *otpkey.Ring {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	r, err := otpkey.NewRing(map[int][]byte{1: k}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const (
	otpTenant = "11111111-1111-1111-1111-111111111111"
)

func TestKeyedIssueThenVerify(t *testing.T) {
	db := otpDB(t)
	ring := testRing(t)
	ctx := context.Background()

	iss, err := Issue(ctx, db, ring, IssueParams{TenantID: otpTenant, Channel: "email", Destination: "guest@example.com"})
	if err != nil {
		t.Fatalf("keyed issue: %v", err)
	}
	var gen *int
	var stored string
	db.QueryRow(ctx, `SELECT otp_key_generation, code_hash FROM auth_otps WHERE id=$1`, iss.ChallengeID).Scan(&gen, &stored)
	if gen == nil || *gen != 1 {
		t.Fatalf("expected pinned generation 1, got %v", gen)
	}
	if strings.Contains(stored, ":") || len(stored) != 64 {
		t.Fatalf("keyed digest must be a bare 64-hex HMAC, got %q", stored)
	}
	if _, err := verify(ctx, db, ring, iss.ChallengeID, iss.Code); err != nil {
		t.Fatalf("keyed verify: %v", err)
	}
	if _, err := verify(ctx, db, ring, iss.ChallengeID, iss.Code); err != ErrAlreadyUsed {
		t.Fatalf("second verify must be ErrAlreadyUsed, got %v", err)
	}
}

func TestKeyedWrongCodeIncrementsAttemptsThenLocks(t *testing.T) {
	db := otpDB(t)
	ring := testRing(t)
	ctx := context.Background()
	iss, err := Issue(ctx, db, ring, IssueParams{TenantID: otpTenant, Channel: "sms", Destination: "+15551230000"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < DefaultMaxAttempts; i++ {
		if _, err := verify(ctx, db, ring, iss.ChallengeID, "000000"); err != ErrCodeMismatch {
			t.Fatalf("wrong attempt %d: want ErrCodeMismatch, got %v", i, err)
		}
	}
	var attempts int
	db.QueryRow(ctx, `SELECT attempts FROM auth_otps WHERE id=$1`, iss.ChallengeID).Scan(&attempts)
	if attempts != DefaultMaxAttempts {
		t.Fatalf("attempts=%d want %d", attempts, DefaultMaxAttempts)
	}
	if _, err := verify(ctx, db, ring, iss.ChallengeID, iss.Code); err != ErrAttemptsExceeded {
		t.Fatalf("after cap: want ErrAttemptsExceeded, got %v", err)
	}
}

func TestLegacyRowVerifiesUnderRing(t *testing.T) {
	db := otpDB(t)
	ring := testRing(t)
	ctx := context.Background()
	code := "246810"
	salt := randHex(8)
	stored := salt + ":" + hashOTP(salt, code)
	var id string
	if err := db.QueryRow(ctx,
		`INSERT INTO auth_otps (tenant_id, channel, destination, code_hash, expires_at, max_attempts)
		 VALUES ($1,'email','legacy@example.com',$2, now()+interval '10 min', 5) RETURNING id`,
		otpTenant, stored).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := verify(ctx, db, ring, id, code); err != nil {
		t.Fatalf("legacy row must verify under ring (compat window): %v", err)
	}
}

func TestLegacyIssueUnchangedWhenNoRing(t *testing.T) {
	db := otpDB(t)
	ctx := context.Background()
	iss, err := Issue(ctx, db, nil, IssueParams{TenantID: otpTenant, Channel: "email", Destination: "nolring@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var gen *int
	var stored string
	db.QueryRow(ctx, `SELECT otp_key_generation, code_hash FROM auth_otps WHERE id=$1`, iss.ChallengeID).Scan(&gen, &stored)
	if gen != nil {
		t.Fatalf("legacy issue must leave generation NULL, got %v", *gen)
	}
	if !strings.Contains(stored, ":") {
		t.Fatalf("legacy issue must store salt:sha256, got %q", stored)
	}
	if _, err := verify(ctx, db, nil, iss.ChallengeID, iss.Code); err != nil {
		t.Fatalf("legacy verify: %v", err)
	}
}

func TestKeyedConcurrentSingleWinner(t *testing.T) {
	db := otpDB(t)
	ring := testRing(t)
	ctx := context.Background()
	iss, err := Issue(ctx, db, ring, IssueParams{TenantID: otpTenant, Channel: "email", Destination: "race@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(ctx, `UPDATE auth_otps SET max_attempts=100 WHERE id=$1`, iss.ChallengeID)

	const n = 20
	var wins int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := verify(ctx, db, ring, iss.ChallengeID, iss.Code); err == nil {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("single-consume violated: %d winners, want 1", wins)
	}
	var consumed *time.Time
	db.QueryRow(ctx, `SELECT consumed_at FROM auth_otps WHERE id=$1`, iss.ChallengeID).Scan(&consumed)
	if consumed == nil {
		t.Fatal("winning verify must consume the row")
	}
}

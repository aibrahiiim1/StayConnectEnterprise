package throttle

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---- unit (no DB) ----------------------------------------------------------

func TestScopeKeyIrreversibleAndStable(t *testing.T) {
	s := &Store{key: []byte("k"), window: time.Minute, now: time.Now}
	hexRe := regexp.MustCompile(`^[0-9a-f]{64}$`)
	a := s.scopeKey(ScopeIdentity, "alice@example.com")
	b := s.scopeKey(ScopeIdentity, "alice@example.com")
	c := s.scopeKey(ScopeIdentity, "bob@example.com")
	d := s.scopeKey(ScopeIP, "alice@example.com")
	if !hexRe.MatchString(a) {
		t.Fatalf("scope key not 64-hex: %q", a)
	}
	if a != b {
		t.Fatal("scope key not stable for same input")
	}
	if a == c {
		t.Fatal("scope key collided across identities")
	}
	if a == d {
		t.Fatal("scope key must differ across scope kinds")
	}
	if strings.Contains(a, "alice") {
		t.Fatal("raw identity leaked into scope key")
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(nil, []byte("k"), time.Minute); err == nil {
		t.Fatal("expected error on nil db")
	}
}

// ---- integration (DB) ------------------------------------------------------

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("THROTTLE_TEST_DSN")
	if dsn == "" {
		t.Skip("THROTTLE_TEST_DSN not set; skipping DB integration test")
	}
	db, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// isolate each test run
	if _, err := db.Exec(context.Background(), `TRUNCATE public.auth_throttle_buckets`); err != nil {
		t.Fatalf("truncate (is 0007 applied?): %v", err)
	}
	return db
}

func TestAllowUntilLimitThenDeny(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, []byte("key"), time.Minute)
	ctx := context.Background()
	rule := Rule{Kind: ScopeIdentity, Value: "u1", Limit: 3}
	for i := 1; i <= 3; i++ {
		d, err := s.Allow(ctx, rule)
		if err != nil || !d.Allowed {
			t.Fatalf("attempt %d should be allowed: %+v err=%v", i, d, err)
		}
	}
	d, err := s.Allow(ctx, rule)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed {
		t.Fatal("4th attempt over limit=3 should be denied")
	}
	if d.Reason != ScopeIdentity || d.RetryAfter <= 0 {
		t.Fatalf("expected identity denial with retry-after, got %+v", d)
	}
}

func TestConcurrentNoBypass(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, []byte("key"), time.Minute)
	ctx := context.Background()
	const limit, workers = 5, 40
	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.Allow(ctx, Rule{Kind: ScopeIP, Value: "10.0.0.9", Limit: limit})
			if err == nil && d.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()
	if allowed != limit {
		t.Fatalf("concurrency bypass: %d allowed, want exactly %d", allowed, limit)
	}
}

func TestBlockedUntilHardBlock(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, []byte("key"), time.Minute)
	ctx := context.Background()
	rule := Rule{Kind: ScopeIdentity, Value: "blockme", Limit: 1, Block: time.Hour}
	if d, _ := s.Allow(ctx, rule); !d.Allowed {
		t.Fatal("first should pass")
	}
	if d, _ := s.Allow(ctx, rule); d.Allowed {
		t.Fatal("second exceeds limit -> deny + block")
	}
	// even a brand-new Store (simulated restart) sees the durable block
	s2, _ := New(db, []byte("key"), time.Minute)
	d, _ := s2.Allow(ctx, rule)
	if d.Allowed || d.RetryAfter < 30*time.Minute {
		t.Fatalf("hard block must persist across restart: %+v", d)
	}
}

func TestRestartPersistenceAndCleanup(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	s, _ := New(db, []byte("key"), time.Minute)
	for i := 0; i < 2; i++ {
		_, _ = s.Allow(ctx, Rule{Kind: ScopeMethod, Value: "voucher", Limit: 100})
	}
	// new Store sees the accumulated count (durable)
	var cnt int
	if err := db.QueryRow(ctx, `SELECT attempt_count FROM public.auth_throttle_buckets WHERE scope_kind='method'`).Scan(&cnt); err != nil || cnt != 2 {
		t.Fatalf("expected durable count 2, got %d err=%v", cnt, err)
	}
	// cleanup does not delete a live bucket
	n, err := s.Cleanup(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("cleanup removed a live bucket (%d)", n)
	}
	// backdate the bucket well past window+grace -> cleanup removes it
	if _, err := db.Exec(ctx, `UPDATE public.auth_throttle_buckets SET window_start = now() - interval '2 hours', blocked_until = NULL`); err != nil {
		t.Fatal(err)
	}
	n, err = s.Cleanup(ctx, time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("cleanup should remove 1 expired bucket, got %d err=%v", n, err)
	}
}

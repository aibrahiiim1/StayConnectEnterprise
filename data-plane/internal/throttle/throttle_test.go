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

var tkey = []byte("0123456789abcdef0123456789abcdef") // 32-byte test key

// ---- unit (no DB) ----------------------------------------------------------

func TestScopeKeyIrreversibleAndStable(t *testing.T) {
	s := &Store{key: tkey, window: time.Minute, now: time.Now}
	hexRe := regexp.MustCompile(`^[0-9a-f]{64}$`)
	a := s.scopeKey(ScopeIdentity, "alice@example.com")
	b := s.scopeKey(ScopeIdentity, "alice@example.com")
	c := s.scopeKey(ScopeIdentity, "bob@example.com")
	d := s.scopeKey(ScopeIP, "alice@example.com")
	if !hexRe.MatchString(a) || a != b || a == c || a == d || strings.Contains(a, "alice") {
		t.Fatalf("scope key invariants broken: %q", a)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(nil, tkey, time.Minute); err == nil {
		t.Fatal("nil db should error")
	}
	if _, err := New(&pgxpool.Pool{}, []byte("short"), time.Minute); err == nil {
		t.Fatal("short key should error")
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
	if _, err := db.Exec(context.Background(), `TRUNCATE public.auth_throttle_buckets`); err != nil {
		t.Fatalf("truncate (is 0007 applied?): %v", err)
	}
	return db
}

func TestAllowUntilLimitThenDeny(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	rule := Rule{Kind: ScopeIdentity, Value: "u1", Method: "account", Limit: 3}
	for i := 1; i <= 3; i++ {
		if d, err := s.Allow(ctx, rule); err != nil || !d.Allowed {
			t.Fatalf("attempt %d should pass: %+v err=%v", i, d, err)
		}
	}
	if d, _ := s.Allow(ctx, rule); d.Allowed {
		t.Fatal("4th over limit=3 should deny")
	}
}

func TestEmptyMethodRejected(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	if _, err := s.Allow(context.Background(), Rule{Kind: ScopeIdentity, Value: "x", Method: "", Limit: 5}); err == nil {
		t.Fatal("empty method must be rejected (use MethodAny explicitly for a global policy)")
	}
	if _, err := s.Allow(context.Background(), Rule{Kind: ScopeIdentity, Value: "x", Method: "bogus", Limit: 5}); err == nil {
		t.Fatal("invalid method must be rejected")
	}
}

func TestConcurrentNoBypass(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	const limit, workers = 5, 40
	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d, err := s.Allow(ctx, Rule{Kind: ScopeIP, Value: "10.0.0.9", Method: "otp", Limit: limit}); err == nil && d.Allowed {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()
	if allowed != limit {
		t.Fatalf("concurrency bypass: %d allowed, want %d", allowed, limit)
	}
}

// TestHardBlockAcrossWindows: 1-minute window, 1-hour block; advancing the injected clock past
// several window boundaries keeps the attempt denied until blocked_until; then it resumes.
func TestHardBlockAcrossWindows(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	base := time.Date(2026, 7, 17, 12, 0, 5, 0, time.UTC)
	clk := base
	s.SetClock(func() time.Time { return clk })
	rule := Rule{Kind: ScopeIdentity, Value: "blockme", Method: "account", Limit: 1, Block: time.Hour}

	if d, _ := s.Allow(ctx, rule); !d.Allowed {
		t.Fatal("1st should pass")
	}
	if d, _ := s.Allow(ctx, rule); d.Allowed {
		t.Fatal("2nd exceeds limit -> deny + 1h block")
	}
	// advance well past the 1-minute window, several times; block must persist
	for _, adv := range []time.Duration{2 * time.Minute, 10 * time.Minute, 45 * time.Minute} {
		clk = base.Add(adv)
		d, _ := s.Allow(ctx, rule)
		if d.Allowed {
			t.Fatalf("attempt at +%v must remain blocked (block is 1h)", adv)
		}
	}
	// a brand-new Store (simulated restart) with the SAME key still sees the durable block
	s2, _ := New(db, tkey, time.Minute)
	s2.SetClock(func() time.Time { return base.Add(30 * time.Minute) })
	if d, _ := s2.Allow(ctx, rule); d.Allowed {
		t.Fatal("hard block must survive restart")
	}
	// after blocked_until, attempts resume in a fresh window
	clk = base.Add(61 * time.Minute)
	if d, _ := s.Allow(ctx, rule); !d.Allowed {
		t.Fatal("after block expiry, attempts must resume")
	}
}

// TestMethodIsolation: the same identity under different methods does not share a counter.
func TestMethodIsolation(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	acct := Rule{Kind: ScopeIdentity, Value: "shared", Method: "account", Limit: 2}
	otp := Rule{Kind: ScopeIdentity, Value: "shared", Method: "otp", Limit: 2}
	// exhaust the account method
	s.Allow(ctx, acct)
	s.Allow(ctx, acct)
	if d, _ := s.Allow(ctx, acct); d.Allowed {
		t.Fatal("account method should be over limit")
	}
	// otp method for the same identity is unaffected
	if d, _ := s.Allow(ctx, otp); !d.Allowed {
		t.Fatal("otp method must have its own independent counter")
	}
}

// TestGlobalMethodShared: MethodAny ("*") is deliberately shared across methods (e.g. endpoint damper).
func TestGlobalMethodShared(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	r := func() Rule { return Rule{Kind: ScopeEndpoint, Value: "", Method: MethodAny, Limit: 2} }
	s.Allow(ctx, r())
	s.Allow(ctx, r())
	if d, _ := s.Allow(ctx, r()); d.Allowed {
		t.Fatal("endpoint '*' damper must be shared/global and hit its cap")
	}
}

func TestCleanupNeverRemovesActiveBlock(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	base := time.Date(2026, 7, 17, 12, 0, 5, 0, time.UTC)
	s.SetClock(func() time.Time { return base })
	rule := Rule{Kind: ScopeIP, Value: "1.2.3.4", Method: "voucher", Limit: 1, Block: time.Hour}
	s.Allow(ctx, rule)
	s.Allow(ctx, rule) // triggers 1h block
	// advance past window+grace but the block is still active -> cleanup must NOT delete it
	s.SetClock(func() time.Time { return base.Add(30 * time.Minute) })
	if n, err := s.Cleanup(ctx, time.Minute); err != nil || n != 0 {
		t.Fatalf("cleanup removed an active block (n=%d err=%v)", n, err)
	}
	// advance past the block + grace -> cleanup removes it
	s.SetClock(func() time.Time { return base.Add(2 * time.Hour) })
	if n, err := s.Cleanup(ctx, time.Minute); err != nil || n != 1 {
		t.Fatalf("cleanup should remove expired block (n=%d err=%v)", n, err)
	}
}

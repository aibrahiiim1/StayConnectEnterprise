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

var tkey = []byte("0123456789abcdef0123456789abcdef")

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

// Callers pass the same rules in rotated order; Allow's sort keeps lock order global so none deadlock.
func TestMixedRuleOrderNoDeadlock(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	const workers, ipLimit = 30, 8
	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rules := []Rule{
				{Kind: ScopeIP, Value: "10.9.9.9", Method: "account", Limit: ipLimit},
				{Kind: ScopeIdentity, Value: "id-mix", Method: "account", Limit: 1000},
				{Kind: ScopeDevice, Value: "dev-mix", Method: "account", Limit: 1000},
			}
			shift := i % 3
			rules = append(rules[shift:], rules[:shift]...)
			if d, err := s.Allow(ctx, rules...); err == nil && d.Allowed {
				atomic.AddInt64(&allowed, 1)
			} else if err != nil {
				t.Errorf("worker %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if allowed != ipLimit {
		t.Fatalf("mixed-order concurrency: %d allowed, want exactly %d (IP cap)", allowed, ipLimit)
	}
}

func TestDuplicateRuleChargedOnce(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	// The same identity listed three times merges to the tightest limit, 2.
	dup := func() []Rule {
		return []Rule{
			{Kind: ScopeIdentity, Value: "dupe", Method: "voucher", Limit: 5},
			{Kind: ScopeIdentity, Value: "dupe", Method: "voucher", Limit: 2},
			{Kind: ScopeIdentity, Value: "dupe", Method: "voucher", Limit: 9},
		}
	}
	for i := 1; i <= 2; i++ {
		if d, err := s.Allow(ctx, dup()...); err != nil || !d.Allowed {
			t.Fatalf("attempt %d should pass (charged once, tightest limit=2): %+v err=%v", i, d, err)
		}
	}
	if d, _ := s.Allow(ctx, dup()...); d.Allowed {
		t.Fatal("3rd attempt must deny (dedup means one charge/attempt, tightest limit=2)")
	}
	var rows int
	db.QueryRow(ctx, `SELECT count(*) FROM public.auth_throttle_buckets WHERE scope_kind='identity' AND method='voucher'`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("dedup must produce one bucket row, got %d", rows)
	}
}

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
	for _, adv := range []time.Duration{2 * time.Minute, 10 * time.Minute, 45 * time.Minute} {
		clk = base.Add(adv)
		d, _ := s.Allow(ctx, rule)
		if d.Allowed {
			t.Fatalf("attempt at +%v must remain blocked (block is 1h)", adv)
		}
	}
	// A new Store with the same key simulates a restart.
	s2, _ := New(db, tkey, time.Minute)
	s2.SetClock(func() time.Time { return base.Add(30 * time.Minute) })
	if d, _ := s2.Allow(ctx, rule); d.Allowed {
		t.Fatal("hard block must survive restart")
	}
	clk = base.Add(61 * time.Minute)
	if d, _ := s.Allow(ctx, rule); !d.Allowed {
		t.Fatal("after block expiry, attempts must resume")
	}
}

func TestMethodIsolation(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	acct := Rule{Kind: ScopeIdentity, Value: "shared", Method: "account", Limit: 2}
	otp := Rule{Kind: ScopeIdentity, Value: "shared", Method: "otp", Limit: 2}
	s.Allow(ctx, acct)
	s.Allow(ctx, acct)
	if d, _ := s.Allow(ctx, acct); d.Allowed {
		t.Fatal("account method should be over limit")
	}
	if d, _ := s.Allow(ctx, otp); !d.Allowed {
		t.Fatal("otp method must have its own independent counter")
	}
}

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
	s.Allow(ctx, rule)
	s.SetClock(func() time.Time { return base.Add(30 * time.Minute) })
	if n, err := s.Cleanup(ctx, time.Minute); err != nil || n != 0 {
		t.Fatalf("cleanup removed an active block (n=%d err=%v)", n, err)
	}
	s.SetClock(func() time.Time { return base.Add(2 * time.Hour) })
	if n, err := s.Cleanup(ctx, time.Minute); err != nil || n != 1 {
		t.Fatalf("cleanup should remove expired block (n=%d err=%v)", n, err)
	}
}

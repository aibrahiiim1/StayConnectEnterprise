package api

import (
	"context"
	"os"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Audit Phase 1 M1: every counted key carries a time-to-live, including one an earlier failed EXPIRE left
// without one.
func TestRedisRateCounterAlwaysLeavesATTL(t *testing.T) {
	url := os.Getenv("CTRLAPI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("CTRLAPI_TEST_REDIS_URL is not set: this test needs a Redis")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	ctx := context.Background()
	c := RedisRateCounter(rdb)
	fresh, stuck := "rl:test:fresh:"+t.Name(), "rl:test:stuck:"+t.Name()
	defer rdb.Del(ctx, fresh, stuck)
	rdb.Del(ctx, fresh, stuck)

	if n, err := c.Incr(ctx, fresh, time.Minute); err != nil || n != 1 {
		t.Fatalf("first hit = %d, %v", n, err)
	}
	if ttl := rdb.TTL(ctx, fresh).Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("a new key must expire within the window, ttl = %v", ttl)
	}
	before := rdb.TTL(ctx, fresh).Val()
	if n, _ := c.Incr(ctx, fresh, time.Hour); n != 2 {
		t.Fatalf("second hit = %d", n)
	}
	if ttl := rdb.TTL(ctx, fresh).Val(); ttl > before {
		t.Fatalf("a later hit must not extend the window: %v > %v", ttl, before)
	}

	// A key left without a TTL (EXPIRE lost after INCR) is repaired on the next hit.
	rdb.Set(ctx, stuck, 99, 0)
	if _, err := c.Incr(ctx, stuck, time.Minute); err != nil {
		t.Fatal(err)
	}
	if ttl := rdb.TTL(ctx, stuck).Val(); ttl <= 0 {
		t.Fatalf("a key without a TTL must get one, ttl = %v", ttl)
	}
}

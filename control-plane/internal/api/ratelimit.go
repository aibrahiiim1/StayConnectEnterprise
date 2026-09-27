package api

import (
	"context"
	"net/http"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
)

// RateLimit is a fixed-window per-client-IP limiter backed by Redis. It protects the unauthenticated and
// password endpoints: operator login and step-up re-authentication, appliance registration, and the
// appliance assignment channel.
//
// THE KEY IS THE SERVER-DERIVED CLIENT ADDRESS (clientip.From): the TCP peer, or — only when the peer is the
// loopback reverse proxy — the X-Real-IP it sets, else the last X-Forwarded-For hop it appended. A client
// talking to ctrlapi directly cannot pick its bucket by sending forwarding headers.
//
// Fail-open: if Redis is unreachable the request is allowed (availability over strict throttling).
//
// prefix namespaces the counter (e.g. "login"); limit is the max requests permitted per window per address.
func RateLimit(rdb *redis.Client, prefix string, limit int, window time.Duration) func(http.Handler) http.Handler {
	if rdb == nil {
		return RateLimitWith(nil, prefix, limit, window)
	}
	return RateLimitWith(redisCounter{rdb}, prefix, limit, window)
}

// RateCounter is the one thing the limiter needs from its store (Redis in production).
type RateCounter interface {
	// Incr increments key, starting a window of the given length when the key is new, and returns the count.
	Incr(ctx context.Context, key string, window time.Duration) (int64, error)
}

// RedisRateCounter counts in Redis with fixed windows.
func RedisRateCounter(rdb *redis.Client) RateCounter { return redisCounter{rdb} }

type redisCounter struct{ rdb *redis.Client }

func (c redisCounter) Incr(ctx context.Context, key string, window time.Duration) (int64, error) {
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		_ = c.rdb.Expire(ctx, key, window).Err()
	}
	return n, nil
}

// rateLimitKey is the counter key for a request: prefix + the trusted client address.
func rateLimitKey(prefix string, r *http.Request) string {
	return "rl:" + prefix + ":" + clientip.From(r)
}

// RateLimitWith is RateLimit over any counter; a nil counter disables limiting.
func RateLimitWith(store RateCounter, prefix string, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil {
				next.ServeHTTP(w, r)
				return
			}
			n, err := store.Incr(r.Context(), rateLimitKey(prefix, r), window)
			if err != nil {
				next.ServeHTTP(w, r) // fail-open
				return
			}
			if n > int64(limit) {
				w.Header().Set("Retry-After", "60")
				Fail(w, r, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Wait a minute and try again.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

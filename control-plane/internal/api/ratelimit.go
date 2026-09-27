package api

import (
	"net/http"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
)

// RateLimit is a fixed-window per-client-IP limiter backed by Redis. The client IP comes from
// clientip.From, which trusts forwarding headers only from the loopback proxy (F13). It
// protects unauthenticated public endpoints (notably appliance enrollment)
// from brute-force and abuse. Fail-open: if Redis is unreachable the request
// is allowed (availability over strict throttling), but that path is rare.
//
// prefix namespaces the counter (e.g. "enroll"); limit is the max requests
// permitted per window per IP.
func RateLimit(rdb *redis.Client, prefix string, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rdb == nil {
				next.ServeHTTP(w, r)
				return
			}
			ip := clientip.From(r)
			key := "rl:" + prefix + ":" + ip
			ctx := r.Context()
			n, err := rdb.Incr(ctx, key).Result()
			if err != nil {
				next.ServeHTTP(w, r) // fail-open
				return
			}
			if n == 1 {
				_ = rdb.Expire(ctx, key, window).Err()
			}
			if n > int64(limit) {
				w.Header().Set("Retry-After", "60")
				Fail(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

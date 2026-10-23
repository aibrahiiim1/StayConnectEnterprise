package api

import (
	"context"
	"net/http"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// ReauthTTL is how long a password step-up stays valid for sensitive actions.
const ReauthTTL = 5 * time.Minute

func reauthKey(sessionToken string) string { return "reauth:" + sessionToken }

func MarkReauth(rdb *redis.Client, sessionToken string) {
	if rdb == nil || sessionToken == "" {
		return
	}
	_ = rdb.Set(context.Background(), reauthKey(sessionToken), "1", ReauthTTL).Err()
}

// RequireReauth demands a recent password re-authentication (within ReauthTTL) in addition to the permission check.
func RequireReauth(rdb *redis.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rdb == nil {
				// Fail closed: reauth is a security control, not optional.
				Fail(w, r, http.StatusForbidden, "reauth_required", "password re-authentication required")
				return
			}
			c, err := r.Cookie(auth.SessionCookieName)
			if err != nil || c.Value == "" {
				Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "login required")
				return
			}
			n, err := rdb.Exists(r.Context(), reauthKey(c.Value)).Result()
			if err != nil || n == 0 {
				Fail(w, r, http.StatusForbidden, "reauth_required", "password re-authentication required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

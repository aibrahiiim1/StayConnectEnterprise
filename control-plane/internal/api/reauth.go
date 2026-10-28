package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// ReauthTTL is how long a password step-up stays valid for sensitive actions.
const ReauthTTL = 5 * time.Minute

func reauthKey(sessionToken string) string { return "reauth:" + sessionToken }

// MarkReauth records a successful step-up. Its error is returned: answering ok without the mark would leave the
// operator refused on the next sensitive action with no explanation (audit Phase 1 L2).
func MarkReauth(ctx context.Context, rdb *redis.Client, sessionToken string) error {
	if rdb == nil {
		return errors.New("step-up store unavailable")
	}
	if sessionToken == "" {
		return errors.New("no session")
	}
	return rdb.Set(ctx, reauthKey(sessionToken), "1", ReauthTTL).Err()
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

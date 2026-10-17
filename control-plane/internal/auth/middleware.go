package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

type ctxKey int

const authCtxKey ctxKey = 1

// FromContext returns the session attached by RequireAuth (or nil).
func FromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(authCtxKey).(*Session)
	return s
}

func withSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, authCtxKey, s)
}

// ContextWithSession attaches a session the way RequireAuth does. For callers that drive a handler without
// the HTTP middleware in front of it (tests, internal jobs).
func ContextWithSession(ctx context.Context, s *Session) context.Context { return withSession(ctx, s) }

// SessionBackend is where sessions live (Redis in production).
type SessionBackend interface {
	Get(ctx context.Context, token string) (*Session, error)
	Destroy(ctx context.Context, token string) error
	// Touch records activity (LastSeenAt) on a session that passed the policy (0052).
	Touch(ctx context.Context, s *Session) error
}

// RequireAuth extracts the sc_session cookie and attaches the Session to the request context.
// Unauthenticated requests get 401.
//
// With a checker, every request is re-authorised against the database (session_policy.go): a session whose
// operator was disabled, demoted, removed or re-passworded, or that is past the absolute lifetime, is
// destroyed and refused, and the roles attached to the request are the operator's CURRENT roles. A database
// failure refuses the request (fail closed) rather than trusting the stored snapshot.
func RequireAuth(store SessionBackend, checker SessionChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(SessionCookieName)
			if err != nil || c.Value == "" {
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "not authenticated", r)
				return
			}
			sess, err := store.Get(r.Context(), c.Value)
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "session lookup failed", r)
				return
			}
			if sess == nil {
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "session expired", r)
				return
			}
			if checker != nil {
				st, err := checker.OperatorState(r.Context(), sess.OperatorID)
				if err != nil {
					jsonErr(w, http.StatusServiceUnavailable, "unavailable", "session check failed; try again", r)
					return
				}
				fresh, why, err := Validate(sess, st, time.Now())
				if err != nil {
					_ = store.Destroy(r.Context(), c.Value)
					jsonErr(w, http.StatusUnauthorized, "unauthenticated", "session ended: "+why, r)
					return
				}
				sess = fresh
			}
			// The console's background whoami poll is not activity: counting it would keep every open tab
			// signed in for ever, and the idle limit would never fire.
			if r.URL.Path != "/v1/auth/whoami" {
				_ = store.Touch(r.Context(), sess)
			}
			next.ServeHTTP(w, r.WithContext(withSession(r.Context(), sess)))
		})
	}
}

// jsonErr writes the standard StayConnect error envelope. Codes here are
// intentionally string-literal (we don't import api to avoid a cycle).
func jsonErr(w http.ResponseWriter, status int, code, msg string, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"error": code, "message": msg}
	if r != nil {
		if tid := middleware.GetReqID(r.Context()); tid != "" {
			body["trace_id"] = tid
		}
	}
	_ = json.NewEncoder(w).Encode(body)
}

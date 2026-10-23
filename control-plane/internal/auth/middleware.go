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

func FromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(authCtxKey).(*Session)
	return s
}

func withSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, authCtxKey, s)
}

// ContextWithSession is for callers that drive a handler without the middleware (tests, internal jobs).
func ContextWithSession(ctx context.Context, s *Session) context.Context { return withSession(ctx, s) }

type SessionBackend interface {
	Get(ctx context.Context, token string) (*Session, error)
	Destroy(ctx context.Context, token string) error
	// Touch records activity (LastSeenAt) on a session that passed the policy.
	Touch(ctx context.Context, s *Session) error
}

// RequireAuth attaches the sc_session Session to the request; unauthenticated requests get 401.
//
// With a checker, every request is re-authorised against the database (session_policy.go): a disabled, demoted,
// removed or re-passworded operator, or a session past the absolute lifetime, is destroyed and refused, and the
// request carries the operator's current roles. A database failure refuses the request (fail closed).
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
			if CountsAsActivity(r) {
				_ = store.Touch(r.Context(), sess)
			}
			next.ServeHTTP(w, r.WithContext(withSession(r.Context(), sess)))
		})
	}
}

// BackgroundHeader marks a request the console sent from a timer rather than a user action. The appliance Admin
// Console uses the same name and value.
const BackgroundHeader = "X-OneGate-Background"

// CountsAsActivity reports whether a request moves the idle limit. Background requests and whoami never do, or
// every open tab would stay signed in for ever.
func CountsAsActivity(r *http.Request) bool {
	return r.URL.Path != "/v1/auth/whoami" && r.Header.Get(BackgroundHeader) != "1"
}

// jsonErr writes the standard error envelope; codes are literals to avoid importing api (import cycle).
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

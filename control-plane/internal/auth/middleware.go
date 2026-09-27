package auth

import (
	"context"
	"encoding/json"
	"net/http"

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

// RequireAuth extracts the sc_session cookie and attaches the Session to
// the request context. Unauthenticated requests get 401.
func RequireAuth(store *SessionStore) func(http.Handler) http.Handler {
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

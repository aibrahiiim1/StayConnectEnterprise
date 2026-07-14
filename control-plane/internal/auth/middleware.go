package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

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

// RequireRole passes when the caller has any of the listed roles in the
// active tenant scope, OR is a platform_admin.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s := FromContext(r.Context())
			if s == nil {
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "not authenticated", r)
				return
			}
			if s.IsSuperAdmin {
				next.ServeHTTP(w, r)
				return
			}
			for _, want := range roles {
				if slices.Contains(s.Roles, want) {
					next.ServeHTTP(w, r)
					return
				}
			}
			jsonErr(w, http.StatusForbidden, "forbidden", "insufficient role", r)
		})
	}
}

// EffectiveTenantID returns the tenant the request is operating on.
// Rules:
//   - platform_admin may pass ?tenant_id=<uuid> to scope into a tenant.
//     If omitted, returns "" (global scope — list-all endpoints decide).
//   - regular operators ALWAYS operate on their DefaultTenantID; any
//     ?tenant_id= override is ignored (silent, not 403, to keep URLs stable).
func EffectiveTenantID(r *http.Request) string {
	s := FromContext(r.Context())
	if s == nil {
		return ""
	}
	if s.IsSuperAdmin {
		if q := r.URL.Query().Get("tenant_id"); q != "" {
			return q
		}
		return ""
	}
	return s.DefaultTenantID
}

// RequireTenant returns 400 when EffectiveTenantID is empty. Use on
// tenant-scoped endpoints where a global view is not meaningful.
func RequireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if EffectiveTenantID(r) == "" {
			jsonErr(w, http.StatusBadRequest, "bad_request", "tenant scope required (pass ?tenant_id=... as platform_admin)", r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireTenantOrPlatform is like RequireTenant but lets a super-admin (platform
// admin) through with an EMPTY tenant scope, enabling the Global Customer Context
// "All Customers" fan-out on list endpoints. A regular operator still must have a
// tenant (their DefaultTenantID is authoritative; an empty scope is rejected).
// Handlers behind this middleware are responsible for honoring an empty scope
// safely — list handlers fan out; create handlers must still require an explicit
// customer.
func RequireTenantOrPlatform(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if EffectiveTenantID(r) == "" {
			if s := FromContext(r.Context()); s == nil || !s.IsSuperAdmin {
				jsonErr(w, http.StatusBadRequest, "bad_request", "tenant scope required (pass ?tenant_id=... as platform_admin)", r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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

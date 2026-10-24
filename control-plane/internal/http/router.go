package http

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/metrics"
	"github.com/stayconnect/enterprise/control-plane/internal/pki"
	"github.com/stayconnect/enterprise/control-plane/internal/portaltls"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
)

type Deps struct {
	DB            *pgxpool.Pool
	Redis         *redis.Client
	Metrics       *metrics.Registry
	Licensing     *licensing.Service         // nil when no vendor key is configured
	CA            *pki.CA                    // nil disables PKI routes
	ReplayCache   *applianceauth.ReplayCache // shared jti replay cache (both transports)
	AssignKey     ed25519.PrivateKey
	VendorKeyPath string
	CABundlePath  string
	ApplianceBase string // CTRLAPI_APPLIANCE_BASE
	Version       string
	AllowOrigins  []string
	CookieSecure  bool
	PortalTLS     *portaltls.Service // nil leaves /cloud/v1/portal-tls unavailable
	MFA           *auth.MFA          // nil refuses every sign-in

	rateCounter api.RateCounter // tests only; production counts in Redis
}

// Per-client-address limits, per minute.
const (
	loginPerMinute     = 20
	reauthPerMinute    = 20
	registerPerMinute  = 30
	appliancePerMinute = 240
)

func NewRouter(d Deps) http.Handler {
	store := &auth.SessionStore{R: d.Redis}
	repo := &auth.Repo{DB: d.DB}
	adeps := authDeps{Repo: repo, Store: store, Secure: d.CookieSecure, DB: d.DB,
		Failures: ratebudget.New(5, 300, time.Minute), MFA: d.MFA}
	// Every authenticated request re-reads the operator; only a DB-less router (unit tests) runs without it.
	var checker auth.SessionChecker
	if d.DB != nil {
		checker = auth.DBSessionChecker{DB: d.DB}
	}

	replayCache := d.ReplayCache
	if replayCache == nil {
		replayCache = applianceauth.NewReplayCache(2*time.Minute, 8192)
	}
	identityBase := &api.IdentityBase{Base: &api.Base{DB: d.DB}, ReplayCache: replayCache}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// No middleware.RealIP: it trusts any client's X-Forwarded-For; clientip.From trusts only the loopback proxy.
	r.Use(traceIDHeader)
	r.Use(requestLog)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))
	r.Use(corsMiddleware(d.AllowOrigins))
	r.Use(sameOriginWrites(d.AllowOrigins))
	if d.Metrics != nil {
		r.Use(d.Metrics.Middleware)
	}

	r.Get("/healthz", healthz(d))
	r.Get("/readyz", readyz(d))
	if d.Metrics != nil {
		r.Method("GET", "/metrics", loopbackOnly(d.Metrics.Handler()))
	}

	// Limits are keyed on the server-derived client address, never a client-controlled header.
	var counter api.RateCounter = d.rateCounter
	if counter == nil && d.Redis != nil {
		counter = api.RedisRateCounter(d.Redis)
	}
	limit := func(prefix string, n int) func(http.Handler) http.Handler {
		return api.RateLimitWith(counter, prefix, n, time.Minute)
	}

	r.Route("/v1", func(r chi.Router) {
		// Password re-entry is limited before the session check, so guesses without a session are limited too.
		r.With(limit("login", loginPerMinute)).Post("/auth/login", adeps.login)
		r.With(limit("login", loginPerMinute)).Post("/auth/login/code", adeps.loginCode)
		r.With(auth.RequireAuth(store, checker)).Get("/auth/mfa", adeps.mfaStatus)
		r.With(auth.RequireAuth(store, checker), api.RequireReauth(d.Redis)).Post("/auth/mfa/recovery-codes", adeps.mfaRecoveryCodes)
		r.With(auth.RequireAuth(store, checker), api.RequireReauth(d.Redis)).Post("/auth/mfa/setup", adeps.mfaSetup)
		r.With(auth.RequireAuth(store, checker), api.RequireReauth(d.Redis)).Post("/auth/mfa/confirm", adeps.mfaConfirm)
		r.With(auth.RequireAuth(store, checker), api.RequireReauth(d.Redis)).Post("/auth/mfa/disable", adeps.mfaDisable)
		r.Post("/auth/logout", adeps.logout)
		r.With(limit("reauth", reauthPerMinute), auth.RequireAuth(store, checker)).Post("/auth/reauth", adeps.reauth)
		r.With(auth.RequireAuth(store, checker)).Get("/auth/whoami", adeps.whoami)

		// Token-less registration: the appliance self-signs and appears as WAITING.
		r.With(limit("register", registerPerMinute)).
			Post("/appliances/register", identityBase.RegisterHandler)

		// Signed request JWT. The assignment channel is mTLS-only (api.ApplianceMTLSRouter), never here.
		r.Group(func(r chi.Router) {
			// Limited before authentication so unauthenticated callers cannot force lookups and audit writes.
			r.Use(limit("appliance", appliancePerMinute))
			r.Use(auth.RequireAppliance(d.DB, replayCache))
			r.Get("/appliance/hello", identityBase.HelloHandler)
			r.Post("/appliance/offline-reconcile", identityBase.OfflineReconcile)
			if d.Licensing != nil {
				licBase := &api.LicensesBase{Base: &api.Base{DB: d.DB}, Svc: d.Licensing}
				r.Get("/appliance/license", licBase.ApplianceLicenseHandler)
			}
			if d.CA != nil {
				certBase := &api.CertBase{Base: &api.Base{DB: d.DB}, CA: d.CA, ClientValid: 90 * 24 * time.Hour}
				r.Post("/appliance/csr", certBase.SubmitCSR)
				r.Get("/appliance/certificate", certBase.FetchCertificate)
			}
		})
	})

	base := &api.Base{DB: d.DB, Redis: d.Redis, AssignKey: d.AssignKey, CA: d.CA,
		ClientValid: 90 * 24 * time.Hour, Lic: d.Licensing, PortalTLS: d.PortalTLS, MFA: d.MFA}
	if d.ApplianceBase == "" {
		slog.Warn("offline activation packages are DISABLED: CTRLAPI_APPLIANCE_BASE is not set. Set it to " +
			"the appliance-facing HTTPS endpoint (a stable FQDN, not an IP) that appliances dial, then restart.")
	}
	off := api.NewOfflineBase(base, d.VendorKeyPath, d.CABundlePath, d.ApplianceBase)
	r.Route("/cloud/v1", func(r chi.Router) {
		r.Use(auth.RequireAuth(store, checker))
		r.Mount("/", base.CloudRoutes(off))
	})
	return r
}

// loopbackOnly refuses anything not straight from this host; a request relayed by the local proxy carries forwarding headers.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !clientip.IsLoopback(clientip.Peer(r)) || clientip.Proxied(r) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func healthz(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func readyz(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := d.DB.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not-ready", "db": "unreachable"})
			return
		}
		if err := d.Redis.Ping(ctx).Err(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not-ready", "redis": "unreachable"})
			return
		}
		// Without the two-step key every console sign-in is refused; central-deploy.sh's smoke test requires "available".
		signin := "available"
		if d.MFA == nil {
			signin = "unavailable"
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "version": d.Version, "signin": signin})
	}
}

func traceIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tid := middleware.GetReqID(r.Context()); tid != "" {
			w.Header().Set("X-Trace-Id", tid)
		}
		next.ServeHTTP(w, r)
	})
}

// sameOriginWrites refuses a state-changing console request whose Origin is neither allowed nor this host.
// SameSite=Lax does not stop a sibling subdomain of the same site. Requests without an Origin pass.
func sameOriginWrites(origins []string) func(http.Handler) http.Handler {
	allow := map[string]bool{}
	for _, o := range origins {
		allow[strings.TrimRight(o, "/")] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			cookieRoute := strings.HasPrefix(r.URL.Path, "/cloud/") || strings.HasPrefix(r.URL.Path, "/v1/auth/")
			origin := r.Header.Get("Origin")
			if !cookieRoute || origin == "" || allow[origin] {
				next.ServeHTTP(w, r)
				return
			}
			host := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
			if host == r.Host || (r.Header.Get("X-Forwarded-Host") != "" && host == r.Header.Get("X-Forwarded-Host")) {
				next.ServeHTTP(w, r)
				return
			}
			api.Fail(w, r, http.StatusForbidden, "cross_origin", "This request did not come from the Central console.")
		})
	}
}

func corsMiddleware(origins []string) func(http.Handler) http.Handler {
	allow := map[string]bool{}
	for _, o := range origins {
		allow[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allow[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With, "+auth.BackgroundHeader)
				w.Header().Set("Access-Control-Expose-Headers", "X-Trace-Id")
				w.Header().Set("Vary", strings.TrimSpace("Origin, "+w.Header().Get("Vary")))
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

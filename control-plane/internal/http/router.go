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
)

type Deps struct {
	DB            *pgxpool.Pool
	Redis         *redis.Client
	Metrics       *metrics.Registry
	Licensing     *licensing.Service         // nil when no vendor key is configured
	CA            *pki.CA                    // appliance certificate authority; nil disables PKI routes
	ReplayCache   *applianceauth.ReplayCache // shared jti replay cache (both transports)
	AssignKey     ed25519.PrivateKey         // dedicated key for signing appliance assignments
	VendorKeyPath string                     // vendor signing key file (offline packages)
	CABundlePath  string                     // CA bundle handed to appliances in offline packages
	ApplianceBase string                     // CTRLAPI_APPLIANCE_BASE, written into activation packages
	Version       string
	AllowOrigins  []string // CORS allowlist for the console in development
	CookieSecure  bool     // set true behind HTTPS

	rateCounter api.RateCounter // tests only; production counts in Redis
}

// Per-client-address limits, per minute.
const (
	loginPerMinute    = 20
	reauthPerMinute   = 20
	registerPerMinute = 30
)

// NewRouter serves exactly the §6 contract: /v1/auth/*, the appliance endpoints under /v1/appliance* and
// /v1/appliances/register, and the operator API under /cloud/v1. Plus /healthz, /readyz and a
// loopback-only /metrics.
func NewRouter(d Deps) http.Handler {
	store := &auth.SessionStore{R: d.Redis}
	repo := &auth.Repo{DB: d.DB}
	adeps := authDeps{Repo: repo, Store: store, Secure: d.CookieSecure, DB: d.DB}

	replayCache := d.ReplayCache
	if replayCache == nil {
		replayCache = applianceauth.NewReplayCache(2*time.Minute, 8192)
	}
	identityBase := &api.IdentityBase{Base: &api.Base{DB: d.DB}, ReplayCache: replayCache}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// No middleware.RealIP: it believes any client's X-Forwarded-For. clientip.From decides per request and
	// trusts forwarding headers only from the loopback proxy.
	r.Use(traceIDHeader)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))
	r.Use(corsMiddleware(d.AllowOrigins))
	if d.Metrics != nil {
		r.Use(d.Metrics.Middleware)
	}

	r.Get("/healthz", healthz(d))
	r.Get("/readyz", readyz(d))
	if d.Metrics != nil {
		r.Method("GET", "/metrics", loopbackOnly(d.Metrics.Handler()))
	}

	// Every limit is keyed on the server-derived client address (clientip), never on a header a direct
	// client controls.
	var counter api.RateCounter = d.rateCounter
	if counter == nil && d.Redis != nil {
		counter = api.RedisRateCounter(d.Redis)
	}
	limit := func(prefix string, n int) func(http.Handler) http.Handler {
		return api.RateLimitWith(counter, prefix, n, time.Minute)
	}

	r.Route("/v1", func(r chi.Router) {
		// Operator authentication. Password re-entry is limited BEFORE the session check, so the limit also
		// holds for guesses made without a valid session.
		r.With(limit("login", loginPerMinute)).Post("/auth/login", adeps.login)
		r.Post("/auth/logout", adeps.logout)
		r.With(limit("reauth", reauthPerMinute), auth.RequireAuth(store)).Post("/auth/reauth", adeps.reauth)
		r.With(auth.RequireAuth(store)).Get("/auth/whoami", adeps.whoami)

		// Appliance: TOKEN-LESS registration. The appliance self-signs with its locally generated identity
		// key and appears as WAITING.
		r.With(limit("register", registerPerMinute)).
			Post("/appliances/register", identityBase.RegisterHandler)

		// Appliance: signed request JWT (and, on the mTLS listener, a client certificate as well). The
		// assignment channel (/appliance/assignment, /ack, /assignment-registry) is mTLS-only and is served
		// by api.ApplianceMTLSRouter, never here.
		r.Group(func(r chi.Router) {
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
		ClientValid: 90 * 24 * time.Hour, Lic: d.Licensing}
	if d.ApplianceBase == "" {
		slog.Warn("offline activation packages are DISABLED: CTRLAPI_APPLIANCE_BASE is not set. Set it to " +
			"the appliance-facing HTTPS endpoint (a stable FQDN, not an IP) that appliances dial, then restart.")
	}
	off := api.NewOfflineBase(base, d.VendorKeyPath, d.CABundlePath, d.ApplianceBase)
	r.Route("/cloud/v1", func(r chi.Router) {
		r.Use(auth.RequireAuth(store))
		r.Mount("/", base.CloudRoutes(off))
	})
	return r
}

// loopbackOnly refuses anything that did not come straight from this host: /metrics is for a local
// scraper, never for the internet, and a request relayed by the local proxy carries forwarding headers.
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
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "version": d.Version})
	}
}

// traceIDHeader mirrors chi's request-id into the X-Trace-Id response header
// so clients can echo it when reporting issues.
func traceIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tid := middleware.GetReqID(r.Context()); tid != "" {
			w.Header().Set("X-Trace-Id", tid)
		}
		next.ServeHTTP(w, r)
	})
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
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With")
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

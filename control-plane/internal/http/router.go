package http

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/configpush"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/pki"
	"github.com/stayconnect/enterprise/control-plane/internal/metrics"
	"github.com/stayconnect/enterprise/control-plane/internal/oidc"
	"github.com/stayconnect/enterprise/control-plane/internal/transport"
)

type Deps struct {
	DB    *pgxpool.Pool
	Redis *redis.Client
	// GuestDB backs the DEPRECATED legacy /v1 guest-domain routes (vouchers,
	// sessions, PMS config, walled garden, payments...). After a site's
	// cutover those tables live in the site-local database, so the
	// compatibility adapters must read/write THERE, not the cloud schema.
	// nil = single-DB mode (pre-cutover): falls back to DB.
	// See docs/API_DEPRECATIONS.md; these mounts disappear after the pilot.
	GuestDB      *pgxpool.Pool
	Transport    transport.ApplianceTransport
	ConfigPush   *configpush.Pusher
	Metrics      *metrics.Registry
	OIDC         *oidc.Registry
	Licensing    *licensing.Service // nil when no vendor key is configured
	CA           *pki.CA            // appliance certificate authority; nil disables PKI routes
	ReplayCache  *applianceauth.ReplayCache // shared jti replay cache (both transports)
	NATSConn     *nats.Conn         // live NATS (mTLS) for command publish; nil disables commands
	CommandKey   string             // path to command-signing key
	AssignKey    ed25519.PrivateKey // dedicated key for signing appliance assignments; nil disables signed assignments
	AssignRegistryRoot ed25519.PrivateKey // registry root key (re-signs the trust registry on key-state change)
	Version      string
	AllowOrigins []string // CORS allowlist for dev UI on :3000
	CookieSecure bool     // set true behind HTTPS
}

func NewRouter(d Deps) http.Handler {
	store := &auth.SessionStore{R: d.Redis}
	repo := &auth.Repo{DB: d.DB}
	adeps := authDeps{Repo: repo, Store: store, Secure: d.CookieSecure}

	// Guest-domain compatibility pool (see Deps.GuestDB).
	guestDB := d.GuestDB
	if guestDB == nil {
		guestDB = d.DB
	}

	// Replay cache for appliance-signed JWTs. 2-minute window + 8k entry cap
	// is plenty for a single control-plane replica in phase 5.1; promote to
	// Redis when we horizontally scale ctrlapi (phase 5.2+).
	replayCache := d.ReplayCache
	if replayCache == nil {
		replayCache = applianceauth.NewReplayCache(2*time.Minute, 8192)
	}
	enrollBase := &api.EnrollmentBase{Base: &api.Base{DB: d.DB}, ReplayCache: replayCache}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
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
		r.Method("GET", "/metrics", d.Metrics.Handler())
	}

	// Stub OIDC consent page. The browser reaches it as
	// /api/oauth/stub/authorize-sso through the Next.js proxy, which strips
	// /api before forwarding here, so the route is mounted without that prefix.
	{
		stubBase := &api.SSOBase{Base: &api.Base{DB: d.DB}, Registry: d.OIDC}
		r.Mount("/oauth/stub/authorize-sso", stubBase.StubAuthorizeRoutes())
	}

	r.Route("/v1", func(r chi.Router) {
		r.Get("/version", version(d))

		// Unauthenticated auth endpoints.
		r.Post("/auth/login", adeps.login)
		r.Post("/auth/logout", adeps.logout)

		// Public appliance enrollment — scd's first-boot POST. Guarded only
		// by the single-use bootstrap token.
		r.With(api.RateLimit(d.Redis, "enroll", 20, time.Minute)).
			Post("/appliances/enroll", enrollBase.EnrollHandler)

		// Public payments flow (phase 12) — guests are anonymous at the
		// portal; Stripe webhooks are authenticated by HMAC signature.
		// Uses the guest-domain pool: issued vouchers must land in the SITE
		// database where scd redeems them.
		payBase := &api.PaymentsBase{Base: &api.Base{DB: guestDB}, Metrics: d.Metrics}
		r.Mount("/", payBase.PublicRoutes())

		// Appliance-JWT-authenticated endpoints. Phase 5.1 ships only the
		// smoke-test "hello"; 5.2 adds the real NATS-backed RPC surface.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAppliance(d.DB, replayCache))
			r.Get("/appliance/hello", enrollBase.HelloHandler)
			r.Post("/appliance/offline-reconcile", enrollBase.OfflineReconcile)
			if d.Licensing != nil {
				licBase := &api.LicensesBase{Base: &api.Base{DB: d.DB}, Svc: d.Licensing}
				// A successful fetch doubles as the edge's cloud validation.
				r.Get("/appliance/license", licBase.ApplianceLicenseHandler)
			}
			// NOTE: /appliance/assignment and its /ack + /assignment-registry are
			// deliberately NOT mounted here. The assignment channel is mTLS-ONLY
			// (ApplianceMTLSRouter) — no bootstrap-token or JWT-over-:443 fallback —
			// so a document can only ever reach a box holding a valid client cert.
			if d.CA != nil {
				certBase := &api.CertBase{Base: &api.Base{DB: d.DB}, CA: d.CA, ClientValid: 90 * 24 * time.Hour}
				r.Post("/appliance/csr", certBase.SubmitCSR)
				r.Get("/appliance/certificate", certBase.FetchCertificate)
				r.Get("/appliance/ca", certBase.CAHandler)
			}
		})

		// SSO (operator) — public; the stub authorize page lives outside
		// /v1 so it's mounted at the router root below.
		base := &api.Base{DB: d.DB}
		ssoBase := &api.SSOBase{
			Base:         base,
			Registry:     d.OIDC,
			Sessions:     store,
			Redis:        d.Redis,
			CookieSecure: d.CookieSecure,
		}
		r.Mount("/auth/sso", ssoBase.Routes())

		// Everything below requires a session.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAuth(store))
			r.Get("/auth/whoami", adeps.whoami)
			r.Post("/auth/reauth", adeps.reauth)

			// Redis is REQUIRED here: RequireReauth fails closed without it, so any
			// step-up-gated route on /v1 (plan limits, tenant limit overrides,
			// subscription terms) would 403 forever even after a successful reauth.
			base := &api.Base{DB: d.DB, Redis: d.Redis}
			// gbase: DEPRECATED guest-domain compatibility adapters — rows
			// live in the site database post-cutover (Deps.GuestDB) while
			// commercial limits stay in the cloud schema (LimitsDB).
			gbase := &api.Base{DB: guestDB, LimitsDB: d.DB, Redis: d.Redis}
			// Appliance routes are cloud-domain but effective-config reads
			// site-owned tables (PMS providers, walled garden) → GuestDB.
			abase := &api.Base{DB: d.DB, GuestDB: guestDB, Redis: d.Redis}
			r.Mount("/tenants", base.TenantsRoutes())
			r.Mount("/sites", base.SitesRoutes())
			r.Mount("/appliances", abase.AppliancesRoutes())
			r.Mount("/ticket-templates", gbase.TemplatesRoutes())
			r.Mount("/plans", base.PlansRoutes())
			r.Mount("/operators", base.OperatorsRoutes())
			// RETIRED centrally — raw guest-operational data + hotel credentials
			// (guest sessions, voucher codes, PMS config/creds, payments, social
			// secrets, walled-garden runtime) are owned ONLY by the appliance
			// Edge API. These return 410 Gone in the Cloud so no Platform or
			// Tenant user can retrieve raw operational records.
			r.Mount("/voucher-batches", gone410())
			r.Mount("/vouchers", gone410())
			r.Mount("/sessions", gone410())
			r.Mount("/pms-providers", gone410())
			r.Mount("/social-providers", gone410())
			r.Mount("/walled-garden", gone410())
			r.Mount("/payments", gone410())
			nbase := &api.NotificationAdminBase{Base: gbase, ConfigPush: d.ConfigPush}
			r.Mount("/notification-providers", nbase.Routes())
			strBase := &api.StripeAdminBase{Base: gbase}
			r.Mount("/stripe-accounts", strBase.Routes())
			ebase := &api.EnrollmentBase{Base: base, ReplayCache: replayCache}
			r.Mount("/appliance-bootstrap-tokens", ebase.TokenRoutes())
		})
	})

	// -------------------------------------------------------------------
	// /cloud/v1 — canonical Cloud API namespace (edge-first refactor).
	//
	// Carries ONLY the vendor-commercial / fleet-management domain:
	// customers (tenants), sites, appliance inventory + enrollment,
	// commercial plans, subscriptions, licenses/entitlements, fleet health
	// and platform/group operators. Guest-domain resources (vouchers,
	// sessions, PMS config, walled garden, guest access plans) are owned by
	// the per-site Edge API (/edge/v1 on the appliance) and are NOT mounted
	// here. The legacy /v1 guest-domain routes remain temporarily as
	// deprecated compatibility adapters — see docs/API_DEPRECATIONS.md.
	// -------------------------------------------------------------------
	r.Route("/cloud/v1", func(r chi.Router) {
		r.Get("/version", version(d))
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAuth(store))
			base := &api.Base{DB: d.DB, Redis: d.Redis}
			abase := &api.Base{DB: d.DB, GuestDB: guestDB, Redis: d.Redis, AssignKey: d.AssignKey}
			r.Mount("/tenants", base.TenantsRoutes())
			r.Mount("/sites", base.SitesRoutes())
			r.Mount("/appliances", abase.AppliancesRoutes())
			// Platform-only appliance enrollment lifecycle (pending, claim,
			// assign, revoke, security alerts) — permission-gated, no tenant scope.
			r.Mount("/appliances-admin", abase.LifecycleRoutes())
			// Dedicated assignment-signing key lifecycle (list, retire/rotate).
			r.Mount("/assignment-keys", (&api.AssignmentKeysBase{Base: base, RegRoot: d.AssignRegistryRoot}).Routes())
			// Platform certificate lifecycle (list, issue, revoke, CA).
			if d.CA != nil {
				certBase := &api.CertBase{Base: base, CA: d.CA, ClientValid: 90 * 24 * time.Hour}
				r.Mount("/certificates", certBase.PlatformRoutes())
			}
			// Tenant-facing own-appliance support/replacement/reassignment requests.
			r.Mount("/appliances-support", abase.TenantSupportRoutes())
			// Signed, allow-listed command channel (platform.commands.issue + reauth).
			if d.CommandKey != "" {
				if cb := api.NewCommandsBase(base, d.NATSConn, d.CommandKey); cb != nil {
					r.Mount("/commands", cb.Routes())
				}
			}
			// Offline signed activation packages (vendor-signed, appliance-bound).
			vendorKey := os.Getenv("CTRLAPI_VENDOR_KEY")
			if vendorKey == "" {
				vendorKey = "/etc/stayconnect/vendor-license.key"
			}
			if ob := api.NewOfflineBase(base, vendorKey, "/etc/stayconnect/pki/nats-ca-bundle.crt"); ob != nil {
				r.Mount("/offline-packages", ob.Routes())
			}
			// Signed software update lifecycle (platform.updates.manage + reauth).
			updateKey := os.Getenv("CTRLAPI_UPDATE_KEY")
			if updateKey == "" {
				updateKey = "/etc/stayconnect/update-signing.key"
			}
			if ub := api.NewUpdatesBase(base, d.NATSConn, updateKey); ub != nil {
				r.Mount("/updates", ub.Routes())
			}
			// Unambiguous name for subscription plans sold by StayConnect.
			r.Mount("/commercial-plans", base.PlansRoutes())
			r.Mount("/operators", base.OperatorsRoutes())
			ebase := &api.EnrollmentBase{Base: base, ReplayCache: replayCache}
			r.Mount("/appliance-bootstrap-tokens", ebase.TokenRoutes())
			fleetBase := &api.FleetBase{Base: base}
			r.Mount("/fleet", fleetBase.Routes())
			if d.Licensing != nil {
				licBase := &api.LicensesBase{Base: base, Svc: d.Licensing}
				r.Mount("/licenses", licBase.Routes())
			}
		})
	})

	return r
}

// gone410 answers every method with 410 Gone. Used to retire raw guest-
// operational + credential routes from the Cloud (owned by the appliance Edge).
func gone410() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"gone","message":"retired from the Cloud; owned by the appliance Edge API (/edge/v1)"}`))
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
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not-ready", "db": err.Error()})
			return
		}
		if err := d.Redis.Ping(ctx).Err(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not-ready", "redis": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
	}
}

func version(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"version": d.Version, "service": "ctrlapi"})
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

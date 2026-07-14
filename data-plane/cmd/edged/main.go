// edged — the Hotel Admin API (Edge API) served on each appliance.
//
// Listens on the management interface (default 127.0.0.1:8090, fronted by
// Caddy on the appliance's management IP — NEVER the guest LAN or WAN) and
// owns the guest-domain resources in the site-local database: local
// operators, guest access plans, vouchers, sessions, PMS configuration,
// walled garden, portal branding, payments and the local audit log.
//
// It needs no cloud connectivity for anything: enforcement actions
// (disconnect, PMS test/reload, license install) go to scd over its unix
// socket; everything else is site-DB CRUD. That is what keeps Hotel Admin
// fully functional during a cloud outage.
//
// Subcommands:
//
//	edged serve                             — run the API (default)
//	edged seed-admin --email E --password P — create/update the first
//	                                          site_admin (bootstrap)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
)

var version = "0.1.0-edge"

type cfg struct {
	Addr        string
	DBURL       string
	SCDSocket   string
	IdentityDir string
	// Legacy identity fallback (pre-enrollment dev boxes).
	TenantID string
	SiteID   string
	// CookieSecure should be true when Caddy fronts edged with TLS.
	CookieSecure bool
}

func loadCfg() cfg {
	return cfg{
		Addr:         envOr("EDGED_ADDR", "127.0.0.1:8090"),
		DBURL:        envOr("EDGED_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect_site?sslmode=disable"),
		SCDSocket:    envOr("EDGED_SCD_SOCKET", "/run/stayconnect/scd.sock"),
		IdentityDir:  envOr("EDGED_IDENTITY_DIR", "/etc/stayconnect/identity"),
		TenantID:     os.Getenv("EDGED_TENANT_ID"),
		SiteID:       os.Getenv("EDGED_SITE_ID"),
		CookieSecure: os.Getenv("EDGED_COOKIE_SECURE") == "true",
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type server struct {
	db       *pgxpool.Pool
	scd      *scdClient
	netd     *netdClient
	sessions *sessionStore
	tenantID string
	siteID   string
	secure   bool
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "seed-admin":
			if err := runSeedAdmin(os.Args[2:]); err != nil {
				slog.Error("seed-admin failed", "err", err)
				os.Exit(1)
			}
			return
		case "serve", "":
		default:
			slog.Error("unknown subcommand", "arg", os.Args[1])
			os.Exit(2)
		}
	}

	// Adaptive crash-loop backoff (see internal/startupbackoff): a persistently
	// broken edged backs off exponentially instead of a 2s restart storm.
	startupbackoff.Guard("edged")

	c := loadCfg()
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Site identity comes from the signed ASSIGNMENT document — the appliance-local
	// source of truth written by scd's assignment agent — NOT from identity.json or
	// env. A generic appliance ships with no tenant/site; before assignment edged
	// runs in awaiting-assignment mode. Legacy env is a migration-only fallback.
	asgStore := &assignment.Store{Dir: envOr("EDGED_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
	if aTen, aSite, _, _ := asgStore.Resolved(); aTen != "" && aSite != "" {
		c.TenantID, c.SiteID = aTen, aSite
		slog.Info("assignment resolved", "tenant_id", aTen, "site_id", aSite)
	} else if c.TenantID != "" && c.SiteID != "" {
		slog.Warn("no signed assignment; using legacy env tenant/site as migration fallback")
	} else {
		c.TenantID, c.SiteID = "", ""
		slog.Warn("awaiting assignment: edged running without a tenant/site (generic appliance)")
	}
	// Adopt a new assignment with no manual restart: re-exec when the locally
	// persisted assignment version changes (scd's agent writes it).
	go watchAssignmentReexec(rootCtx, asgStore)

	pool, err := pgxpool.New(rootCtx, c.DBURL)
	if err != nil {
		// A bad DSN is a genuine configuration fault, not a transient one.
		slog.Error("db open", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	// The site DB is a container that may not be ready at cold boot, or during
	// a restart of the DB. Retry the first connection with bounded backoff
	// instead of exiting: crash-looping here would, under systemd's start-limit,
	// wedge edged permanently (the Hotel Admin API would stay inactive with no
	// automatic recovery). If the DB is still unreachable after the window we
	// serve anyway — the pool keeps retrying per query, so edged recovers on its
	// own the moment the DB comes back, with no manual restart.
	for attempt := 1; ; attempt++ {
		pctx, cancel := context.WithTimeout(rootCtx, 3*time.Second)
		perr := pool.Ping(pctx)
		cancel()
		if perr == nil {
			break
		}
		if rootCtx.Err() != nil {
			return
		}
		if attempt >= 60 {
			slog.Warn("site db still unreachable after retries; serving degraded (pool keeps retrying per query)",
				"err", perr, "dsn_host", c.DBURL)
			break
		}
		slog.Warn("site db not ready, retrying", "attempt", attempt, "err", perr)
		select {
		case <-rootCtx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	s := &server{
		db:       pool,
		scd:      newSCDClient(c.SCDSocket),
		netd:     newNetdClient(envOr("EDGED_NETD_SOCKET", "/run/stayconnect/netd.sock")),
		sessions: newSessionStore(12 * time.Hour),
		tenantID: c.TenantID,
		siteID:   c.SiteID,
		secure:   c.CookieSecure,
	}

	// Appliance Health Supervisor: observe/diagnose every critical service,
	// persist the authoritative health model, track boot convergence and push
	// sanitized telemetry. It never controls restarts (systemd + adaptive
	// startup backoff own recovery), so it cannot fight the service manager.
	go s.healthMonitorLoop(rootCtx)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	r.Route("/edge/v1", func(r chi.Router) {
		r.Get("/health", s.health)
		r.Post("/auth/login", s.login)
		r.Post("/auth/logout", s.logout)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/whoami", s.whoami)

			// License is readable by anyone who can log in; install/refresh
			// are site_admin actions.
			r.Get("/license", s.licenseStatus)
			r.With(s.requireRole("license", permWrite)).Post("/license", s.licenseInstall)
			r.With(s.requireRole("license", permWrite)).Post("/license/refresh", s.licenseRefresh)

			// Local enrollment wizard. These also exist under /network/setup/* for
			// backwards compatibility, but the wizard is a SETUP concern, not a
			// networking one, and the Hotel Admin UI calls them here — without this
			// the whole wizard renders "Could not read setup status: HTTP 404".
			r.Get("/setup/status", s.setupStatus)
			r.With(s.requireRole("network", permWrite)).Post("/setup/enroll", s.setupEnroll)
			r.With(s.requireRole("network", permWrite)).Post("/setup/offline-import", s.setupOfflineImport)

			// Hotel Admin TLS certificate: status (read), diagnostic Check, and the
			// manual Rotate (step-up inside the handler). Same "network" role as the
			// system-network settings that own the management IP.
			r.With(s.requireRole("network", permRead)).Get("/hotel-admin-cert", s.hotelAdminCertGet)
			r.With(s.requireRole("network", permWrite)).Post("/hotel-admin-cert/check", s.hotelAdminCertCheck)
			r.With(s.requireRole("network", permWrite)).Post("/hotel-admin-cert/rotate", s.hotelAdminCertRotate)

			mountResource(r, s, "operators", s.operatorsRoutes)
			mountResource(r, s, "guest-access-plans", s.guestAccessPlansRoutes)
			mountResource(r, s, "voucher-batches", s.voucherBatchesRoutes)
			mountResource(r, s, "vouchers", s.vouchersRoutes)
			mountResource(r, s, "guest-accounts", s.guestAccountsRoutes)
			mountResource(r, s, "sessions", s.sessionsRoutes)
			mountResource(r, s, "pms-providers", s.pmsProvidersRoutes)
			mountResource(r, s, "auth-methods", s.authMethodsRoutes)
			mountResource(r, s, "walled-garden", s.walledGardenRoutes)
			mountResource(r, s, "portal-branding", s.brandingRoutes)
			mountResource(r, s, "payments", s.paymentsRoutes)
			mountResource(r, s, "stripe-accounts", s.stripeAccountsRoutes)
			mountResource(r, s, "notification-providers", s.notificationProvidersRoutes)
			mountResource(r, s, "social-providers", s.socialProvidersRoutes)
			mountResource(r, s, "audit", s.auditRoutes)
			mountResource(r, s, "reports", s.reportsRoutes)
			mountResource(r, s, "backups", s.backupsRoutes)
			mountResource(r, s, "network", s.networkRoutes)
			// NOTE: mounted at "diagnostics", NOT "health" — the unauthenticated
			// GET /edge/v1/health summary (common.go) must not be shadowed.
			mountResource(r, s, "diagnostics", s.healthRoutes)
		})
	})

	// Management-plane listener only. Refuse to bind wildcard/guest-network
	// addresses: misconfiguration must fail loudly, not expose Hotel Admin.
	if host, _, err := net.SplitHostPort(c.Addr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			slog.Error("EDGED_ADDR must bind a specific management address, not a wildcard", "addr", c.Addr)
			os.Exit(2)
		}
	}

	srv := &http.Server{Addr: c.Addr, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("edged listening", "addr", c.Addr, "site_id", c.SiteID, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "err", err)
			stop()
		}
	}()

	<-rootCtx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

// mountResource wires a resource router under its permission key: reads
// need permRead on the key, writes permWrite. Fine-grained checks happen in
// requireRole using the role→permission matrix in auth.go.
func mountResource(r chi.Router, s *server, name string, routes func() http.Handler) {
	r.Route("/"+name, func(r chi.Router) {
		r.Use(s.resourcePermission(name))
		r.Mount("/", routes())
	})
}

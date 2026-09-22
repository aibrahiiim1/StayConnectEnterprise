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
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/posting"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
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

	// surfaces records every resource mountResource actually mounted on THIS appliance, so the admin can ask
	// rather than infer it from build-time flags. See capabilities.go.
	surfaces *mountedSurfaces

	// Phase 2 DARK Hotel-Admin commerce. commerce is ALWAYS constructed but holds a nil repository while
	// the master flag is OFF (zero Phase-2 SQL); commerceCfg gates whether the admin routes are mounted.
	commerce    *iamv2.CommerceAdmin
	commerceCfg iamv2.CommerceConfig
	// commerceRepo is the same repository the admin engine holds. The grace publication path needs it
	// directly because D32 publication is not an admin-engine operation: it derives system catalogue rows and
	// goes through the canonical audited boundary, neither of which the operator-facing engine may do.
	commerceRepo iamv2.CommerceAdminRepository
	// iamv2Cfg is the IAM-v2 authentication config. edged needs it because credential ISSUANCE must land
	// in whichever domain will later AUTHENTICATE the credential: with IAM-v2 as the ACCOUNT authority,
	// an account issued into the legacy table can never be logged in with. Nothing in the runtime issued
	// IAM-v2 credentials at all before this -- the authentication domain existed and the issuance side of
	// it did not, so iam_v2.guest_access_accounts sat at 0 rows on an appliance with ACCOUNT enabled.
	iamv2Cfg iamv2.Config

	// Phase 3 DARK Hotel-Admin PMS/Stay surface. pmsCfg gates whether ANY Phase-3 route is mounted; while
	// the master flag is OFF the routes do not exist at all and edged issues zero Phase-3 SQL.
	pmsCfg    iamv2.PMSConfig
	phase5Cfg iamv2.Phase5Config
	phase6    iamv2.Phase6Config

	// Phase 4 DARK financial surface. financialCfg gates whether the Manual Review routes are mounted at
	// all; while dark they do not exist, so an unmounted route cannot leak a financial schema that is not
	// live yet. It carries NO transport and constructs no engine — edged is an operator API, not a sender.
	financialCfg posting.Config
	// The outcome of migration 0023 reconcile at startup, so the financial surface can report what the
	// detector said rather than re-deriving it per request. Empty when it could not be established.
	financialEpoch string
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
		surfaces: newMountedSurfaces(),
	}

	// Phase 2 DARK Hotel-Admin commerce. Config from env (all flags default OFF); nil repository while the
	// master flag is OFF (zero Phase-2 SQL). Fail closed if the master flag is set without a wired repo
	// (cutover only) — edged holds ZERO iam_v2 privileges under its Gate-P role while dark.
	// PHASE 6 (DARK) CONFIGURATION IS LOADED FIRST, BEFORE ANY COMPONENT CONSUMES IT.
	//
	// The ordering is the point. This block used to sit forty lines further down, after the commerce admin
	// had already been handed s.phase6.AggregateTimeOn() -- reading a zero-value config nothing had assigned
	// yet. The capability was therefore OFF whatever the environment said, and no unit test could see it,
	// because every unit test called the validator directly with a flag it chose itself.
	//
	// The operator setting surface is mounted under its own admin flag; the per-appliance PRODUCT setting it
	// edits is a different control entirely, lives in the database, and defaults OFF.
	p6cfg, err := iamv2.LoadPhase6ConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase6 configuration", "err", err)
		os.Exit(1)
	}
	s.phase6 = p6cfg
	slog.Info("phase6 dark guest-device surface", "flags", p6cfg.SafeFlagSummary())

	commCfg, err := iamv2.LoadCommerceConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase2 commerce config", "err", err)
		os.Exit(2)
	}
	// Wired for the same reason as scd's: the AUTHORIZED DEVELOPMENT trial (D31/T0068) runs IAM-v2 for real,
	// so the accepted admin repository is constructed when the master flag is on and left nil while it is off.
	// This is NOT a Production transition decision -- D31 withdrew the "clean replacement / legacy disposable"
	// wording as policy that was never approved, and the Production strategy remains OPEN.
	var commRepo iamv2.CommerceAdminRepository // nil while dark
	if commCfg.MasterEnabled {
		if pool == nil {
			slog.Error("phase2 commerce master flag enabled but no database pool is available")
			os.Exit(2)
		}
		commRepo = iamv2.NewPgCommerceAdminRepository(pool)
		// D32: converge the site on its hidden system grace package at startup.
		//
		// Here rather than in scd because this repository is the one with the admin write surface, and
		// because provisioning is an ADMIN-plane concern: it creates the thing Hotel Admin then points policy
		// at. It is idempotent, so a site that already has one keeps it -- including its immutable revision
		// history, which live entitlements pin.
		//
		// A provisioning failure is logged and does NOT abort startup: the normal grace package being absent
		// is exactly the condition Emergency Grace exists to cover, so refusing to serve would turn a
		// degraded path into an outage.
		if r, perr := iamv2.NewGraceProvisioner(commRepo).EnsureSiteGracePackage(
			context.Background(), commRepo.(iamv2.GraceProvisionRepository), c.TenantID, c.SiteID); perr != nil {
			slog.Error("grace provisioning failed; Emergency Grace remains the fallback", "err", perr)
		} else if r.Skipped != "" {
			slog.Warn("grace provisioning skipped", "reason", r.Skipped)
		} else {
			slog.Info("system grace package converged",
				"package_id", r.PackageID, "revision_id", r.RevisionID,
				"created", r.Created, "revision_published", r.RevisionNew)
		}
	}
	commAdmin, err := iamv2.NewCommerceAdmin(commCfg, commRepo, iamv2.NopObserver{})
	if err != nil {
		slog.Error("phase2 commerce admin new", "err", err)
		os.Exit(2)
	}
	// PHASE 6 (DARK by default): may this build PUBLISH AGGREGATE_ONLINE_TIME plan revisions? With the flag
	// off -- every environment today -- the admin path refuses the mode exactly as it did before Phase 6, and
	// every revision published stays VALIDITY_WINDOW. Existing revisions are immutable and are unaffected
	// either way.
	applyAggregateTimeCapability(commAdmin, p6cfg)
	s.commerce = commAdmin
	s.commerceRepo = commRepo
	s.commerceCfg = commCfg
	// Fail closed on an incoherent IAM-v2 auth config: LoadConfigFromEnv already rejects a per-method flag
	// set while the master flag is off, and edged must not run with a config scd would refuse.
	iamAuthCfg, iamAuthErr := iamv2.LoadConfigFromEnv(os.Getenv, true)
	if iamAuthErr != nil {
		slog.Error("iamv2 auth config invalid", "err", iamAuthErr)
		os.Exit(1)
	}
	s.iamv2Cfg = iamAuthCfg
	slog.Info("phase2 dark commerce admin constructed", "flags", commCfg.SafeFlagSummary())

	// Phase 3 DARK Hotel-Admin PMS/Stay surface. All flags default OFF and a child flag set while the master
	// flag is OFF is a startup failure (a deployment mistake must be loud, not silently "off anyway").
	finCfg, err := posting.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase-4 financial flags are incoherent", "err", err)
		os.Exit(2)
	}
	s.financialCfg = finCfg

	// MIGRATION 0023's RESTORE-ROLLBACK DETECTOR, WHICH HAD NO CALLER UNTIL NOW.
	//
	// Its own contract says it runs BEFORE any financial worker starts, and edged is the only process that
	// constructs a financial engine, so this is that point: after the flags are loaded, before any route is
	// mounted. See financial_epoch_reconcile.go for the failure posture, which mirrors the Phase-3
	// controlled-writer precedent -- loud while Phase 4 is dark, fatal once money can move.
	s.financialEpoch = s.reconcileFinancialEpochAtStartup(rootCtx)

	pmsCfg, err := iamv2.LoadPMSConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase3 pms config", "err", err)
		os.Exit(2)
	}
	s.pmsCfg = pmsCfg

	// Phase 5 DARK post-stay surface. Same rule as every phase before it: a child flag set while the master
	// is off is a startup failure, not a quiet no-op.
	p5cfg, err := iamv2.LoadPhase5ConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase-5 flags are incoherent", "err", err)
		os.Exit(2)
	}
	s.phase5Cfg = p5cfg
	slog.Info("phase5 dark post-stay admin surface", "flags", p5cfg.SafeFlagSummary())

	slog.Info("phase3 dark pms admin surface", "flags", pmsCfg.SafeFlagSummary())
	// Before any Phase-3 admin surface is served, prove the controlled-writer boundary is actually in force
	// for this process. An operator publishing a grace policy through a UI that turns out to be writing raw
	// rows would leave authoritative state with no provenance and no way to tell afterwards.
	if pmsCfg.Enabled() {
		if err := writerguard.Verify(rootCtx, pool, writerguard.Phase3Requirements()); err != nil {
			slog.Error("edged: refusing to serve the Phase-3 surface", "err", err)
			os.Exit(2)
		}
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
			// ALWAYS-PRESENT SURFACES THAT DO NOT GO THROUGH mountResource.
			//
			// The licence, diagnostics and the setup wizard are registered as plain routes because they are not
			// tenant resources behind the role matrix in the same way. They are still SURFACES the navigation
			// asks about, and the first sweep after /capabilities shipped caught the consequence of forgetting
			// that: the License destination reported itself unavailable on an appliance that serves it.
			//
			// Recorded next to the routes themselves rather than in a list somewhere else, for the same reason
			// mountResource records its own.
			s.surfaces.add("license")
			s.surfaces.add("diagnostics")
			s.surfaces.add("reports")

			r.Get("/license", s.licenseStatus)
			r.With(s.requireRole("license", permWrite)).Post("/license", s.licenseInstall)
			r.With(s.requireRole("license", permWrite)).Post("/license/refresh", s.licenseRefresh)

			// Local enrollment wizard. These also exist under /network/setup/* for
			// backwards compatibility, but the wizard is a SETUP concern, not a
			// networking one, and the Hotel Admin UI calls them here — without this
			// the whole wizard renders "Could not read setup status: HTTP 404".
			// WHAT THIS APPLIANCE SERVES. Read by the navigation so it never offers a destination that would
			// 404. Authenticated, but not role-filtered -- see capabilities.go.
			r.Get("/capabilities", s.capabilities)

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
			// guest-access-plans, voucher-batches and vouchers are REMOVED. They were the operator surface
			// over public.ticket_templates, public.voucher_batches and public.vouchers -- the superseded
			// commerce and credential domain. The current surface is "commercial-packages" below: service
			// plans and internet packages in iam_v2, with vouchers issued through scd against
			// iam_v2.vouchers. Two operator surfaces over two competing plan models is how an operator ends
			// up editing the plan that nothing reads.
			mountResource(r, s, "guest-accounts", s.guestAccountsRoutes)
			mountResource(r, s, "sessions", s.sessionsRoutes)
			// USAGE EXPLORER. Read-only investigation over durable accounting and session data; see
			// resources_usage.go. Under the "sessions" role because it is the same live-access state that
			// resource already governs, rather than a new row in the role matrix.
			mountResource(r, s, "usage", s.usageRoutes)
			// "pms-providers" is NOT mounted. It was the superseded PMS configuration model, replaced by the
			// PMS Interface surface below; keeping both meant two operator screens configuring one property
			// management system, and the older one could start a competing connector. See resources_providers.go.
			mountResource(r, s, "auth-methods", s.authMethodsRoutes)
			mountResource(r, s, "walled-garden", s.walledGardenRoutes)
			mountResource(r, s, "portal-branding", s.brandingRoutes)
			mountResource(r, s, "portal-assets", s.portalAssetRoutes)
			// payments is REMOVED. It was a read-only list over public.payments, a Stripe-session record
			// keyed to a superseded voucher and access plan. The current financial surface is
			// "financial-ops" below, over the Phase-4 iam_v2 payment transactions and settlements.
			mountResource(r, s, "stripe-accounts", s.stripeAccountsRoutes)
			mountResource(r, s, "notification-providers", s.notificationProvidersRoutes)
			mountResource(r, s, "social-providers", s.socialProvidersRoutes)
			// Phase 2 (DARK): the commercial-packages admin resource is mounted ONLY when the admin
			// surface is ON; while dark it is absent and the admin engine holds a nil repository.
			if s.commerceCfg.AdminOn() {
				mountResource(r, s, "commercial-packages", s.commercialPackagesRoutes)
			}
			// Phase 3 (DARK): the Stay/Event/Folio/Resolution/Grace surface is mounted ONLY when the
			// Phase-3 master flag AND its admin flag are both ON. While dark these paths do not exist.
			if s.pmsCfg.AdminOn() {
				mountResource(r, s, "pms-stays", s.pmsStaysRoutes)
				mountResource(r, s, "pms-events", s.pmsEventsRoutes)
				mountResource(r, s, "pms-resolutions", s.pmsResolutionsRoutes)
				// GUEST SIGN-IN ATTEMPTS. Two resources, because the second permission is a real boundary and not a
				// flag: the credential values hang off their OWN key, so a caller holding only the list permission is
				// refused by middleware before the handler runs. Registered through mountResource for the reason
				// recorded below — a route mounted directly inside requireAuth has authentication and no authorization.
				mountResource(r, s, "guest-signin-attempts", s.signInAttemptsRoutes)
				mountResource(r, s, "guest-signin-credentials", s.signInCredentialsRoutes)
				// Guest sign-in protection: the policy, and the restrictions it creates. Two keys, because
				// changing the property's thresholds and ending one device's wait are different powers —
				// see resources_signin_protection.go. Neither carries the credentials key.
				mountResource(r, s, "guest-signin-protection", s.signInProtectionRoutes)
				mountResource(r, s, "guest-signin-restrictions", s.signInRestrictionsRoutes)
				mountResource(r, s, "checkout-grace", s.checkoutGraceConfigRoutes)
				mountResource(r, s, "operational-alerts", s.operationalAlertsRoutes)
				mountResource(r, s, "pms-interfaces", s.pmsInterfacesRoutes)
				mountResource(r, s, "pms-routing", s.pmsRoutingRoutes)
				mountResource(r, s, "pms-source-conflicts", s.pmsSourceConflictsRoutes)
				// Unresolved departures as CASES. Read is wide because "which guests does the PMS and the
				// mirror disagree about" is a question the desk asks; write is the one action that hands a
				// recorded event back to the ingestion engine, which is not a desk decision.
				mountResource(r, s, "pms-reconciliation", s.pmsReconciliationRoutes)
				// Closing the stays a complete roster no longer lists. A separate key from the case list
				// above, because reading a disagreement and acting on three hundred of them at once are
				// different powers.
				mountResource(r, s, "pms-roster-reconciliation", s.rosterReconciliationRoutes)
				// Reporting to the cloud. Two keys again: setting how long delivered records are kept, and
				// releasing records the appliance gave up on, are different powers.
				mountResource(r, s, "cloud-sync-settings", s.cloudSyncSettingsRoutes)
				mountResource(r, s, "cloud-sync-recovery", s.cloudSyncRecoveryRoutes)
			}
			// Phase 5 (DARK): the operator post-stay surface. Mounted only when the Phase-5 master flag
			// AND its admin flag are both ON; while dark this path does not exist.
			if s.phase5Cfg.AdminOn() {
				mountResource(r, s, "post-stay-profiles", s.postStayProfilesRoutes)
			}
			// Cross-PMS transfer is its OWN flag, not a child of the post-stay one: they are different
			// operations with different blast radii, and a property that wants post-stay PINs has not
			// thereby asked to be able to end a guest's access and move it.
			if s.phase5Cfg.TransferOn() {
				mountResource(r, s, "stay-transfers", s.stayTransfersRoutes)
			}
			// Phase 6 (DARK): the per-appliance Guest Device Self-Service setting. Mounted only when the
			// Phase-6 master AND its admin flag are on; while dark this path does not exist. Note the two
			// controls are independent -- this mounts the SCREEN, and the screen edits a database setting
			// that is OFF by default and governs the guest surface separately.
			// It is mounted through mountResource like every other management surface, which is what puts it
			// behind resourcePermission and therefore behind the role matrix. The earlier version registered
			// the two routes directly, so they sat inside requireAuth with NO authorization boundary at all --
			// every logged-in operator, including read-only desk roles, could change a guest-facing appliance
			// capability. Authentication is not authorization.
			if s.phase6.DeviceAdminOn() {
				mountResource(r, s, "guest-device-self-service", s.guestDeviceSelfServiceRoutes)
			}
			// Phase 4 (DARK): Financial Manual Review. Mounted only when the Phase-4 master flag AND the
			// review flag are both ON. The delivered configuration has both OFF, so this path does not
			// exist on the appliance.
			if s.financialCfg.ReviewOn() {
				mountResource(r, s, "financial-review", s.financialReviewRoutes)
				// Financial OPERATIONS -- health, settlement history, recovery -- share the review
				// permission because they share a readership, and mount under the same flag so the whole
				// financial surface appears and disappears together.
				mountResource(r, s, "financial-ops", s.financialOpsRoutes)
			}
			// THE VOUCHER SURFACE, and it is mounted UNCONDITIONALLY on purpose.
			//
			// Issuance needs an existing package revision, and authoring packages lives behind the Phase-2
			// commerce admin flag. It would have been easy to hang the whole voucher surface off that flag
			// too -- and wrong: issuing against a revision somebody already published is not the same power
			// as publishing one, and a property that is not authoring packages today still has cards to
			// print and cards to cancel. A surface that appears and disappears with an unrelated flag is
			// also how an operator loses a destination without anything failing.
			//
			// Three keys, three powers. `voucher-codes` is the narrow one: recovering a guest credential in
			// the clear, which additionally requires a password step-up and a bounded reason at the route.
			mountResource(r, s, "vouchers", s.vouchersRoutes)
			mountResource(r, s, "voucher-codes", s.voucherCodesRoutes)
			mountResource(r, s, "voucher-code-settings", s.voucherCodeSettingsRoutes)
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
	// Recorded HERE, where a surface actually becomes reachable, so /capabilities cannot drift from what is
	// mounted. A separate list would be a second thing to remember.
	s.surfaces.add(name)
	r.Route("/"+name, func(r chi.Router) {
		r.Use(s.resourcePermission(name))
		r.Mount("/", routes())
	})
}

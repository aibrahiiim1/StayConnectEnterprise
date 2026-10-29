// edged is the Hotel Admin API, served on the management interface only (never the guest LAN or WAN).
// Enforcement actions go to scd over its unix socket; everything else is site-DB CRUD.
//
// Subcommands: serve (default), seed-admin --email E (--password P | --password-stdin), reset-mfa --email E.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/posting"
	"github.com/stayconnect/enterprise/data-plane/internal/reqlog"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

var version = "0.1.0-edge"

type cfg struct {
	Addr        string
	DBURL       string
	SCDSocket   string
	IdentityDir string
	// Resolved from the verified assignment at startup (scope.go); never from the environment in production.
	TenantID     string
	SiteID       string
	CookieSecure bool
}

func loadCfg() cfg {
	return cfg{
		Addr:        envOr("EDGED_ADDR", "127.0.0.1:8090"),
		DBURL:       envOr("EDGED_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect_site?sslmode=disable"),
		SCDSocket:   envOr("EDGED_SCD_SOCKET", "/run/stayconnect/scd.sock"),
		IdentityDir: envOr("EDGED_IDENTITY_DIR", "/etc/stayconnect/identity"),
		// Secure unless explicitly switched off: browsers treat http://localhost as secure, so only a deliberate
		// plain-HTTP setup needs EDGED_COOKIE_SECURE=false.
		CookieSecure: os.Getenv("EDGED_COOKIE_SECURE") != "false",
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type server struct {
	db *pgxpool.Pool
	// assetOpt turns an uploaded portal image into its stored form in the background (AD-58); created on first use.
	assetOptOnce sync.Once
	assetOpt     *assetOptimiser
	propsOnce    sync.Once
	props        *proposalStore
	// dbDefaultPassword warns while the database accepts its default superuser password.
	dbDefaultPassword *defaultDBPasswordCheck
	scd               *scdClient
	// ledgerC commits every erasure to both ledger copies before it is reported.
	ledgerC  ledgerClient
	netd     *netdClient
	sessions *sessionStore
	// pending holds sign-ins whose password was accepted but whose code is not yet entered.
	pending  pendingSignins
	mfa      mfaService
	tenantID string
	siteID   string
	secure   bool
	// reads records who opened or searched guest data, off the request path.
	reads *readRecorder

	// surfaces records every resource actually mounted, so the admin can ask rather than infer it from build flags.
	surfaces *mountedSurfaces
	// modCache holds scd's module resolver answer for a few seconds; nil only in unit tests that build a server by hand.
	modCache *moduleCache

	// commerce is always constructed but holds a nil repository while the master flag is off; commerceCfg gates the admin routes.
	commerce    *iamv2.CommerceAdmin
	commerceCfg iamv2.CommerceConfig
	// commerceRepo is the engine's repository; grace publication needs it directly because it derives system
	// catalogue rows through the audited boundary, which the operator-facing engine may not do.
	commerceRepo iamv2.CommerceAdminRepository
	// iamv2Cfg: credential issuance must land in the domain that will authenticate it, or an account could never log in.
	iamv2Cfg iamv2.Config

	// pmsCfg gates whether any Phase-3 route is mounted; while off the routes do not exist and edged issues no Phase-3 SQL.
	pmsCfg    iamv2.PMSConfig
	phase5Cfg iamv2.Phase5Config
	phase6    iamv2.Phase6Config

	// financialCfg gates the Manual Review routes, so an unmounted route cannot leak a schema that is not live.
	// It carries no transport and constructs no engine: edged is an operator API, not a sender.
	financialCfg posting.Config
	// financialEpoch is the startup outcome of the migration 0023 reconcile; empty when it could not be established.
	financialEpoch string
	// Where portald listens on this appliance; the zero value is the appliance's own.
	portalLB portalLoopback
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
		case "reset-mfa":
			if err := runResetMFA(os.Args[2:]); err != nil {
				slog.Error("reset-mfa failed", "err", err)
				os.Exit(1)
			}
			return
		case "serve", "":
		default:
			slog.Error("unknown subcommand", "arg", os.Args[1])
			os.Exit(2)
		}
	}

	// A persistently broken edged backs off exponentially instead of restart-storming.
	startupbackoff.Guard("edged")

	c := loadCfg()
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Site identity comes only from the verified signed assignment, never identity.json or env; until then edged awaits assignment.
	asgStore := &assignment.Store{Dir: envOr("EDGED_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
	var scope assignment.Resolution
	c.TenantID, c.SiteID, scope = resolveSiteScope(os.Getenv)
	if c.TenantID != "" && c.SiteID != "" {
		slog.Info("assignment resolved", "tenant_id", c.TenantID, "site_id", c.SiteID)
	} else {
		slog.Warn("awaiting assignment: edged running without a tenant/site", "assignment_outcome", scope.Outcome.String())
	}
	// Re-exec when the persisted assignment version changes.
	go watchAssignmentReexec(rootCtx, asgStore)

	pool, err := pgxpool.New(rootCtx, c.DBURL)
	if err != nil {
		slog.Error("db open", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	// The site DB may not be ready at cold boot. Retry with bounded backoff rather than exit: crash-looping would
	// hit systemd's start limit and wedge edged. After the window serve anyway; the pool keeps retrying per query.
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
		dbDefaultPassword: newDefaultDBPasswordCheck(pool.Config().ConnConfig.Host, pool.Config().ConnConfig.Port),
		db:                pool,
		scd:               newSCDClient(c.SCDSocket),
		netd:              newNetdClient(envOr("EDGED_NETD_SOCKET", "/run/stayconnect/netd.sock")),
		sessions:          newSessionStore(sessionStoreCap),
		tenantID:          c.TenantID,
		siteID:            c.SiteID,
		secure:            c.CookieSecure,
		surfaces:          newMountedSurfaces(),
		modCache:          &moduleCache{},
	}
	s.mfa = scdMFA{scd: s.scd, db: pool}
	s.ledgerC = &scdLedger{c: s.scd}

	// Phase 6 configuration must load before the commerce admin consumes s.phase6.AggregateTimeOn(); otherwise it
	// reads a zero-value config and the capability is off whatever the environment says.
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
	var commRepo iamv2.CommerceAdminRepository // nil while the master flag is off
	if commCfg.MasterEnabled {
		if pool == nil {
			slog.Error("phase2 commerce master flag enabled but no database pool is available")
			os.Exit(2)
		}
		commRepo = iamv2.NewPgCommerceAdminRepository(pool)
		// Converge the site on its hidden system grace package (idempotent; keeps the immutable revision history that live
		// entitlements pin). A failure is logged and must not abort startup: a missing grace package is the case
		// Emergency Grace covers, so refusing to serve would turn a degraded path into an outage.
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
	// Whether this build may publish AGGREGATE_ONLINE_TIME plan revisions; off, every new revision stays VALIDITY_WINDOW.
	applyAggregateTimeCapability(commAdmin, p6cfg)
	s.commerce = commAdmin
	s.commerceRepo = commRepo
	s.commerceCfg = commCfg
	// Fail closed on an auth config scd would refuse.
	iamAuthCfg, iamAuthErr := iamv2.LoadConfigFromEnv(os.Getenv, true)
	if iamAuthErr != nil {
		slog.Error("iamv2 auth config invalid", "err", iamAuthErr)
		os.Exit(1)
	}
	s.iamv2Cfg = iamAuthCfg
	slog.Info("phase2 dark commerce admin constructed", "flags", commCfg.SafeFlagSummary())

	finCfg, err := posting.LoadConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase-4 financial flags are incoherent", "err", err)
		os.Exit(2)
	}
	s.financialCfg = finCfg

	// The restore-rollback detector must run before any financial worker starts: after the flags load, before any route is mounted.
	s.financialEpoch = s.reconcileFinancialEpochAtStartup(rootCtx)

	pmsCfg, err := iamv2.LoadPMSConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase3 pms config", "err", err)
		os.Exit(2)
	}
	s.pmsCfg = pmsCfg

	p5cfg, err := iamv2.LoadPhase5ConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase-5 flags are incoherent", "err", err)
		os.Exit(2)
	}
	s.phase5Cfg = p5cfg
	slog.Info("phase5 dark post-stay admin surface", "flags", p5cfg.SafeFlagSummary())

	slog.Info("phase3 dark pms admin surface", "flags", pmsCfg.SafeFlagSummary())
	// Prove the controlled-writer boundary is in force before serving Phase 3, or raw writes would leave state with no provenance.
	if pmsCfg.Enabled() {
		if err := writerguard.Verify(rootCtx, pool, writerguard.Phase3Requirements()); err != nil {
			slog.Error("edged: refusing to serve the Phase-3 surface", "err", err)
			os.Exit(2)
		}
	}

	go s.healthMonitorLoop(rootCtx)

	s.reads = newReadRecorder(s.writeReadEntry)
	go s.reads.run(rootCtx, time.Second)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// No middleware.RealIP: it let a client choose RemoteAddr through True-Client-IP (audit P9 L5). clientIP trusts
	// the proxy's X-Real-IP only from a loopback peer.
	// No path, query or client address in the request log.
	r.Use(reqlog.Middleware)
	r.Use(middleware.Recoverer)
	r.Use(limitRequestBody)
	// 15 s for every request but the network applies, which wait for netd (route_timeout.go).
	r.Use(routeTimeout(defaultRequestTimeout, networkApplyTimeout))

	r.Route("/edge/v1", func(r chi.Router) {
		// Every state-changing request, login included, must come from the console's own origin.
		r.Use(sameOrigin)
		r.Use(noStore)
		r.Get("/health", s.health)
		r.Post("/auth/login", s.login)
		r.Post("/auth/login/code", s.loginCode)
		r.Post("/auth/logout", s.logout)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Use(s.recordGuestDataReads)
			r.Get("/auth/whoami", s.whoami)
			r.Get("/auth/keepalive", s.keepalive)
			r.Post("/auth/password", s.changeOwnPassword)
			r.Get("/auth/mfa", s.getOwnMFA)
			r.Post("/auth/mfa/recovery-codes", s.newOwnRecoveryCodes)
			r.Post("/auth/mfa/setup", s.setupOwnMFA)
			r.Post("/auth/mfa/confirm", s.confirmOwnMFA)
			r.Post("/auth/mfa/disable", s.disableOwnMFA)

			// Plain routes that bypass mountResource must still be recorded as surfaces, or /capabilities reports them unavailable.
			s.surfaces.add("license")
			s.surfaces.add("diagnostics")
			s.surfaces.add("reports")

			r.With(s.requireRole("license", permRead)).Get("/central/status", s.centralStatus)
			r.With(s.requireRole("license", permRead)).Post("/central/refresh", s.centralRefresh)
			r.With(s.requireAnyWrite("license", "network")).Get("/central/offline-request", s.centralOfflineRequest)
			r.With(s.requireAnyWrite("license", "network")).Post("/central/offline-package", s.centralOfflinePackage)
			r.With(s.requireRole("license", permWrite)).Post("/license", s.licenseInstall)

			// Authenticated but not role-filtered: the navigation reads it so it never offers a destination that would 404.
			r.Get("/capabilities", s.capabilities)

			// Same "network" role as the settings that own the management IP; Rotate steps up inside the handler.
			r.With(s.requireRole("network", permRead)).Get("/hotel-admin-cert", s.hotelAdminCertGet)
			r.With(s.requireRole("network", permWrite)).Post("/hotel-admin-cert/check", s.hotelAdminCertCheck)
			r.With(s.requireRole("network", permWrite)).Post("/hotel-admin-cert/rotate", s.hotelAdminCertRotate)

			mountResource(r, s, "operators", s.operatorsRoutes)
			// Always mounted: it is how an operator learns what the licence authorises and the site may use.
			mountResource(r, s, "modules", s.modulesRoutes)
			mountResource(r, s, "guest-accounts", s.guestAccountsRoutes)
			// The generated-password format is a property configuration decision, so its own key rather than guest-accounts write.
			mountResource(r, s, "account-password-settings", s.accountPasswordSettingsRoutes)
			mountResource(r, s, "sessions", s.sessionsRoutes)
			// Client devices share the sessions key (same live-access state); the daily device-change limit is a
			// configuration decision, so it sits under auth-methods.
			r.Route("/client-devices", func(r chi.Router) {
				r.Use(s.resourcePermission("sessions"))
				r.Mount("/", s.clientDevicesAdminRoutes())
			})
			r.Route("/device-settings", func(r chi.Router) {
				r.Use(s.resourcePermission("auth-methods"))
				r.Mount("/", s.deviceSettingsRoutes())
			})
			// One-time-code caps, the sign-in ceiling, the room-pause exemption, device retention and the uploaded-image limits (0170).
			r.Route("/signin-capacity-settings", func(r chi.Router) {
				r.Use(s.resourcePermission("auth-methods"))
				r.Mount("/", s.signinCapacityRoutes())
			})
			// Retention removes history when shortened, so it sits under the "backups" key (site administrators write).
			r.Route("/accounting-retention", func(r chi.Router) {
				r.Use(s.resourcePermission("backups"))
				r.Mount("/", s.accountingRetentionRoutes())
			})
			// Shortening a period anonymises history on the next run, hence the same "backups" key.
			r.Route("/data-retention", func(r chi.Router) {
				r.Use(s.resourcePermission("backups"))
				r.Mount("/", s.dataRetentionRoutes())
			})
			// Backup key holders: readers see the state, site administrators manage holders.
			r.Route("/backup-keys", func(r chi.Router) {
				r.Use(s.resourcePermission("backups"))
				r.Mount("/", s.backupKeysRoutes())
			})
			mountResource(r, s, "guest-data-rights", s.guestDataRightsRoutes)
			mountResource(r, s, "usage", s.usageRoutes)
			mountResource(r, s, "auth-methods", s.authMethodsRoutes)
			// Email-account terms are the same decision as switching the method on, so they use the auth-methods key.
			r.Route("/self-service-settings", func(r chi.Router) {
				r.Use(s.resourcePermission("auth-methods"))
				r.Mount("/", s.selfServiceSettingsRoutes())
			})
			// Reading a live code has its own key, separate from the list and "try again".
			mountResource(r, s, "code-requests", s.codeRequestsRoutes)
			mountResource(r, s, "code-reveal", s.codeRevealRoutes)
			mountResource(r, s, "walled-garden", s.walledGardenRoutes)
			mountResource(r, s, "client-groups", s.clientGroupsRoutes)
			mountResource(r, s, "portal-branding", s.brandingRoutes)
			mountResource(r, s, "portal-assets", s.portalAssetRoutes)
			// Card payment accounts: site-local, secrets sealed and write-only, served by scd which owns the key.
			mountResource(r, s, "payment-providers", s.paymentProvidersRoutes)
			mountResource(r, s, "notification-providers", s.notificationProvidersRoutes)
			mountResource(r, s, "social-providers", s.socialProvidersRoutes)
			if s.commerceCfg.AdminOn() {
				mountResource(r, s, "commercial-packages", s.commercialPackagesRoutes)
			}
			// Mounted only when the Phase-3 master and admin flags are both on; otherwise the paths do not exist.
			if s.pmsCfg.AdminOn() {
				mountResource(r, s, "pms-stays", s.pmsStaysRoutes)
				mountResource(r, s, "pms-events", s.pmsEventsRoutes)
				// Its own key: reading the activity log and answering it are different powers.
				mountResource(r, s, "pms-event-decisions", s.pmsEventDecisionsRoutes)
				mountResource(r, s, "pms-resolutions", s.pmsResolutionsRoutes)
				// Credential values hang off their own key, so a caller with only the list permission is refused by middleware.
				// Always register through mountResource: a route mounted directly inside requireAuth has no authorization.
				mountResource(r, s, "guest-signin-attempts", s.signInAttemptsRoutes)
				mountResource(r, s, "guest-signin-credentials", s.signInCredentialsRoutes)
				// Two keys: changing thresholds and ending one device's wait are different powers. Neither carries the credentials key.
				mountResource(r, s, "guest-signin-protection", s.signInProtectionRoutes)
				mountResource(r, s, "guest-signin-restrictions", s.signInRestrictionsRoutes)
				mountResource(r, s, "checkout-grace", s.checkoutGraceConfigRoutes)
				mountResource(r, s, "operational-alerts", s.operationalAlertsRoutes)
				mountResource(r, s, "pms-interfaces", s.pmsInterfacesRoutes)
				r.With(s.resourcePermission("pms-interfaces")).Get("/pms-providers", s.listPMSProviders)
				// Module-gated on room_charge.
				mountResource(r, s, "pms-financial-onboarding", s.pmsFinancialOnboardingRoutes)
				mountResource(r, s, "pms-routing", s.pmsRoutingRoutes)
				mountResource(r, s, "pms-source-conflicts", s.pmsSourceConflictsRoutes)
				// Read is wide (the desk asks which guests disagree); write hands an event back to ingestion, which is not a desk decision.
				mountResource(r, s, "pms-reconciliation", s.pmsReconciliationRoutes)
				// Separate key: reading a disagreement and bulk-closing stays are different powers.
				mountResource(r, s, "pms-roster-reconciliation", s.rosterReconciliationRoutes)
			}
			if s.phase5Cfg.AdminOn() {
				mountResource(r, s, "post-stay-profiles", s.postStayProfilesRoutes)
			}
			// Cross-PMS transfer has its own flag: ending a guest's access and moving it has a different blast radius than post-stay PINs.
			if s.phase5Cfg.TransferOn() {
				mountResource(r, s, "stay-transfers", s.stayTransfersRoutes)
			}
			// This mounts only the screen; the setting it edits is separate. It must go through mountResource so the role
			// matrix applies: routes registered directly in requireAuth let any operator change a guest-facing capability.
			if s.phase6.DeviceAdminOn() {
				mountResource(r, s, "guest-device-self-service", s.guestDeviceSelfServiceRoutes)
			}
			if s.financialCfg.ReviewOn() {
				mountResource(r, s, "financial-review", s.financialReviewRoutes)
				// Financial operations share the review permission and flag, so the whole surface appears together.
				mountResource(r, s, "financial-ops", s.financialOpsRoutes)
			} else {
				// Card disputes are reachable whatever the review flag.
				mountResource(r, s, "financial-review", s.cardCasesRoutes)
			}
			// Vouchers are mounted unconditionally: issuing against a published revision is not the power to publish one,
			// and a flag-dependent surface silently loses a destination. `voucher-codes` is the narrow key
			// (recovering a credential in the clear, with password step-up and a bounded reason).
			mountResource(r, s, "vouchers", s.vouchersRoutes)
			mountResource(r, s, "voucher-codes", s.voucherCodesRoutes)
			mountResource(r, s, "voucher-code-settings", s.voucherCodeSettingsRoutes)
			mountResource(r, s, "audit", s.auditRoutes)
			mountResource(r, s, "reports", s.reportsRoutes)
			mountResource(r, s, "backups", s.backupsRoutes)
			// A full database dump has its own key: site_admin and the IT manager only, behind password step-up.
			mountResource(r, s, "backup-download", s.backupDownloadRoutes)
			mountResource(r, s, "network", s.networkRoutes)
			// Its own key rather than "network": who may see or move the portal certificate is not inherited from the guest-network editor.
			mountResource(r, s, "portal-https", s.portalHTTPSRoutes)
			// Mounted at "diagnostics", not "health": the unauthenticated GET /edge/v1/health must not be shadowed.
			mountResource(r, s, "diagnostics", s.healthRoutes)
		})
	})

	// Management plane only: refuse wildcard addresses so a misconfiguration cannot expose Hotel Admin.
	if host, _, err := net.SplitHostPort(c.Addr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			slog.Error("EDGED_ADDR must bind a specific management address, not a wildcard", "addr", c.Addr)
			os.Exit(2)
		}
	}

	// ReadTimeout bounds a slow body (the largest is an 8 MB portal image); no WriteTimeout, backup downloads stream.
	srv := &http.Server{Addr: c.Addr, Handler: r, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute}
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
	// Flush pending guest-data reads before exiting.
	select {
	case <-s.reads.done:
	case <-shutCtx.Done():
	}
}

// mountResource mounts a resource under its permission key: reads need permRead, writes permWrite.
func mountResource(r chi.Router, s *server, name string, routes func() http.Handler) {
	// Recorded here so /capabilities cannot drift from what is mounted.
	s.surfaces.add(name)
	r.Route("/"+name, func(r chi.Router) {
		r.Use(s.resourcePermission(name))
		// Module-owned surfaces follow the licence and the site's choice; core ones pass through.
		r.Use(s.moduleGate(name))
		r.Mount("/", routes())
	})
}

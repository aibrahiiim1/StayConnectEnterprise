// netd is the privileged network-configuration daemon, the only component allowed to change appliance
// networking. It listens on a root-owned unix socket (never TCP) and applies, health-checks and auto-rolls-back
// guest-network configuration; edged proxies operator requests to it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/conntrack"
	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

var version = "0.1.0-netd"

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	// `netd flows-end --ip X` (flows.go) runs first so it needs no database, socket or backoff tracker.
	if len(os.Args) > 1 && os.Args[1] == "flows-end" {
		os.Exit(runFlowsEndCommand(os.Args[2:], os.Stdout, os.Stderr))
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Exponential crash-loop backoff (internal/startupbackoff).
	startupbackoff.Guard("netd")

	sock := envOr("NETD_SOCKET", "/run/stayconnect/netd.sock")
	dbURL := envOr("NETD_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect_site?sslmode=disable")
	dryRun := os.Getenv("NETD_DRY_RUN") == "true"

	topo := netcfg.DefaultTopology()
	topo.WANInterface = envOr("NETD_WAN_IFACE", topo.WANInterface)
	topo.MgmtInterface = envOr("NETD_MGMT_IFACE", topo.MgmtInterface)
	// Pins the admin UI to the mgmt IP (blocks it on a shared NIC's WAN address); empty keeps the interface-wide accept.
	topo.MgmtAddr = envOr("NETD_MGMT_ADDR", topo.MgmtAddr)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(rootCtx, dbURL)
	if err != nil {
		slog.Error("db open", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(rootCtx); err != nil {
		slog.Error("site db unreachable", "err", err)
		os.Exit(1)
	}

	st := &store{db: pool}
	confirmWindow := 120 * time.Second
	if v := os.Getenv("NETD_CONFIRM_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			confirmWindow = time.Duration(n) * time.Second
		}
	}
	ap := &applier{
		st:           st,
		kea:          newKeaClient(envOr("NETD_KEA_SOCKET", "/run/kea/kea4-ctrl-socket")),
		topo:         topo,
		generatedDir: envOr("NETD_GENERATED_DIR", "/etc/stayconnect/generated/network"),
		netplanFile:  envOr("NETD_NETPLAN_FILE", "/etc/netplan/50-stayconnect-guest.yaml"),
		unboundFrag:  envOr("NETD_UNBOUND_FRAG", "/etc/unbound/unbound.conf.d/stayconnect-guest.conf"),
		// Separate file because it changes at runtime (portal.go).
		unboundPortalFrag: envOr("NETD_UNBOUND_PORTAL_FRAG", "/etc/unbound/unbound.conf.d/stayconnect-portal.conf"),
		keaLeaseCSV:       envOr("NETD_KEA_LEASE_CSV", "/var/lib/kea/kea-leases4.csv"),
		keaSocket:         envOr("NETD_KEA_SOCKET", "/run/kea/kea4-ctrl-socket"),
		keaConfFile:       envOr("NETD_KEA_CONF", "/etc/kea/kea-dhcp4.conf"),
		confirmWindow:     confirmWindow,
		legacyBridge:      envOr("NETD_LEGACY_BRIDGE", "br-lan"),
		dryRun:            dryRun,
	}

	// Refresh the interface inventory at boot. Log the sync error: a swallowed failure (for example a missing
	// UPDATE grant) leaves guest-network validation reading a stale inventory and rejecting present parents.
	if ifaces, err := Discover(rootCtx); err == nil {
		if err := st.SyncInterfaces(rootCtx, ifaces, topo.MgmtInterface, topo.WANInterface); err != nil {
			slog.Error("interface inventory refresh failed; the inventory is now STALE and guest-network "+
				"validation reads it to decide which parents exist", "err", err)
		}
	} else {
		slog.Warn("interface discovery failed", "err", err)
	}

	// Boot reconciliation re-asserts the active revision onto the live OS: nftables loads only its static
	// config at boot, so otherwise the auth set and any bridge netplan did not recreate would drift from the DB.
	// A previous run that stopped mid-apply left its revision in 'applying', which holds the single in-flight slot against every later change and which
	// nothing else would ever move. It is rolled back first, then the confirmed configuration is re-asserted over whatever that left.
	ap.loadIncompleteMarker()
	ap.ReclaimInterruptedOnBoot(rootCtx)
	ap.ReconcileActiveOnBoot(rootCtx)

	// Portal runtime state (interface, DHCP option 114, resolver fragment follow scd's HTTPS verdict, which
	// changes with no revision). Never touches nftables or netplan. See portal.go.
	go ap.portalRuntimeLoop(rootCtx)
	// Re-asserts the ruleset while netd runs (nft_watch.go).
	go ap.nftStructureLoop(rootCtx, 10*time.Second)

	sn := &sysNetMgr{
		st:            st,
		wanIface:      topo.WANInterface,
		lanPhys:       envOr("NETD_LAN_PHYS", "ens192"),
		lanBr:         envOr("NETD_LEGACY_BRIDGE", "br-lan"),
		mgmtAddr:      topo.MgmtAddr,
		confirmWindow: confirmWindow,
		dryRun:        dryRun,
		// A WAN address change moves the pinned console address (NETD_MGMT_ADDR=auto); follow it once netplan applies.
		afterNetplan: func(ctx context.Context) { ap.reassertNftStructure(ctx, "system_network") },
		settle:       3 * time.Second,
	}

	// Phase 3 (ADR-0002): netd is the only process that mutates Phase-3 shaping and decides for itself whether
	// it is live, so a dark appliance refuses to mutate tc whatever another local process submits.
	p3mode, p3err := loadPhase3Mode(rootCtx, os.Getenv)
	if p3err != nil {
		slog.Error("netd: phase3 config fail-closed", "err", p3err)
		os.Exit(1)
	}
	p3authz := newShapingAuthz(os.Getenv)
	if p3mode.Active && !p3authz.configured {
		// Refuse to start rather than accept plans from anyone.
		slog.Error("netd: phase3 is live but NETD_PHASE3_PRODUCER_UID is unset — no producer can be authenticated")
		os.Exit(1)
	}
	if p3mode.Active {
		// Allocating generations and registering class origins is only meaningful if the writer-guard boundary is installed.
		if err := writerguard.Verify(rootCtx, pool, writerguard.Phase3Requirements()); err != nil {
			slog.Error("netd: refusing to run Phase-3 shaping", "err", err)
			os.Exit(1)
		}
	}
	p3shaping := &phase3Shaping{
		shp:   shape.New(),
		mode:  p3mode,
		authz: p3authz,
		store: &planStore{path: envOr("NETD_PHASE3_PLAN_STATE", "/var/lib/stayconnect/netd-phase3-plan.json")},
		// The class creator registers the accounting origin: only it can read the counters before a guest uses it (phase3_origin.go).
		origins:    &pgOrigins{pool: pool},
		classStore: &classStore{path: envOr("NETD_PHASE3_CLASS_STATE", "/var/lib/stayconnect/netd-phase3-classes.json")},
		// Write-ahead activation journal, fsynced before a guest is provisionally authorized (phase3_journal.go).
		journal: &activationJournal{path: envOr("NETD_PHASE3_ACTIVATION_JOURNAL",
			"/var/lib/stayconnect/netd-phase3-activation-journal.json")},
		// Monotonic clock for the activation bound; the wall clock can move backwards and lengthen it.
		secClock: newSecurityClock(),
		// Generations come from a durable, appliance-scoped allocator, never process memory or the clock.
		generations: &pgGenerations{pool: pool},
		// Packet-authorization gate: writes only the Phase-3 set, and revocation also flushes the address's
		// conntrack flows so access ends for open connections too.
		gate: newFlushingGate(newNFTGate(), conntrack.New()),
		// The legacy auth_ipv4 set is kept empty.
		legacy: newNFTLegacySet(),
		// Confirmed kernel result, so a Session is only `active` once both halves are in force.
		enforcement: &pgEnforcement{pool: pool, tenant: p3mode.TenantID, site: p3mode.SiteID},
		// DHCP decides who holds an address; without it a reassigned address kept the previous guest's entitlement (phase3_address_owner.go).
		owner: addressOwner{src: ap.kea},
		// Kea also locates its lease database so an evidence outage can name the artifact.
		leaseFiles: ap.kea,
	}
	// A persisted class is carried forward only when the kernel still has that exact slot under the same boot;
	// otherwise it is dropped so its successor allocates a fresh generation.
	bootID := readBootID(envOr("NETD_BOOT_ID_FILE", "/proc/sys/kernel/random/boot_id"))
	prevClasses, _, _ := p3shaping.classStore.load()
	inv, verified := kernelInventory(rootCtx, p3shaping.shp, bridgesIn(prevClasses))
	p3shaping.restore(prevClasses, bootID, inv, verified)
	// The activation journal is restored separately and unconditionally: a reboot drops classes but must not
	// forget unprovable activations, or rebooting would buy a fresh grace period.
	p3shaping.restoreAttempts(p3shaping.journal.load(p3mode.TenantID, p3mode.SiteID, p3mode.ApplianceID))
	if p3mode.Active {
		slog.Info("netd phase3 managed-class state restored",
			"persisted", len(prevClasses.Classes), "carried_forward", len(p3shaping.classes),
			"kernel_verified", verified, "note", p3shaping.restoreNote)
	}
	srv := &server{st: st, ap: ap, kea: ap.kea, topo: topo, sn: sn, phase3: p3shaping}
	// The scope is re-read while running so a reassignment needs no restart (phase3_scope_watch.go).
	go scopeWatch{
		p:    p3shaping,
		load: func(ctx context.Context) (phase3Mode, error) { return loadPhase3Mode(ctx, os.Getenv) },
		verifyBoundary: func(ctx context.Context) error {
			return writerguard.Verify(ctx, pool, writerguard.Phase3Requirements())
		},
	}.loop(rootCtx, 30*time.Second)
	slog.Info("netd phase3 shaping writer", "active", p3mode.Active, "producer_authenticated", p3authz.configured)

	// Watchdog: rolls back pending revisions past their deadline; also runs at boot to recover from a crash.
	go srv.watchdogLoop(rootCtx)

	// System-network: reconcile a stale unconfirmed change left by a crash, then run the auto-rollback watchdog.
	sn.ReconcileStalePendingOnBoot(rootCtx)
	go sn.sysnetWatchdog(rootCtx)

	// Every route names its callers (caller_authz.go); the Phase-3 routes keep a stricter producer-uid check (phase3_control.go).
	callers := loadCallerPolicy(os.Getenv)
	for _, pr := range callers.problems {
		slog.Warn("netd caller policy", "problem", pr)
	}
	srv.callers = callers
	r := srv.routes()

	_ = os.MkdirAll("/run/stayconnect", 0o755)
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
	_ = os.Chmod(sock, 0o660)
	if g, err := user.LookupGroup("stayconnect"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			_ = os.Chown(sock, 0, gid)
		}
	}

	// Every connection carries the kernel-reported peer identity so the Phase-3 handler never trusts a header.
	hs := &http.Server{
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if pc, ok := c.(*peerConn); ok && pc.err == nil {
				return context.WithValue(ctx, peerConnKey{}, pc.id)
			}
			return ctx
		},
	}
	go func() {
		slog.Info("netd listening", "socket", sock, "dry_run", dryRun, "version", version)
		if err := hs.Serve(&peerListener{Listener: ln, authz: p3authz}); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "err", err)
			stop()
		}
	}()

	<-rootCtx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutCtx)
}

// routes is netd's whole HTTP surface; the Phase-3 routes authenticate the single producer themselves.
func (s *server) routes() http.Handler {
	admin := func(h http.HandlerFunc) http.HandlerFunc { return s.callers.gate(classAdmin, h) }

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(180 * time.Second))
	r.Get("/v1/health", admin(s.health))
	r.Get("/v1/interfaces", admin(s.interfaces))
	r.Post("/v1/validate", admin(s.validate))
	r.Post("/v1/apply", admin(s.apply))
	r.Post("/v1/confirm", admin(s.confirm))
	r.Post("/v1/rollback", admin(s.rollback))
	r.Get("/v1/leases", admin(s.leases))
	r.Get("/v1/pending", admin(s.pending))
	r.Post("/v1/portal-ip/validate", admin(s.portalIPValidate))
	// The only Phase-3 tc mutation entry point (ADR-0002), authenticated in the handler against the producer uid.
	r.Post("/v1/phase3/shaping", s.phase3ShapingHandler)
	// Per-class generations: the only trustworthy answer to "did this counter series restart?"
	r.Get("/v1/phase3/shaping/epochs", s.phase3EpochsHandler)
	r.Post("/v1/flows/end", s.callers.gate(classFlows, s.flowsEnd))

	r.Get("/v1/system-network", admin(s.sysnetGet))
	r.Post("/v1/system-network/validate", admin(s.sysnetValidate))
	r.Post("/v1/system-network/apply", admin(s.sysnetApply))
	r.Post("/v1/system-network/confirm", admin(s.sysnetConfirm))
	r.Post("/v1/system-network/rollback", admin(s.sysnetRollback))
	r.Get("/v1/system-network/history", admin(s.sysnetHistory))
	r.Get("/v1/system-network/diagnostics", admin(s.sysnetDiagnostics))
	return r
}

type server struct {
	st   *store
	ap   *applier
	kea  *keaClient
	topo netcfg.Topology
	sn   *sysNetMgr
	// phase3 is the single Phase-3 shaping writer (ADR-0002); acctd derives the plan.
	phase3 *phase3Shaping
	// callers decides who may call each route and whose body-supplied actor is believed (caller_authz.go).
	callers callerPolicy
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	configured, healthy, detail := s.keaStatus(r.Context())
	// status-get answering is not evidence that leases can be read (a Kea with no lease manager still answers it).
	ev := s.phase3.evidenceStatus(r.Context())
	if configured && healthy && !ev.Available {
		healthy = false
		detail = "Kea answers its control socket but its LEASES CANNOT BE READ (" + ev.Fault + "): " + ev.Detail
	}
	writeJSON(w, 200, map[string]any{
		"service": "netd", "version": version,
		// Configured and healthy are separate so an intentionally idle Kea differs from a broken one.
		"kea_configured": configured,
		"kea_healthy":    healthy,
		"kea_detail":     detail,
		"phase3_shaping": s.phase3.statusWithEvidence(ev, true),
		// Whether an earlier rollback left the appliance off its confirmed configuration (further changes are refused), and whether the
		// file Kea starts from holds the confirmed configuration.
		"rollback_incomplete": s.ap.rollbackIncompleteStatus(),
		"kea_disk":            s.ap.keaDiskStatus(),
	})
}

// keaStatus separates "Kea has nothing to serve yet" from "Kea is broken". Kea is deliberately stopped until a
// guest network is applied and confirmed (it binds that bridge), and reporting that as a fault would hold boot
// convergence open forever. The prerequisite is the confirmed active revision, not draft rows.
func (s *server) keaStatus(ctx context.Context) (configured, healthy bool, detail string) {
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	id, _, err := s.st.CurrentActive(qctx)
	switch {
	case err != nil:
		// Unreadable prerequisite: fail towards reporting, so a broken Kea is never shown as "waiting".
		configured = true
	default:
		configured = id != ""
	}
	if !configured {
		return false, false, "waiting for client networking: Kea serves the LAN bridge, which exists only " +
			"once a client network has been applied and confirmed on this appliance"
	}
	if s.kea.Healthy() {
		return true, true, "Kea DHCP answering status-get on its control socket"
	}
	return true, false, "Kea DHCP is not answering its control socket, but a guest network is configured"
}

func (s *server) interfaces(w http.ResponseWriter, r *http.Request) {
	ifaces, err := Discover(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	// Log the sync error: a silent failure makes a present interface look absent in the wizard.
	if err := s.st.SyncInterfaces(r.Context(), ifaces, s.topo.MgmtInterface, s.topo.WANInterface); err != nil {
		slog.Error("interface inventory refresh failed; serving discovery without a fresh inventory", "err", err)
	}
	// Discover() leaves Role empty; overlay the operator-assigned role and protected flag from the inventory.
	if meta, err := s.st.InterfaceMeta(r.Context()); err == nil {
		for i := range ifaces {
			if m, ok := meta[ifaces[i].Name]; ok {
				ifaces[i].Role = m.Role
				ifaces[i].Protected = m.Protected
			} else if ifaces[i].Role == "" {
				ifaces[i].Role = "unused"
			}
		}
	}
	writeJSON(w, 200, map[string]any{"interfaces": ifaces})
}

type actorReq struct {
	Actor   string `json:"actor"`
	Summary string `json:"summary"`
}

func (s *server) validate(w http.ResponseWriter, r *http.Request) {
	var req actorReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	actor, _, _ := s.callers.actorFor(r.Context(), req.Actor, "", "")
	res, err := s.ap.Validate(changeCtx(r), req.Summary, actor)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, res)
}

func (s *server) apply(w http.ResponseWriter, r *http.Request) {
	var req actorReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	actor, _, _ := s.callers.actorFor(r.Context(), req.Actor, "", "")
	res, err := s.ap.Apply(changeCtx(r), req.Summary, actor)
	if err != nil {
		// A refusal that changed nothing (another change is running or awaiting confirmation, or an earlier rollback did not finish) is a 409.
		code := 500
		if errors.Is(err, errApplyBusy) || errors.Is(err, errChangeInFlight) || errors.Is(err, errRollbackIncomplete) {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	code := 200
	if res.State == "failed" {
		code = 422
	}
	writeJSON(w, code, res)
}

type idReq struct {
	RevisionID string `json:"revision_id"`
	Actor      string `json:"actor"`
}

func (s *server) confirm(w http.ResponseWriter, r *http.Request) {
	var req idReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	actor, _, _ := s.callers.actorFor(r.Context(), req.Actor, "", "")
	if err := s.ap.Confirm(changeCtx(r), req.RevisionID, actor); err != nil {
		code := 500
		if errors.Is(err, errNotPending) {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"state": "active"})
}

func (s *server) rollback(w http.ResponseWriter, r *http.Request) {
	var req idReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	actor, _, _ := s.callers.actorFor(r.Context(), req.Actor, "", "")
	if err := s.ap.Rollback(changeCtx(r), req.RevisionID, actor); err != nil {
		// A deliberate refusal (for example the confirmed active revision, which would tear down every guest network) is a 409, not a 500.
		if errors.Is(err, errRollbackRefused) || errors.Is(err, errApplyBusy) {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"state": "rolled_back"})
}

func (s *server) leases(w http.ResponseWriter, r *http.Request) {
	leases, err := s.kea.Leases()
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"leases": leases})
}

func (s *server) pending(w http.ResponseWriter, r *http.Request) {
	id, bundle, overdue, err := s.st.PendingRevision(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"revision_id": id, "bundle_path": bundle, "overdue": overdue})
}

func (s *server) watchdogLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			id, _, overdue, err := s.st.PendingRevision(ctx)
			if err == nil && id != "" && overdue {
				slog.Warn("watchdog: rolling back unconfirmed revision", "revision", id)
				s.ap.rollbackExclusive(ctx, id, "confirmation window elapsed")
			}
		}
	}
}

func extractDhcp4(raw []byte) (map[string]any, error) {
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, err
	}
	d, ok := full["Dhcp4"].(map[string]any)
	if !ok {
		return nil, errors.New("no Dhcp4 in file")
	}
	return d, nil
}

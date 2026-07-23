// netd — the privileged network-configuration daemon.
//
// netd is the ONLY component allowed to change appliance networking. It runs as
// root, listens on a protected unix socket (/run/stayconnect/netd.sock, group
// stayconnect, 0660), and exposes a small structured JSON API used by edged
// (the unprivileged Hotel Admin API). It renders guest-network configuration
// from the Site database, validates it (structural + Kea config-test + nft -c),
// applies it surgically and reversibly, health-checks it, and auto-rolls-back
// if the operator does not confirm within a safe window.
//
// netd is NEVER exposed on TCP. edged proxies operator requests to it.
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Adaptive crash-loop backoff (see internal/startupbackoff): a persistently
	// broken netd backs off exponentially instead of a fixed 2s restart storm.
	startupbackoff.Guard("netd")

	sock := envOr("NETD_SOCKET", "/run/stayconnect/netd.sock")
	dbURL := envOr("NETD_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect_site?sslmode=disable")
	dryRun := os.Getenv("NETD_DRY_RUN") == "true"

	topo := netcfg.DefaultTopology()
	topo.WANInterface = envOr("NETD_WAN_IFACE", topo.WANInterface)
	topo.MgmtInterface = envOr("NETD_MGMT_IFACE", topo.MgmtInterface)
	// Pin the admin UI to the mgmt IP (blocks it on a shared mgmt/WAN NIC's WAN
	// address). Empty keeps the interface-wide accept for separate-NIC installs.
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
		st:            st,
		kea:           newKeaClient(envOr("NETD_KEA_SOCKET", "/run/kea/kea4-ctrl-socket")),
		topo:          topo,
		generatedDir:  envOr("NETD_GENERATED_DIR", "/etc/stayconnect/generated/network"),
		netplanFile:   envOr("NETD_NETPLAN_FILE", "/etc/netplan/50-stayconnect-guest.yaml"),
		unboundFrag:   envOr("NETD_UNBOUND_FRAG", "/etc/unbound/unbound.conf.d/stayconnect-guest.conf"),
		keaLeaseCSV:   envOr("NETD_KEA_LEASE_CSV", "/var/lib/kea/kea-leases4.csv"),
		keaSocket:     envOr("NETD_KEA_SOCKET", "/run/kea/kea4-ctrl-socket"),
		confirmWindow: confirmWindow,
		legacyBridge:  envOr("NETD_LEGACY_BRIDGE", "br-lan"),
		dryRun:        dryRun,
	}

	// Refresh interface inventory at boot.
	if ifaces, err := Discover(rootCtx); err == nil {
		_ = st.SyncInterfaces(rootCtx, ifaces, topo.MgmtInterface, topo.WANInterface)
	} else {
		slog.Warn("interface discovery failed", "err", err)
	}

	// Boot reconciliation: re-assert the active revision's generated artifacts
	// onto the live OS. nftables loads its static /etc/nftables.conf on boot (the
	// IP-only auth set), so without this the concatenated auth set — and any
	// managed guest bridge netplan did not recreate — would silently drift from
	// the DB source of truth until the next apply.
	ap.ReconcileActiveOnBoot(rootCtx)

	sn := &sysNetMgr{
		st:            st,
		wanIface:      topo.WANInterface,
		lanPhys:       envOr("NETD_LAN_PHYS", "ens192"),
		lanBr:         envOr("NETD_LEGACY_BRIDGE", "br-lan"),
		mgmtAddr:      topo.MgmtAddr,
		confirmWindow: confirmWindow,
		dryRun:        dryRun,
	}

	// Phase 3 (ADR-0002): netd is the ONLY process that mutates Phase-3 shaping — and it decides for itself
	// whether Phase 3 is live. Deriving the mode here, from the same flags, enrollment identity and signed
	// assignment every other daemon uses, is what makes the kill switch real: a dark appliance refuses to
	// mutate tc no matter what any other local process submits.
	p3mode, p3err := loadPhase3Mode(rootCtx, os.Getenv)
	if p3err != nil {
		slog.Error("netd: phase3 config fail-closed", "err", p3err)
		os.Exit(1)
	}
	p3authz := newShapingAuthz(os.Getenv)
	if p3mode.Active && !p3authz.configured {
		// Live enforcement with no way to tell the producer from any other local process is not a degraded
		// mode, it is an unenforceable one. Refuse to start rather than accept plans from anyone.
		slog.Error("netd: phase3 is live but NETD_PHASE3_PRODUCER_UID is unset — no producer can be authenticated")
		os.Exit(1)
	}
	if p3mode.Active {
		// netd writes no Phase-3 table directly, but it DOES perform two authoritative operations — allocating
		// a class generation and registering a class origin — and both are only meaningful if the boundary
		// they belong to is actually installed. On a schema whose guards were never applied, netd would go on
		// allocating generations perfectly happily while nothing enforced that they were the only ones.
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
		// The accounting origin is registered by the process that creates the class — the only one that can
		// read its counters before a guest can use it (see phase3_origin.go).
		origins:    &pgOrigins{pool: pool},
		classStore: &classStore{path: envOr("NETD_PHASE3_CLASS_STATE", "/var/lib/stayconnect/netd-phase3-classes.json")},
		// Generations come from a durable, appliance-scoped allocator that reconciles against the
		// generations surviving accounting checkpoints actually pin — never from this process's memory and
		// never from the clock.
		generations: &pgGenerations{pool: pool},
	}
	// Continuity is PROVEN, not assumed: a persisted class is carried forward only when the kernel still has
	// that exact slot under the same boot. A class that was flushed, recreated by hand, or whose minor now
	// belongs to a different session is dropped so its successor allocates a fresh generation.
	bootID := readBootID(envOr("NETD_BOOT_ID_FILE", "/proc/sys/kernel/random/boot_id"))
	prevClasses, _ := p3shaping.classStore.load()
	inv, verified := kernelInventory(rootCtx, p3shaping.shp, bridgesIn(prevClasses))
	p3shaping.restore(prevClasses, bootID, inv, verified)
	if p3mode.Active {
		slog.Info("netd phase3 managed-class state restored",
			"persisted", len(prevClasses.Classes), "carried_forward", len(p3shaping.classes),
			"kernel_verified", verified, "note", p3shaping.restoreNote)
	}
	srv := &server{st: st, ap: ap, kea: ap.kea, topo: topo, sn: sn, phase3: p3shaping}
	slog.Info("netd phase3 shaping writer", "active", p3mode.Active, "producer_authenticated", p3authz.configured)

	// Watchdog: roll back any pending revision whose deadline has passed. Also
	// runs once at boot to recover from a crash during pending_confirmation.
	go srv.watchdogLoop(rootCtx)

	// System-network (WAN/LAN) safety: reconcile a stale unconfirmed change left
	// by a crash/reboot (carryover E), then run the in-process auto-rollback
	// watchdog (carryover D — fully audited).
	sn.ReconcileStalePendingOnBoot(rootCtx)
	go sn.sysnetWatchdog(rootCtx)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(90 * time.Second))
	r.Get("/v1/health", srv.health)
	r.Get("/v1/interfaces", srv.interfaces)
	r.Post("/v1/validate", srv.validate)
	r.Post("/v1/apply", srv.apply)
	r.Post("/v1/confirm", srv.confirm)
	r.Post("/v1/rollback", srv.rollback)
	r.Post("/v1/adopt", srv.adopt)
	r.Get("/v1/leases", srv.leases)
	r.Get("/v1/pending", srv.pending)
	// System (WAN/LAN) network management — the appliance's own base networking.
	// Phase 3: the ONLY Phase-3 tc mutation entry point on this appliance (ADR-0002).
	r.Post("/v1/phase3/shaping", srv.phase3ShapingHandler)
	// the TC owner's per-class generations: the only trustworthy answer to "did this counter series restart?"
	r.Get("/v1/phase3/shaping/epochs", srv.phase3EpochsHandler)

	r.Get("/v1/system-network", srv.sysnetGet)
	r.Post("/v1/system-network/validate", srv.sysnetValidate)
	r.Post("/v1/system-network/apply", srv.sysnetApply)
	r.Post("/v1/system-network/confirm", srv.sysnetConfirm)
	r.Post("/v1/system-network/rollback", srv.sysnetRollback)
	r.Get("/v1/system-network/history", srv.sysnetHistory)
	r.Get("/v1/system-network/diagnostics", srv.sysnetDiagnostics)

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

	// Every accepted connection carries the kernel's statement of who is on the other end, so the Phase-3
	// handler can authenticate its caller instead of believing a header.
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

type server struct {
	st   *store
	ap   *applier
	kea  *keaClient
	topo netcfg.Topology
	sn   *sysNetMgr
	// phase3 is the SINGLE Phase-3 shaping writer (ADR-0002). acctd derives the plan; netd applies it.
	phase3 *phase3Shaping
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"service": "netd", "version": version,
		"kea_healthy": s.kea.Healthy(),
		// Phase-3 shaping is reported here because netd is its only writer (ADR-0002): if a submitted plan
		// could not be put in force, this is where it becomes visible.
		"phase3_shaping": s.phase3.status(),
	})
}

func (s *server) interfaces(w http.ResponseWriter, r *http.Request) {
	ifaces, err := Discover(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	_ = s.st.SyncInterfaces(r.Context(), ifaces, s.topo.MgmtInterface, s.topo.WANInterface)
	// Overlay the operator-assigned role/protected flag from the inventory so the
	// UI can tell which interfaces may parent a guest network. Discover() alone
	// leaves Role empty, which made every interface non-selectable in the wizard.
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
	res, err := s.ap.Validate(r.Context(), req.Summary, req.Actor)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, res)
}

func (s *server) apply(w http.ResponseWriter, r *http.Request) {
	var req actorReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	res, err := s.ap.Apply(r.Context(), req.Summary, req.Actor)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
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
	if err := s.ap.Confirm(r.Context(), req.RevisionID, req.Actor); err != nil {
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
	if err := s.ap.Rollback(r.Context(), req.RevisionID, req.Actor); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"state": "rolled_back"})
}

// adopt records the current DB intent as an already-active revision WITHOUT
// applying anything — used for legacy import (the running system already
// matches) and to seed the first known-good rollback target.
func (s *server) adopt(w http.ResponseWriter, r *http.Request) {
	var req actorReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	intent, err := s.st.LoadIntent(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	id, seq, err := s.st.CreateRevision(r.Context(), req.Summary+" (adopt)", intent, req.Actor)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	bundle := s.ap.generatedDir + "/revision-" + pad6(seq)
	if err := s.ap.generate(bundle, intent); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	_ = s.st.MarkApplying(r.Context(), id, bundle, req.Actor, nil)
	_ = s.st.markActiveAdopt(r.Context(), id, req.Actor)
	writeJSON(w, 200, map[string]any{"revision_id": id, "seq": seq, "state": "active", "adopted": true})
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

// watchdogLoop rolls back any pending revision past its confirm deadline.
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
				s.ap.rollback(ctx, id, "confirmation window elapsed")
			}
		}
	}
}

func pad6(n int64) string {
	s := strconv.FormatInt(n, 10)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
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

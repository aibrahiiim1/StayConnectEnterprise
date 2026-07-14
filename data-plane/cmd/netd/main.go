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
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
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

	srv := &server{st: st, ap: ap, kea: ap.kea, topo: topo, sn: sn}

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

	hs := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("netd listening", "socket", sock, "dry_run", dryRun, "version", version)
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

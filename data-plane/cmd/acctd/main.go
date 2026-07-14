// acctd — Accounting Daemon.
//
// Every tick (default 1s):
//   1. Snapshot byte counters from tc for every guest-session class.
//   2. Compute per-IP deltas against the previous snapshot.
//   3. Write accounting_records rows + update sessions.bytes_up/bytes_down.
//   4. Enforce quotas:
//        - elapsed seconds > ticket_templates.duration_seconds  -> revoke (quota_time)
//        - bytes_up+bytes_down > ticket_templates.data_cap_bytes -> revoke (quota_bytes)
//      Revoke is done by POSTing to scd's Unix socket so nft/tc are cleaned up.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
	"github.com/stayconnect/enterprise/data-plane/internal/livez"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
)

type cfg struct {
	DBURL        string
	ScdSocket    string
	TickSeconds  int
	TenantID     string
	ApplianceID  string
	LegacyBridge string
}

func loadCfg() cfg {
	return cfg{
		DBURL:        envOr("ACCTD_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable"),
		ScdSocket:    envOr("ACCTD_SCD_SOCKET", "/run/stayconnect/scd.sock"),
		TickSeconds:  envInt("ACCTD_TICK_SECONDS", 1),
		TenantID:     os.Getenv("ACCTD_TENANT_ID"),
		ApplianceID:  os.Getenv("ACCTD_APPLIANCE_ID"),
		LegacyBridge: envOr("ACCTD_LEGACY_BRIDGE", "br-lan"),
	}
}
func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return d
}

type snapshot map[string]snapEntry

type snapEntry struct {
	BytesUp   uint64
	BytesDown uint64
}

type acctd struct {
	db           *pgxpool.Pool
	shp          *shape.Client
	scd          *http.Client
	tenantID     string
	applID       string
	legacyBridge string
	prev         snapshot
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Adaptive crash-loop backoff (see internal/startupbackoff).
	startupbackoff.Guard("acctd")

	c := loadCfg()

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Accounting rows are attributed to a CUSTOMER, so the tenant/appliance identity
	// must come from the same source of truth as everything else on the appliance:
	//   appliance_id -> identity.json (enrollment)
	//   tenant_id    -> the vendor-signed ASSIGNMENT document
	// The legacy ACCTD_TENANT_ID/ACCTD_APPLIANCE_ID env vars are a migration-only
	// fallback: leaving them hard-wired meant a re-assigned appliance kept billing
	// usage to the PREVIOUS customer.
	idStore := &identity.Store{Dir: envOr("ACCTD_IDENTITY_DIR", "/etc/stayconnect/identity")}
	if ident, err := idStore.LoadOrEnroll(rootCtx, "", "", "", false); err == nil && ident != nil {
		c.ApplianceID = ident.ApplianceID
	}
	asgStore := &assignment.Store{Dir: envOr("ACCTD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
	if aTen, _, _, _ := asgStore.Resolved(); aTen != "" {
		c.TenantID = aTen
	} else {
		c.TenantID = "" // unassigned appliance bills nobody
	}
	if c.TenantID == "" || c.ApplianceID == "" {
		slog.Warn("acctd: appliance not enrolled/assigned — accounting paused until a signed assignment arrives")
		// Wait for an assignment, then re-exec into the normal path.
		waitForAssignment(rootCtx, asgStore)
		return
	}
	slog.Info("acctd identity resolved", "tenant_id", c.TenantID, "appliance_id", c.ApplianceID)
	// Adopt a re-assignment without manual intervention.
	go watchAssignmentReexec(rootCtx, asgStore)

	pool, err := pgxpool.New(rootCtx, c.DBURL)
	if err != nil {
		slog.Error("db open", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	a := &acctd{
		db:           pool,
		shp:          shape.New(),
		scd:          newUnixClient(c.ScdSocket),
		tenantID:     c.TenantID,
		applID:       c.ApplianceID,
		legacyBridge: c.LegacyBridge,
		prev:         snapshot{},
	}

	tick := time.NewTicker(time.Duration(c.TickSeconds) * time.Second)
	defer tick.Stop()

	slog.Info("acctd started", "tick_s", c.TickSeconds)
	for {
		select {
		case <-rootCtx.Done():
			return
		case <-tick.C:
			if err := a.loop(rootCtx); err != nil {
				slog.Error("loop", "err", err)
			}
			// Liveness heartbeat: proves the accounting loop is PROGRESSING (not
			// just that the process is up) for the edged health supervisor.
			livez.Touch("acctd")
		}
	}
}

func newUnixClient(socketPath string) *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: tr, Timeout: 3 * time.Second}
}

// activeSession is one accountable session with its network placement and quota.
type activeSession struct {
	id, tid, vid       string
	ip                 net.IP
	bridge             string
	dataCap            int64
	durSec             int
	startedAt          time.Time
	totalUp, totalDown int64
}

func (a *acctd) loop(ctx context.Context) error {
	sessions, err := a.loadActive(ctx)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		a.prev = snapshot{}
		return nil
	}

	// Read tc counters once per device. Download counts live on the guest
	// bridge (dst = guest IP); upload counts live on the bridge's IFB
	// (src = guest IP, captured pre-SNAT via the ingress redirect). Each
	// device serves exactly one guest subnet, so (device, minor) uniquely
	// identifies a session — traffic can never be attributed across networks.
	downCache := map[string]map[int]shape.ClassBytes{}
	upCache := map[string]map[int]shape.ClassBytes{}
	readDown := func(bridge string) map[int]shape.ClassBytes {
		if m, ok := downCache[bridge]; ok {
			return m
		}
		m, _ := a.shp.ReadClasses(ctx, bridge)
		downCache[bridge] = m
		return m
	}
	readUp := func(bridge string) map[int]shape.ClassBytes {
		ifb := shape.IFBName(bridge)
		if m, ok := upCache[ifb]; ok {
			return m
		}
		m, _ := a.shp.ReadClasses(ctx, ifb)
		upCache[ifb] = m
		return m
	}

	now := time.Now()
	next := snapshot{}

	for _, s := range sessions {
		minor, ok := shape.MinorForIP(s.ip)
		if !ok {
			continue
		}
		curUp := readUp(s.bridge)[minor].Bytes
		curDown := readDown(s.bridge)[minor].Bytes
		next[s.id] = snapEntry{BytesUp: curUp, BytesDown: curDown}

		prev, seen := a.prev[s.id]
		if !seen {
			// First observation of this session (fresh auth, or an acctd/scd
			// restart, or a reboot that rebuilt the class). Adopt the current
			// counter as the baseline and write nothing, so already-persisted
			// totals are never double-counted. Subsequent ticks measure deltas.
			continue
		}

		dUp := int64(curUp) - int64(prev.BytesUp)
		dDown := int64(curDown) - int64(prev.BytesDown)
		if dUp < 0 { // class was re-created (counter reset) — count from zero
			dUp = int64(curUp)
		}
		if dDown < 0 {
			dDown = int64(curDown)
		}

		if dUp != 0 || dDown != 0 {
			_, _ = a.db.Exec(ctx, `
				INSERT INTO accounting_records (ts, session_id, tenant_id, appliance_id, bytes_up, bytes_down)
				VALUES ($1, $2, $3, $4, $5, $6)
			`, now, s.id, s.tid, a.applID, dUp, dDown)

			s.totalUp += dUp
			s.totalDown += dDown
			_, _ = a.db.Exec(ctx, `
				UPDATE sessions SET bytes_up = $2, bytes_down = $3, last_activity_at = $4
				 WHERE id = $1
			`, s.id, s.totalUp, s.totalDown, now)
		}

		// Quota enforcement (bytes + time).
		if s.vid != "" {
			usedBytes := s.totalUp + s.totalDown
			elapsed := int(now.Sub(s.startedAt).Seconds())
			if s.dataCap > 0 && usedBytes >= s.dataCap {
				a.revoke(ctx, s.ip.String(), "quota_bytes")
				continue
			}
			if s.durSec > 0 && elapsed >= s.durSec {
				a.revoke(ctx, s.ip.String(), "quota_time")
				continue
			}
		}
	}

	// prev is replaced (not merged) so revoked/closed sessions drop out and
	// their baselines don't linger.
	a.prev = next
	return nil
}

// loadActive returns every active session for this tenant with its network
// placement (ingress bridge) and quota limits.
func (a *acctd) loadActive(ctx context.Context) ([]activeSession, error) {
	rows, err := a.db.Query(ctx, `
		SELECT s.id, s.tenant_id, COALESCE(s.voucher_id::text,''),
		       host(s.ip), COALESCE(s.ingress_interface, ''),
		       s.started_at, s.bytes_up, s.bytes_down,
		       COALESCE(t.data_cap_bytes, 0), COALESCE(t.duration_seconds, 0)
		  FROM sessions s
		  LEFT JOIN vouchers v ON v.id = s.voucher_id
		  LEFT JOIN ticket_templates t ON t.id = v.template_id
		 WHERE s.tenant_id = $1
		   AND s.state = 'active'
	`, a.tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activeSession
	for rows.Next() {
		var s activeSession
		var ipStr, bridge string
		if err := rows.Scan(&s.id, &s.tid, &s.vid, &ipStr, &bridge,
			&s.startedAt, &s.totalUp, &s.totalDown, &s.dataCap, &s.durSec); err != nil {
			return nil, err
		}
		s.ip = net.ParseIP(ipStr)
		if s.ip == nil {
			continue
		}
		if bridge == "" {
			bridge = a.legacyBridge
		}
		s.bridge = bridge
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *acctd) revoke(ctx context.Context, ip, reason string) {
	body, _ := json.Marshal(map[string]string{"ip": ip, "reason": reason})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		"http://unix/v1/sessions/revoke", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.scd.Do(req)
	if err != nil {
		slog.Error("revoke", "ip", ip, "err", err)
		return
	}
	defer resp.Body.Close()
	slog.Info("revoked", "ip", ip, "reason", reason, "status", resp.StatusCode)
	// prev is keyed by session id and fully replaced each tick, so a revoked
	// session drops out naturally once it leaves the active set.
}

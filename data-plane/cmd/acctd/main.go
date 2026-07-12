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
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

type cfg struct {
	DBURL       string
	ScdSocket   string
	TickSeconds int
	TenantID    string
	ApplianceID string
}

func loadCfg() cfg {
	return cfg{
		DBURL:       envOr("ACCTD_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable"),
		ScdSocket:   envOr("ACCTD_SCD_SOCKET", "/run/stayconnect/scd.sock"),
		TickSeconds: envInt("ACCTD_TICK_SECONDS", 1),
		TenantID:    os.Getenv("ACCTD_TENANT_ID"),
		ApplianceID: os.Getenv("ACCTD_APPLIANCE_ID"),
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
	db       *pgxpool.Pool
	shp      *shape.Client
	scd      *http.Client
	tenantID string
	applID   string
	prev     snapshot
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

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
	if ident, err := idStore.LoadOrEnroll(rootCtx, "", "", ""); err == nil && ident != nil {
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
		db:       pool,
		shp:      shape.New(),
		scd:      newUnixClient(c.ScdSocket),
		tenantID: c.TenantID,
		applID:   c.ApplianceID,
		prev:     snapshot{},
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

func (a *acctd) loop(ctx context.Context) error {
	stats, err := a.shp.Stats(ctx)
	if err != nil {
		return err
	}

	// Combine WAN (bytes_up) and LAN (bytes_down) into per-IP snapshot.
	cur := snapshot{}
	for _, s := range stats {
		key := s.IP.String()
		e := cur[key]
		switch s.Iface {
		case shape.WANIface:
			e.BytesUp = s.Bytes
		case shape.LANIface:
			e.BytesDown = s.Bytes
		}
		cur[key] = e
	}

	now := time.Now()

	for ipStr, cur := range cur {
		prev := a.prev[ipStr]
		dUp := int64(cur.BytesUp) - int64(prev.BytesUp)
		dDown := int64(cur.BytesDown) - int64(prev.BytesDown)
		if dUp < 0 {
			dUp = int64(cur.BytesUp)
		}
		if dDown < 0 {
			dDown = int64(cur.BytesDown)
		}

		if dUp == 0 && dDown == 0 && a.prev[ipStr] == (snapEntry{}) {
			// first observation, no delta to write
			continue
		}

		sid, tid, vid, dataCap, durSec, startedAt, totalUp, totalDown, found, err := a.lookupActive(ctx, ipStr)
		if err != nil {
			slog.Warn("lookup active", "ip", ipStr, "err", err)
			continue
		}
		if !found {
			continue
		}

		_, _ = a.db.Exec(ctx, `
			INSERT INTO accounting_records (ts, session_id, tenant_id, appliance_id, bytes_up, bytes_down)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, now, sid, tid, a.applID, dUp, dDown)

		newUp := totalUp + dUp
		newDown := totalDown + dDown
		_, _ = a.db.Exec(ctx, `
			UPDATE sessions SET bytes_up = $2, bytes_down = $3, last_activity_at = $4
			 WHERE id = $1
		`, sid, newUp, newDown, now)

		// Quota check.
		if vid != "" {
			usedBytes := newUp + newDown
			elapsed := int(now.Sub(startedAt).Seconds())
			if dataCap > 0 && usedBytes >= dataCap {
				a.revoke(ctx, ipStr, "quota_bytes")
				continue
			}
			if durSec > 0 && elapsed >= durSec {
				a.revoke(ctx, ipStr, "quota_time")
				continue
			}
		}
	}

	a.prev = cur
	return nil
}

// lookupActive returns session + quota values for the active session matching ip.
func (a *acctd) lookupActive(ctx context.Context, ip string) (
	sid, tid, vid string,
	dataCap int64, durSec int,
	startedAt time.Time,
	totalUp, totalDown int64,
	found bool, err error,
) {
	err = a.db.QueryRow(ctx, `
		SELECT s.id, s.tenant_id, COALESCE(s.voucher_id::text,''), s.started_at,
		       s.bytes_up, s.bytes_down,
		       COALESCE(t.data_cap_bytes, 0), COALESCE(t.duration_seconds, 0)
		  FROM sessions s
		  LEFT JOIN vouchers v ON v.id = s.voucher_id
		  LEFT JOIN ticket_templates t ON t.id = v.template_id
		 WHERE s.tenant_id = $1
		   AND s.ip = $2::inet
		   AND s.state = 'active'
		 ORDER BY s.started_at DESC
		 LIMIT 1
	`, a.tenantID, ip).Scan(&sid, &tid, &vid, &startedAt, &totalUp, &totalDown, &dataCap, &durSec)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", "", 0, 0, time.Time{}, 0, 0, false, nil
		}
		return
	}
	return sid, tid, vid, dataCap, durSec, startedAt, totalUp, totalDown, true, nil
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
	delete(a.prev, ip)
}

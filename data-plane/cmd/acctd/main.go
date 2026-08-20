// acctd — Accounting Daemon.
//
// Every tick (default 1s):
//  1. Snapshot byte counters from tc for every guest-session class.
//  2. Compute per-IP deltas against the previous snapshot.
//  3. Write accounting_records rows + update sessions.bytes_up/bytes_down.
//  4. Enforce quotas:
//     - elapsed seconds > ticket_templates.duration_seconds  -> revoke (quota_time)
//     - bytes_up+bytes_down > ticket_templates.data_cap_bytes -> revoke (quota_bytes)
//     Revoke is done by POSTing to scd's Unix socket so nft/tc are cleaned up.
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
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
	"github.com/stayconnect/enterprise/data-plane/internal/livez"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
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
	// p3 is the Phase-3 arm (nil while dark).
	p3 *phase3
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
	assignedSite := ""
	assignedGen := int64(0)
	if aTen, aSite, _, aVer := asgStore.Resolved(); aTen != "" {
		c.TenantID = aTen
		assignedSite = aSite
		assignedGen = aVer
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

	// Phase 3 (DARK): the enforcement arm is constructed only when the master + checkout-grace flags are on.
	// While dark p3 is nil, every call on it is a no-op, and acctd issues zero Phase-3 queries.
	pmsCfg, err := iamv2.LoadPMSConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("acctd: phase3 config fail-closed", "err", err)
		os.Exit(1)
	}
	// The controlled-writer boundary must be REAL before this process writes anything Phase-3. A schema whose
	// guards were never applied accepts raw writes silently, and a process connected as the operations' owner
	// satisfies every guard trivially — both are "Phase 3 is running" with none of its guarantees.
	if pmsCfg.Enabled() {
		if err := writerguard.Verify(rootCtx, pool, writerguard.Phase3Requirements()); err != nil {
			slog.Error("acctd: refusing to start", "err", err)
			os.Exit(1)
		}
	}

	// The shaping contract is scoped to THIS appliance under THIS assignment: netd checks a submitted plan
	// against its own copy of the same facts, so a plan derived for another site can never be applied here.
	p3scope := planScope{TenantID: c.TenantID, SiteID: assignedSite, ApplianceID: c.ApplianceID,
		AssignmentGe: assignedGen}
	if rec, err := asgStore.Load(); err == nil && rec != nil && rec.Current != nil {
		p3scope.AssignmentID = rec.Current.AssignmentID
	}
	// The plan generation is durable: a restarted producer that began again at 1 would have every plan
	// correctly refused as stale, freezing enforcement at the pre-restart state with nothing looking broken.
	p3plans := newPlanCounter(envOr("ACCTD_PHASE3_PLAN_STATE", "/var/lib/stayconnect/acctd-phase3-plan.json"))
	p3plans.start()
	a.p3 = newPhase3(pmsCfg, a, c.TenantID, assignedSite, p3scope, p3plans)
	// ADR-0002: acctd derives the plan; netd is the ONLY process that mutates Phase-3 tc state.
	netdShaping := newNetdShaper(envOr("ACCTD_NETD_SOCKET", "/run/stayconnect/netd.sock"))
	p3 := a.p3
	// PHASE 6: the accounting owner of last resort. Constructed only when the Phase-3 arm is absent, so a
	// normal deployment sweeps once per tick -- and an appliance whose Phase-3 flags are off still accounts
	// for any aggregate entitlement it has already granted, instead of letting a finite budget become
	// unlimited because of a flag belonging to another phase.
	aggOwner := newAggregateOwner(p3, a.db, c.TenantID, assignedSite,
		aggregateChargeBoundSeconds(c.TickSeconds))
	slog.Info("acctd phase3 arm", "flags", pmsCfg.SafeFlagSummary(), "active", p3 != nil,
		"phase6_fallback_accounting", aggOwner != nil,
		"accounting_owner", map[bool]string{true: "phase3", false: "legacy"}[p3.ownsAccounting()])

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
			// Phase 3 runs on the same tick over its OWN session domain: measure first (so this tick's usage
			// is attributed before anything is closed out), then enforce expiry at its true time, then submit
			// the derived plan to netd — the single shaping writer. All three are no-ops while dark.
			if n := p3.accountingPass(rootCtx, a.shp, netdShaping, c.LegacyBridge, time.Now()); n > 0 {
				slog.Debug("phase3: accounting samples ingested", "count", n)
			}
			p3.enforceExpiries(rootCtx)
			aggOwner.sweep(rootCtx)
			p3.reconcileShaping(rootCtx, netdShaping, c.LegacyBridge)
			// Liveness heartbeat: proves the accounting loop is PROGRESSING (not
			// just that the process is up) for the edged health supervisor — together with WHY it is
			// degraded, if it is. A ticking loop whose every observation is refused is not healthy, and
			// the heartbeat alone cannot say so.
			livez.Touch("acctd")
			livez.Report("acctd", p3.degradedSummary())
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
	// voucherBytes is the AGGREGATE bytes across every session of this session's
	// voucher, as of this tick's load — the data cap is enforced on this
	// aggregate so multiple devices on one voucher share (not multiply) the cap.
	voucherBytes int64
}

// loop is retained as the accounting tick's entry point, but the LEGACY PASS IS GONE.
//
// It used to read tc counters for every row in public.sessions, write public.accounting_records, update the
// session's byte columns and enforce the voucher's data/time quota by calling back into scd. All of that
// belonged to the superseded session domain: the quotas came from ticket_templates through vouchers, and the
// records were keyed by a public.sessions id.
//
// The current authority is Phase 3. netd is the only shaping writer (ADR-0002); phase3.accountingPass reads
// each live iam_v2.sessions row's absolute counters and submits them through the controlled ingest, and
// entitlement exhaustion -- not a per-voucher byte cap -- is what ends access, decided by internal/enforce.
//
// Nothing replaces the legacy pass here because something already had: the two ran side by side, and the
// legacy one stood down whenever Phase 3 owned accounting. Removing it makes permanent the state the system
// was already reaching at runtime, and removes the possibility of two writers disagreeing about one guest's
// usage.
func (a *acctd) loop(ctx context.Context) error {
	a.prev = snapshot{}
	return nil
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

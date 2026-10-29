// acctd is the accounting daemon.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/livez"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

type cfg struct {
	DBURL        string
	TickSeconds  int
	TenantID     string
	ApplianceID  string
	LegacyBridge string
}

// loadCfg requires ACCTD_DB_URL (audit P6 L14): a built-in fallback carried a published password and turned
// TLS off, so a missing env file silently connected with development credentials.
func loadCfg(getenv func(string) string) (cfg, error) {
	c := cfg{
		DBURL:        getenv("ACCTD_DB_URL"),
		TickSeconds:  tickSeconds(getenv),
		LegacyBridge: envOr("ACCTD_LEGACY_BRIDGE", "br-lan"),
	}
	if c.DBURL == "" {
		return c, errors.New("ACCTD_DB_URL is not set; acctd has no built-in database URL")
	}
	return c, nil
}
func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// The tick is bounded: netd admits a new guest on a short provisional lease that only the next plan submission
// renews and a plan is current for planValidity, so a tick of a minute or more would drop every guest offline
// each cycle without failing loudly. Out-of-range values are clamped with a warning.
const (
	minTickSeconds = 1
	maxTickSeconds = 10
)

func tickSeconds(getenv func(string) string) int {
	raw := getenv("ACCTD_TICK_SECONDS")
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	switch {
	case err != nil:
		slog.Warn("acctd: ACCTD_TICK_SECONDS is not a number; using 1", "value", raw)
		return 1
	case n < minTickSeconds:
		slog.Warn("acctd: ACCTD_TICK_SECONDS below the minimum; clamped", "value", n, "used", minTickSeconds)
		return minTickSeconds
	case n > maxTickSeconds:
		slog.Warn("acctd: ACCTD_TICK_SECONDS above the maximum; clamped", "value", n, "used", maxTickSeconds)
		return maxTickSeconds
	}
	return n
}

type acctd struct {
	db           *pgxpool.Pool
	shp          *shape.Client
	tenantID     string
	applID       string
	legacyBridge string
	p3           *phase3 // nil while dark
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	startupbackoff.Guard("acctd")

	c, err := loadCfg(os.Getenv)
	if err != nil {
		slog.Error("acctd: refusing to start", "err", err)
		os.Exit(1)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Tenant and appliance identity come from the verified assignment and registered identity (scope.go), never
	// the environment: a hard-wired env var would keep billing a re-assigned appliance's usage to the previous customer.
	asgStore := &assignment.Store{Dir: envOr("ACCTD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
	scope := resolveAcctdScope(os.Getenv)
	c.TenantID, c.ApplianceID = scope.TenantID, scope.ApplianceID
	assignedSite := scope.SiteID
	assignedGen := scope.Version
	if c.TenantID == "" || c.ApplianceID == "" {
		slog.Warn("acctd: appliance not registered/assigned — accounting paused until a verified signed assignment arrives",
			"assignment_outcome", scope.Outcome.String())
		waitForAssignment(rootCtx, func() bool { return resolveAcctdScope(os.Getenv).TenantID != "" })
		return
	}
	slog.Info("acctd identity resolved", "tenant_id", c.TenantID, "appliance_id", c.ApplianceID)
	go watchAssignmentReexec(rootCtx, asgStore, assignedGen)

	pool, err := pgxpool.New(rootCtx, c.DBURL)
	if err != nil {
		slog.Error("db open", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	a := &acctd{
		db:           pool,
		shp:          shape.New(),
		tenantID:     c.TenantID,
		applID:       c.ApplianceID,
		legacyBridge: c.LegacyBridge,
	}

	// The Phase-3 arm exists only when its flags are on; while dark p3 is nil and every call on it is a no-op.
	pmsCfg, err := iamv2.LoadPMSConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("acctd: phase3 config fail-closed", "err", err)
		os.Exit(1)
	}
	// The controlled-writer boundary must be real before any Phase-3 write: a schema without the guards accepts
	// raw writes silently, and a connection as the operations' owner satisfies every guard trivially.
	if pmsCfg.Enabled() {
		if err := writerguard.Verify(rootCtx, pool, writerguard.Phase3Requirements()); err != nil {
			slog.Error("acctd: refusing to start", "err", err)
			os.Exit(1)
		}
	}

	// Plans are scoped to this appliance and assignment; netd checks them against its own copy.
	p3scope := planScope{TenantID: c.TenantID, SiteID: assignedSite, ApplianceID: c.ApplianceID,
		AssignmentGen: assignedGen}
	if rec, err := asgStore.Load(); err == nil && rec != nil && rec.Current != nil {
		p3scope.AssignmentID = rec.Current.AssignmentID
	}
	// The plan generation is durable: restarting at 1 would get every plan refused as stale, silently freezing enforcement.
	p3plans := newPlanCounter(envOr("ACCTD_PHASE3_PLAN_STATE", "/var/lib/stayconnect/acctd-phase3-plan.json"))
	p3plans.start()
	a.p3 = newPhase3(pmsCfg, a, c.TenantID, assignedSite, p3scope, p3plans)
	// acctd derives the plan; netd is the only process that mutates Phase-3 tc state.
	netdShaping := newNetdShaper(envOr("ACCTD_NETD_SOCKET", "/run/stayconnect/netd.sock"))
	p3 := a.p3
	// Accounting owner of last resort, active only when the Phase-3 arm is absent, so a finite budget already
	// granted does not become unlimited because another phase's flag is off.
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
			// Phase 3 (phase3.accountingPass) is the sole accounting writer, so two writers can never disagree
			// about one guest's usage. Order matters: measure first so this tick's usage is attributed before anything is closed out,
			// then enforce expiry, then submit the plan to netd. All are no-ops while dark.
			now := time.Now()
			if n := p3.accountingPass(rootCtx, a.shp, netdShaping, c.LegacyBridge, now); n > 0 {
				slog.Debug("phase3: accounting samples ingested", "count", n)
			}
			// Session limits run before the expiry sweep so an ended session accrues no more online time. Idle is
			// judged only after a clean accounting pass: an unread meter is not an idle guest.
			p3.enforceSessionLimits(rootCtx, now)
			p3.enforceExpiries(rootCtx)
			p3.accountingMaintenance(rootCtx, now)
			aggOwner.sweep(rootCtx)
			p3.reconcileShaping(rootCtx, netdShaping, c.LegacyBridge)
			// The heartbeat proves the loop is progressing; the report says why it is degraded, since a loop
			// whose every observation is refused is not healthy.
			livez.Touch("acctd")
			livez.Report("acctd", p3.degradedSummary())
		}
	}
}

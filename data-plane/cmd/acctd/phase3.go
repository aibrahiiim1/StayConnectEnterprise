package main

// Access ends at its true time (window_ends_at, or the sample that crossed a data quota), never at sweep time:
// recording the sweep time would shift every guest's access by the sweep interval and make the audit trail
// unreproducible. The authoritative logic is in internal/enforce; this file only runs it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeproducer"
)

// phase3 is acctd's Phase-3 arm; a nil *phase3 is inert and safe to call.
type phase3 struct {
	cfg iamv2.PMSConfig
	// degraded is non-empty when the last derived plan could not be put in force: kernel and durable state disagree.
	degraded string
	// acctDegraded is non-empty when the last accounting pass did not complete cleanly. acctd keeps no counter
	// baseline of its own; the durable checkpoint is the baseline, which makes a restart safe.
	acctDegraded string
	// lastPassOK lets health tell silence from success.
	lastPassOK time.Time
	enf        *enforce.Enforcer
	// site is the single site this appliance serves; expiry enforcement is scoped to it.
	tenant, site string
	// scope and plans scope a submitted plan and give it a durable monotonic generation so netd can refuse a
	// stale or replayed one.
	scope     planScope
	plans     *planCounter
	lastPurge time.Time
}

// newPhase3 returns nil while the enforcement plane is off. It must not be gated on Checkout Grace: the shaping
// producer is what moves a PENDING_ENFORCEMENT session into force, and gating it left Room-authenticated guests
// pending forever.
func newPhase3(cfg iamv2.PMSConfig, a *acctd, tenant, site string, scope planScope, plans *planCounter) *phase3 {
	if !cfg.EnforcementOn() {
		return nil
	}
	enf := enforce.New(a.db)

	// Accrual is deliberately NOT gated by the aggregate flag. If a rollback or config change turned the flag
	// off after aggregate entitlements were granted, their budgets would stop being consumed and a finite
	// package would silently become unlimited. The flag gates acquisition (iamv2.TimeModeAcquirable) and
	// surfaces only; accrual is data-driven and writes nothing unless such an entitlement exists.
	//
	// The charge bound is a few ticks: a tick delayed by load is real observed time, a longer gap is an outage.
	enf = enf.WithAggregateOnlineTime(aggregateChargeBoundSeconds(tickSeconds(os.Getenv)))
	if p6, err := iamv2.LoadPhase6ConfigFromEnv(os.Getenv); err != nil {
		slog.Error("phase6 configuration is unreadable; aggregate ACQUISITION stays OFF, "+
			"but accrual for any entitlement already in that mode continues", "err", err)
	} else if !p6.AggregateTimeOn() {
		slog.Info("phase6 aggregate acquisition is OFF; accrual still runs for entitlements already in that mode")
	}

	return &phase3{cfg: cfg, enf: enf, tenant: tenant, site: site, scope: scope, plans: plans}
}

// aggregateChargeBoundSeconds is four ticks with a one-minute floor: the floor stops a single slow pass from
// being recorded as a skipped interval when ticks are 1 s.
func aggregateChargeBoundSeconds(tickSeconds int) int {
	if tickSeconds < 1 {
		tickSeconds = 1
	}
	if b := tickSeconds * 4; b > 60 {
		return b
	}
	return 60
}

// enforceExpiries is idempotent, so running it every tick costs one query when nothing has expired.
func (p *phase3) enforceExpiries(ctx context.Context) {
	if p == nil {
		return
	}
	due, err := p.enf.EnforceExpiries(ctx, p.tenant, p.site)
	if err != nil {
		// Loud, but it must not stop the loop; the next tick retries.
		slog.Error("phase3: expiry enforcement failed", "err", err)
		return
	}
	for _, x := range due {
		slog.Info("phase3: access ended at its true time",
			"entitlement", x.EntitlementID, "reason", x.Reason, "effective_at", x.At,
			"sessions_ended", x.Sessions, "devices_revoked", x.Devices)
	}
}

// enforceSessionLimits ends sessions past their plan's idle or maximum-session limit. Idle is judged only when
// this tick's accounting pass completed cleanly.
func (p *phase3) enforceSessionLimits(ctx context.Context, now time.Time) {
	if p == nil {
		return
	}
	judgeIdle := p.acctDegraded == "" && p.lastPassOK.Equal(now)
	ended, err := p.enf.EnforceSessionLimits(ctx, p.tenant, p.site, now, judgeIdle)
	if err != nil {
		slog.Error("phase3: plan session limits could not be enforced", "err", err)
		return
	}
	for _, x := range ended {
		slog.Info("phase3: session ended by its plan's limit", "session", x.SessionID,
			"entitlement", x.EntitlementID, "reason", x.Reason, "effective_at", x.At)
	}
}

// The purge runs in bounded batches so a site that shortens its retention catches up over hours without a long
// transaction on the table every tick writes to.
const (
	purgeInterval   = time.Hour
	purgeBatch      = 5000
	purgeMaxBatches = 20
)

func (p *phase3) purgeAccountingDetail(ctx context.Context, now time.Time) {
	if p == nil || (!p.lastPurge.IsZero() && now.Sub(p.lastPurge) < purgeInterval) {
		return
	}
	p.lastPurge = now
	total := 0
	for i := 0; i < purgeMaxBatches; i++ {
		n, err := p.enf.PurgeAccountingDetail(ctx, p.tenant, p.site, purgeBatch)
		if err != nil {
			slog.Error("phase3: accounting retention purge failed", "err", err)
			return
		}
		total += n
		if n < purgeBatch {
			break
		}
	}
	if total > 0 {
		slog.Info("phase3: accounting detail older than the site's retention removed", "records", total)
	}
}

// shapingPlan derives the desired enforcement from durable state rather than remembering it, so a Grace
// conversion, rebinding or revocation is reflected without separate bookkeeping.
func (p *phase3) shapingPlan(ctx context.Context) (enforce.Plan, bool) {
	if p == nil {
		return enforce.Plan{}, false
	}
	plan, err := p.enf.PlanForSite(ctx, p.tenant, p.site)
	if err != nil {
		slog.Error("phase3: could not derive the shaping plan", "err", err)
		return enforce.Plan{}, false
	}
	return plan, true
}

// planSubmitter delivers the desired state to netd, the only Phase-3 shaping writer; acctd has no tc client,
// so it cannot race netd.
type planSubmitter interface {
	SubmitShapingPlan(ctx context.Context, env shapeplan.Envelope) (shapingResult, error)
}

// shapingResult is netd's answer: a refusal (netd would not act) differs from Degraded (the kernel is not
// enforcing what durable state says).
type shapingResult struct {
	Accepted      bool     `json:"accepted"`
	Reason        string   `json:"reason,omitempty"`
	TornDown      int      `json:"torn_down"`
	StraysRemoved int      `json:"strays_removed"`
	Shaped        int      `json:"shaped"`
	Failed        int      `json:"failed"`
	Degraded      bool     `json:"degraded"`
	Problems      []string `json:"problems,omitempty"`
}

// planValidity spans a few ticks: one missed submission is no incident, but a silently dead producer shows up
// as a stale plan on netd health.
const planValidity = 90 * time.Second

// buildEnvelope lists every live session exactly once, entitled or not: "not mentioned" must never end access,
// since it is indistinguishable from a truncated body.
// A generation that could not be saved is an error: submitting it would let a restart re-issue it.
func (p *phase3) buildEnvelope(plan enforce.Plan, managedBridges []string, fallbackBridge string, now time.Time) (shapeplan.Envelope, error) {
	gen, runtime, err := p.plans.next()
	if err != nil {
		return shapeplan.Envelope{}, err
	}
	return shapeproducer.BuildEnvelope(plan, shapeproducer.Scope{
		TenantID: p.tenant, SiteID: p.site, ApplianceID: p.scope.ApplianceID,
		AssignmentID: p.scope.AssignmentID, AssignmentGen: p.scope.AssignmentGen,
	}, gen, runtime, managedBridges, fallbackBridge, now, planValidity), nil
}

// reconcileShaping submits the full desired state every tick, not a delta, so a restart, reboot or manual tc
// change converges on the next submission.
func (p *phase3) reconcileShaping(ctx context.Context, netd planSubmitter, fallbackBridge string) {
	if p == nil {
		return
	}
	plan, ok := p.shapingPlan(ctx)
	if !ok {
		return
	}
	env, err := p.buildEnvelope(plan, p.managedBridges(ctx), fallbackBridge, time.Now())
	if err != nil {
		// Audit P6 M2: never submit a generation that is not durably saved; report it on health instead.
		p.degraded = "shaping plan not submitted: " + err.Error()
		slog.Error("phase3: the plan generation could not be saved; plan not submitted", "err", err)
		return
	}
	res, err := netd.SubmitShapingPlan(ctx, env)
	if err != nil {
		// Re-derived next tick; what must not happen is pretending it was applied.
		p.degraded = "shaping plan not applied: " + err.Error()
		slog.Warn("phase3: could not submit the shaping plan to netd", "err", err)
		return
	}
	if !res.Accepted {
		// A contract or scope problem, not a kernel one: the same plan will be refused again.
		p.degraded = "netd refused the shaping plan: " + res.Reason
		slog.Error("phase3: netd refused the shaping plan", "reason", res.Reason)
		return
	}
	if res.Degraded {
		p.degraded = "netd applied the plan with problems"
		slog.Warn("phase3: netd applied the shaping plan with problems",
			"failed", res.Failed, "torn_down", res.TornDown, "strays_removed", res.StraysRemoved, "shaped", res.Shaped)
		return
	}
	if res.StraysRemoved > 0 {
		// Not an error, but never silent: the kernel was forwarding for a session durable state lacks.
		slog.Warn("phase3: reconciliation removed shaping classes with no live session", "count", res.StraysRemoved)
	}
	p.degraded = ""
}

// managedBridges reads bridges from network configuration, not live sessions: a class left on a bridge with no
// sessions would otherwise forward traffic for ended access indefinitely. A failed read returns nothing extra
// and the empty-bridge sweep resumes next tick.
func (p *phase3) managedBridges(ctx context.Context) []string {
	if p == nil || p.enf == nil {
		return nil
	}
	rows, err := p.enf.Pool().Query(ctx,
		`SELECT DISTINCT bridge_name FROM public.guest_networks
		  WHERE enabled AND bridge_name <> '' ORDER BY bridge_name`)
	if err != nil {
		slog.Warn("phase3: could not list guest bridges; only bridges with live sessions are reconciled", "err", err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return out
		}
		out = append(out, b)
	}
	return out
}

func (p *phase3) Degraded() string {
	if p == nil {
		return ""
	}
	return p.degraded
}

// degradedSummary is the one line the health supervisor sees; a dark arm is never degraded.
func (p *phase3) degradedSummary() string {
	if p == nil {
		return ""
	}
	if p.acctDegraded == "" && p.degraded == "" && !p.lastPassOK.IsZero() &&
		time.Since(p.lastPassOK) > accountingFreshness {
		return reasonNoRecentPass
	}
	switch {
	case p.acctDegraded != "" && p.degraded != "":
		return p.acctDegraded + "; " + p.degraded
	case p.acctDegraded != "":
		return p.acctDegraded
	default:
		return p.degraded
	}
}

// ownsAccounting: when Phase 3 owns accounting no other writer may run, or one physical delta would be
// recorded twice with no way to tell the duplicate.
func (p *phase3) ownsAccounting() bool { return p != nil }

// netdShaper submits plans to netd over its local Unix socket.
type netdShaper struct {
	client *http.Client
	url    string
}

func newNetdShaper(socketPath string) *netdShaper {
	return &netdShaper{
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
			Timeout: 10 * time.Second,
		},
		url: "http://netd/v1/phase3/shaping",
	}
}

func (n *netdShaper) SubmitShapingPlan(ctx context.Context, env shapeplan.Envelope) (shapingResult, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return shapingResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(raw))
	if err != nil {
		return shapingResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return shapingResult{}, err
	}
	defer resp.Body.Close()
	var out shapingResult
	// A refusal carries its reason in the same shape, so the producer can report why enforcement is not in force.
	if derr := json.NewDecoder(resp.Body).Decode(&out); derr != nil && resp.StatusCode == http.StatusOK {
		return shapingResult{}, derr
	}
	if resp.StatusCode != http.StatusOK {
		if out.Reason == "" {
			out.Reason = "http " + resp.Status
		}
		out.Accepted = false
		return out, nil
	}
	return out, nil
}

// ClassEpochs asks netd for the managed-class generations; acctd never invents one, so an unreachable netd
// defers the pass rather than judging a backwards counter alone.
func (n *netdShaper) ClassEpochs(ctx context.Context) (map[string]int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://netd/v1/phase3/shaping/epochs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("netd refused the class-generation request: %s", resp.Status)
	}
	var out struct {
		Epochs map[string]int64 `json:"epochs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Epochs == nil {
		out.Epochs = map[string]int64{}
	}
	return out.Epochs, nil
}

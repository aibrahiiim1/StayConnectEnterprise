package main

// PHASE-3 ACCOUNTING + EXPIRY ENFORCEMENT, wired into the acctd loop.
//
// While the Phase-3 flags are OFF this file does nothing at all: no query, no enforcement, no behaviour
// change — the appliance keeps running exactly the legacy accounting path it runs today.
//
// With the flags ON, acctd gains a second responsibility on the same tick: end access that has actually
// ended. The rule that matters is WHEN it ended. A validity window ends at its window_ends_at, and a data
// quota ends at the sample that crossed it — not at whatever moment this sweep happened to run. Recording the
// sweep time instead would quietly extend or shorten every guest's access by the sweep interval and make the
// audit trail unreproducible.
//
// Everything authoritative lives in internal/enforce and the controlled database operations: this file is the
// composition root that runs them, logs what happened, and stays out of the way.

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

// phase3 is acctd's Phase-3 arm. A zero value is inert, which is what a dark appliance gets.
type phase3 struct {
	cfg iamv2.PMSConfig
	// degraded is non-empty when the last derived plan could not be put in force. It is reported rather than
	// hidden: an unapplied plan means the kernel and durable state disagree.
	degraded string
	// acctDegraded is non-empty when the last accounting pass could not complete cleanly (unreadable counters,
	// unavailable class generations, a refused observation). acctd keeps NO counter baseline of its own: the
	// durable checkpoint in the database is the baseline, which is what makes a restart safe.
	acctDegraded string
	// lastPassOK is when a pass last completed with nothing degraded. Silence is indistinguishable from
	// success from the outside, so health needs to know when the loop last actually worked.
	lastPassOK time.Time
	enf        *enforce.Enforcer
	// site is the single site this appliance serves; expiry enforcement is scoped to it.
	tenant, site string
	// scope and plans are the producer half of the shaping contract: the appliance identity a submitted plan
	// is scoped to, and the durable monotonic generation that lets netd refuse a stale or replayed one.
	scope planScope
	plans *planCounter
}

// newPhase3 constructs the enforcement arm whenever the Phase-3 ENFORCEMENT PLANE is on. It returns nil while
// dark, and a nil *phase3 is safe to call — so the tick path needs no flag checks of its own.
//
// The gate used to be CheckoutGraceOn. That made the shaping PRODUCER — the half that notices a session in
// PENDING_ENFORCEMENT and tells netd to enforce it — conditional on an unrelated post-departure feature. On an
// appliance running Room authentication with Checkout Grace off, nothing derived a plan at all: two real
// guests were granted entitlements and sessions that stayed pending forever, and the guest was told the
// connection had failed while the rows said otherwise.
func newPhase3(cfg iamv2.PMSConfig, a *acctd, tenant, site string, scope planScope, plans *planCounter) *phase3 {
	if !cfg.EnforcementOn() {
		return nil
	}
	enf := enforce.New(a.db)

	// PHASE 6: ACCRUAL IS NOT GATED BY THE FLAG, AND THAT IS THE SAFETY PROPERTY.
	//
	// The obvious wiring -- accrue only while the aggregate flag is on -- has a failure mode that is worse
	// than the feature being off: an appliance that has ALREADY granted aggregate entitlements, whose flag is
	// then turned off by a rollback, a config change or a partially applied deployment, would stop consuming
	// their budgets. Nothing would ever exhaust. A guest holding a finite two-hour package would silently
	// hold unlimited access, and the durable record would show consumption frozen at whatever it was, with no
	// evidence that anything had stopped.
	//
	// So the flag gates ACQUISITION (no new aggregate entitlement can be created while it is off -- see
	// iamv2.TimeModeAcquirable) and the SURFACES, while accrual is DATA-DRIVEN: the tick runs every sweep and
	// finds nothing to do unless an aggregate entitlement actually exists. On every appliance today that is
	// zero rows and zero writes, which is what "dark" has to mean -- no Phase-6 BEHAVIOUR -- rather than
	// "the accounting for existing entitlements is switched off".
	//
	// The bound is deliberately a few sweep intervals rather than one: a tick delayed by ordinary load is
	// still real observed time and should be charged in full, while a gap that long means the service was
	// not running and must not be.
	enf = enf.WithAggregateOnlineTime(aggregateChargeBoundSeconds(envInt("ACCTD_TICK_SECONDS", 1)))
	if p6, err := iamv2.LoadPhase6ConfigFromEnv(os.Getenv); err != nil {
		slog.Error("phase6 configuration is unreadable; aggregate ACQUISITION stays OFF, "+
			"but accrual for any entitlement already in that mode continues", "err", err)
	} else if !p6.AggregateTimeOn() {
		slog.Info("phase6 aggregate acquisition is OFF; accrual still runs for entitlements already in that mode")
	}

	return &phase3{cfg: cfg, enf: enf, tenant: tenant, site: site, scope: scope, plans: plans}
}

// aggregateChargeBoundSeconds turns the sweep interval into the per-tick charge bound.
//
// FOUR TICKS, with a floor of a minute. The multiple absorbs an ordinary slow or delayed sweep -- that is
// real observed time and a guest should be charged for it -- while still being short enough that an outage
// is unmistakably an outage rather than a long tick. The floor exists because a one-second sweep would
// otherwise produce a four-second bound, and then a single slow pass would start recording skipped intervals
// for time the service actually was watching.
func aggregateChargeBoundSeconds(tickSeconds int) int {
	if tickSeconds < 1 {
		tickSeconds = 1
	}
	if b := tickSeconds * 4; b > 60 {
		return b
	}
	return 60
}

// enforce runs one expiry pass. It is idempotent: an Entitlement already terminated is not re-terminated, so
// running it every tick costs one query when nothing has expired.
func (p *phase3) enforceExpiries(ctx context.Context) {
	if p == nil {
		return // dark: nothing was constructed, nothing runs
	}
	due, err := p.enf.EnforceExpiries(ctx, p.tenant, p.site)
	if err != nil {
		// An enforcement failure must be loud but must not stop the legacy accounting loop: the entitlements
		// stay live and the next tick tries again.
		slog.Error("phase3: expiry enforcement failed", "err", err)
		return
	}
	for _, x := range due {
		slog.Info("phase3: access ended at its true time",
			"entitlement", x.EntitlementID, "reason", x.Reason, "effective_at", x.At,
			"sessions_ended", x.Sessions, "devices_revoked", x.Devices)
	}
}

// shapingPlan derives what the edge should currently be enforcing. It is returned rather than applied here:
// the shaping owner applies it, and deriving it from durable state (instead of remembering it) is what keeps
// a Grace conversion, a rebinding or a revocation reflected without separate bookkeeping.
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

// planSubmitter delivers a complete desired state to netd, the single Phase-3 shaping writer (ADR-0002).
// acctd deliberately has no tc client for Phase-3: it cannot race netd because it cannot write.
type planSubmitter interface {
	SubmitShapingPlan(ctx context.Context, env shapeplan.Envelope) (shapingResult, error)
}

// shapingResult is what netd reports back. A refusal and a degraded application are different failures and are
// reported differently: the first means netd would not act on the plan at all, the second means the kernel is
// not enforcing what durable state says it should.
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

// planValidity is how long a submitted plan may be considered current. It is deliberately a few ticks: long
// enough that one missed submission is not an incident, short enough that a producer which has silently died
// shows up as a stale plan on netd health instead of looking like a quiet, healthy site.
const planValidity = 90 * time.Second

// buildEnvelope turns the derived plan into a complete, scoped, hashed desired state. Every live session
// appears exactly once — entitled or not — because "not mentioned" must never be how access ends: that is
// indistinguishable from a truncated body.
func (p *phase3) buildEnvelope(plan enforce.Plan, managedBridges []string, fallbackBridge string, now time.Time) shapeplan.Envelope {
	gen, runtime := p.plans.next()
	return shapeproducer.BuildEnvelope(plan, shapeproducer.Scope{
		TenantID: p.tenant, SiteID: p.site, ApplianceID: p.scope.ApplianceID,
		AssignmentID: p.scope.AssignmentID, AssignmentGen: p.scope.AssignmentGe,
	}, gen, runtime, managedBridges, fallbackBridge, now, planValidity)
}

// reconcileShaping derives the current desired state and submits it to netd. It is a full RECONCILIATION every
// tick, not a delta: a process restart, a reboot, or a manual tc change converges on the next submission, and
// neither side has to remember anything for that to work.
func (p *phase3) reconcileShaping(ctx context.Context, netd planSubmitter, fallbackBridge string) {
	if p == nil {
		return
	}
	plan, ok := p.shapingPlan(ctx)
	if !ok {
		return
	}
	res, err := netd.SubmitShapingPlan(ctx, p.buildEnvelope(plan, p.managedBridges(ctx), fallbackBridge, time.Now()))
	if err != nil {
		// Bounded retry: the plan is re-derived and re-submitted on the next tick, so a missed submission is
		// never stale — it is superseded. What must not happen is pretending it was applied.
		p.degraded = "shaping plan not applied: " + err.Error()
		slog.Warn("phase3: could not submit the shaping plan to netd", "err", err)
		return
	}
	if !res.Accepted {
		// netd refused the envelope outright. That is a contract or scope problem, not a kernel problem, and it
		// must be visible as such: re-deriving the same plan will be refused for the same reason.
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
		// Not an error, but never silent: a stray means the kernel was forwarding for a session durable state
		// does not have, which is exactly the drift reconciliation exists to catch.
		slog.Warn("phase3: reconciliation removed shaping classes with no live session", "count", res.StraysRemoved)
	}
	p.degraded = ""
}

// managedBridges lists every guest bridge this site has. It is read from the appliance's own network
// configuration rather than derived from live sessions, because the interesting case is precisely a bridge
// with no sessions: nothing else would ever mention it, and a class left behind there would forward traffic
// for access that ended, indefinitely.
//
// A failed read returns nothing extra rather than failing the tick: the session bridges are still declared,
// so reconciliation stays correct where guests actually are, and the empty-bridge sweep resumes next tick.
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

// Degraded reports the truthful current enforcement state for health reporting: empty when the last plan was
// applied cleanly, otherwise why it was not.
func (p *phase3) Degraded() string {
	if p == nil {
		return ""
	}
	return p.degraded
}

// degradedSummary is the single truthful line the health supervisor sees: empty when this tick's accounting
// and enforcement both completed, otherwise both reasons. A dark arm is never degraded — it does nothing.
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

// ownsAccounting reports whether Phase-3 owns accounting for this appliance. When it does, the legacy writer
// must not run for the same sample: two rows for one physical delta would double every total derived from
// them, and there is no way to tell afterwards which one was the duplicate.
func (p *phase3) ownsAccounting() bool { return p != nil }

// netdShaper submits plans to netd over its protected local Unix socket. It is the only thing standing
// between acctd's derivation and the kernel, and it deliberately cannot do anything else.
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
	// A refusal carries its bounded reason in the same shape as an acceptance, so the producer can report WHY
	// enforcement is not in force instead of a bare status code.
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

// ClassEpochs asks netd for the current managed-class generations. acctd never invents one: if netd cannot be
// reached the pass defers rather than judging a backwards counter on its own.
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

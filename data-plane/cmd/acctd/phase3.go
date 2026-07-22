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
	"sort"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
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

// newPhase3 constructs the enforcement arm ONLY when the Phase-3 master + checkout-grace flags are on. It
// returns nil while dark, and a nil *phase3 is safe to call — so the tick path needs no flag checks of its own.
func newPhase3(cfg iamv2.PMSConfig, a *acctd, tenant, site string, scope planScope, plans *planCounter) *phase3 {
	if !cfg.CheckoutGraceOn() {
		return nil
	}
	if plans == nil {
		// An in-memory counter is only correct for a test: it restarts at 1, and netd would then refuse every
		// plan this process produced. Callers that mean it pass a durable one.
		plans = newPlanCounter("")
	}
	return &phase3{cfg: cfg, enf: enforce.New(a.db), tenant: tenant, site: site, scope: scope, plans: plans}
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
	bridgeOf := func(b string) string {
		if b != "" {
			return b
		}
		return fallbackBridge
	}
	sessions := make([]shapeplan.Session, 0, len(plan.Tear)+len(plan.Shape))
	for _, s := range plan.Tear {
		sessions = append(sessions, shapeplan.Session{
			SessionID: s.SessionID, DeviceID: s.DeviceID, IP: s.IP, Bridge: bridgeOf(s.Bridge), Entitled: false})
	}
	for _, s := range plan.Shape {
		sessions = append(sessions, shapeplan.Session{
			SessionID: s.SessionID, DeviceID: s.DeviceID, IP: s.IP, Bridge: bridgeOf(s.Bridge),
			DownKbps: s.DownKbps, UpKbps: s.UpKbps, Entitled: true})
	}
	// Every bridge a session is on must be declared, plus every guest bridge the site has — including ones
	// with no sessions at all. Those are the ones that can quietly keep forwarding for access that ended.
	declared := map[string]bool{}
	for _, b := range managedBridges {
		if b != "" {
			declared[b] = true
		}
	}
	for _, s := range sessions {
		if s.Bridge != "" {
			declared[s.Bridge] = true
		}
	}
	if fallbackBridge != "" {
		declared[fallbackBridge] = true
	}
	bridges := make([]string, 0, len(declared))
	for b := range declared {
		bridges = append(bridges, b)
	}
	sort.Strings(bridges)

	gen, runtime := p.plans.next()
	env := shapeplan.Envelope{
		ContractVersion:    shapeplan.ContractVersion,
		TenantID:           p.tenant,
		SiteID:             p.site,
		ApplianceID:        p.scope.ApplianceID,
		AssignmentID:       p.scope.AssignmentID,
		AssignmentGen:      p.scope.AssignmentGe,
		ProducerRuntimeGen: runtime,
		PlanGeneration:     gen,
		GeneratedAt:        now.UTC(),
		ExpiresAt:          now.UTC().Add(planValidity),
		ManagedBridges:     bridges,
		Sessions:           sessions,
	}
	env.DesiredStateHash = shapeplan.HashDesiredState(bridges, sessions)
	return env
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

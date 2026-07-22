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
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
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
	enf          *enforce.Enforcer
	// site is the single site this appliance serves; expiry enforcement is scoped to it.
	tenant, site string
}

// newPhase3 constructs the enforcement arm ONLY when the Phase-3 master + checkout-grace flags are on. It
// returns nil while dark, and a nil *phase3 is safe to call — so the tick path needs no flag checks of its own.
func newPhase3(cfg iamv2.PMSConfig, a *acctd, tenant, site string) *phase3 {
	if !cfg.CheckoutGraceOn() {
		return nil
	}
	return &phase3{cfg: cfg, enf: enforce.New(a.db), tenant: tenant, site: site}
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

// planSubmitter delivers a complete shaping plan to netd, the single Phase-3 shaping writer (ADR-0002).
// acctd deliberately has no tc client for Phase-3: it cannot race netd because it cannot write.
type planSubmitter interface {
	SubmitShapingPlan(ctx context.Context, plan enforce.Plan, fallbackBridge string) (shapingResult, error)
}

// shapingResult is what netd reports back. Degraded is surfaced rather than swallowed: a plan that could not
// be applied means the kernel is not enforcing what durable state says it should.
type shapingResult struct {
	TornDown int      `json:"torn_down"`
	Shaped   int      `json:"shaped"`
	Failed   int      `json:"failed"`
	Degraded bool     `json:"degraded"`
	Problems []string `json:"problems,omitempty"`
}

// reconcileShaping derives the current plan and submits it to netd. It is a full RECONCILIATION every tick,
// not a delta: a process restart, a reboot, or a manual tc change converges on the next submission, and
// neither side has to remember anything for that to work.
func (p *phase3) reconcileShaping(ctx context.Context, netd planSubmitter, fallbackBridge string) {
	if p == nil {
		return
	}
	plan, ok := p.shapingPlan(ctx)
	if !ok {
		return
	}
	res, err := netd.SubmitShapingPlan(ctx, plan, fallbackBridge)
	if err != nil {
		// Bounded retry: the plan is re-derived and re-submitted on the next tick, so a missed submission is
		// never stale — it is superseded. What must not happen is pretending it was applied.
		p.degraded = "shaping plan not applied: " + err.Error()
		slog.Warn("phase3: could not submit the shaping plan to netd", "err", err)
		return
	}
	if res.Degraded {
		p.degraded = "netd applied the plan with problems"
		slog.Warn("phase3: netd applied the shaping plan with problems",
			"failed", res.Failed, "torn_down", res.TornDown, "shaped", res.Shaped)
		return
	}
	p.degraded = ""
}

// Degraded reports the truthful current enforcement state for health reporting: empty when the last plan was
// applied cleanly, otherwise why it was not.
func (p *phase3) Degraded() string {
	if p == nil {
		return ""
	}
	return p.degraded
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

func (n *netdShaper) SubmitShapingPlan(ctx context.Context, plan enforce.Plan, fallbackBridge string) (shapingResult, error) {
	bridgeOf := func(s enforce.SessionShape) string {
		if s.Bridge != "" {
			return s.Bridge
		}
		return fallbackBridge
	}
	type sess struct {
		SessionID string `json:"session_id"`
		IP        string `json:"ip"`
		Bridge    string `json:"bridge"`
		DownKbps  int    `json:"down_kbps"`
		UpKbps    int    `json:"up_kbps"`
	}
	body := struct {
		Tear  []sess `json:"tear"`
		Shape []sess `json:"shape"`
	}{Tear: []sess{}, Shape: []sess{}}
	for _, s := range plan.Tear {
		body.Tear = append(body.Tear, sess{SessionID: s.SessionID, IP: s.IP, Bridge: bridgeOf(s)})
	}
	for _, s := range plan.Shape {
		body.Shape = append(body.Shape, sess{SessionID: s.SessionID, IP: s.IP, Bridge: bridgeOf(s),
			DownKbps: s.DownKbps, UpKbps: s.UpKbps})
	}
	raw, err := json.Marshal(body)
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
	if resp.StatusCode != http.StatusOK {
		return shapingResult{}, fmt.Errorf("netd rejected the shaping plan: %s", resp.Status)
	}
	var out shapingResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return shapingResult{}, err
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

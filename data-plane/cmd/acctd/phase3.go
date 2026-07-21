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
	"context"
	"log/slog"
	"net"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// phase3 is acctd's Phase-3 arm. A zero value is inert, which is what a dark appliance gets.
type phase3 struct {
	cfg iamv2.PMSConfig
	enf *enforce.Enforcer
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

// shapeApplier is the narrow slice of the tc client the reconciliation needs. Keeping it an interface is what
// lets the composition-root test prove the ORDER of operations without touching the kernel.
type shapeApplier interface {
	EnsureBridgeInfra(ctx context.Context, bridge string) error
	AddSession(ctx context.Context, bridge string, ip net.IP, downKbps, upKbps int) error
	DeleteSession(ctx context.Context, bridge string, ip net.IP) error
}

// reconcileShaping makes the edge match what durable state says it should be. It is a RECONCILIATION, not an
// event handler: it re-derives the whole plan every tick, so a process restart, a reboot, or a manual change
// to tc converges back on the next pass without any remembered state of its own.
//
// Order matters and is deliberate: tear down first, then shape. A session that has lost its entitlement must
// stop being forwarded before capacity is handed to whoever is still entitled — the other order leaves a
// window where ended access is still shaped.
func (p *phase3) reconcileShaping(ctx context.Context, shp shapeApplier, fallbackBridge string) {
	if p == nil {
		return
	}
	plan, ok := p.shapingPlan(ctx)
	if !ok {
		return
	}
	p.applyPlan(ctx, shp, plan, fallbackBridge)
}

// applyPlan is the part that talks to the edge, separated so the ORDER and the rate/bridge decisions can be
// proven without a database or a kernel.
func (p *phase3) applyPlan(ctx context.Context, shp shapeApplier, plan enforce.Plan, fallbackBridge string) {
	bridgeOf := func(s enforce.SessionShape) string {
		if s.Bridge != "" {
			return s.Bridge
		}
		return fallbackBridge
	}
	for _, s := range plan.Tear {
		ip := net.ParseIP(s.IP)
		if ip == nil {
			continue
		}
		if err := shp.DeleteSession(ctx, bridgeOf(s), ip); err != nil {
			slog.Warn("phase3: could not tear down shaping", "session", s.SessionID, "err", err)
		}
	}
	for _, s := range plan.Shape {
		ip := net.ParseIP(s.IP)
		if ip == nil || s.DownKbps <= 0 || s.UpKbps <= 0 {
			// A session with no addressing or no rates is not shaped at all: applying a zero rate would be a
			// silent full-speed pass, which is the opposite of what an unratable session should get.
			continue
		}
		b := bridgeOf(s)
		if err := shp.EnsureBridgeInfra(ctx, b); err != nil {
			slog.Warn("phase3: bridge infrastructure unavailable", "bridge", b, "err", err)
			continue
		}
		if err := shp.AddSession(ctx, b, ip, s.DownKbps, s.UpKbps); err != nil {
			slog.Warn("phase3: could not apply shaping", "session", s.SessionID, "err", err)
		}
	}
}

// applyPlanForTest exposes the plan-application half to composition-root tests.
func (p *phase3) applyPlanForTest(ctx context.Context, shp shapeApplier, plan enforce.Plan, fallbackBridge string) {
	p.applyPlan(ctx, shp, plan, fallbackBridge)
}

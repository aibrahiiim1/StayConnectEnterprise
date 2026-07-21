package main

// Composition-root tests for acctd's Phase-3 arm: what the DAEMON does with the enforcement library, not what
// the library can do on its own.

import (
	"context"
	"net"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

type shapeCall struct {
	op     string // "ensure" | "add" | "delete"
	bridge string
	ip     string
	down   int
	up     int
}

type fakeShaper struct{ calls []shapeCall }

func (f *fakeShaper) EnsureBridgeInfra(ctx context.Context, bridge string) error {
	f.calls = append(f.calls, shapeCall{op: "ensure", bridge: bridge})
	return nil
}
func (f *fakeShaper) AddSession(ctx context.Context, bridge string, ip net.IP, down, up int) error {
	f.calls = append(f.calls, shapeCall{op: "add", bridge: bridge, ip: ip.String(), down: down, up: up})
	return nil
}
func (f *fakeShaper) DeleteSession(ctx context.Context, bridge string, ip net.IP) error {
	f.calls = append(f.calls, shapeCall{op: "delete", bridge: bridge, ip: ip.String()})
	return nil
}

// While dark, the arm is never constructed and every entry point is a safe no-op: acctd keeps running exactly
// the legacy path, issuing zero Phase-3 queries and touching no shaping.
func TestPhase3ArmIsInertWhileDark(t *testing.T) {
	for _, cfg := range []iamv2.PMSConfig{
		{},                           // everything off
		{MasterEnabled: true},        // master on, checkout-grace off
		{CheckoutGraceEnabled: true}, // surface on without master (invalid; still off)
	} {
		p := newPhase3(cfg, &acctd{}, "t", "s")
		if p != nil {
			t.Fatalf("the Phase-3 arm was constructed while dark: %+v", cfg)
		}
		shp := &fakeShaper{}
		// a nil arm must be callable — the tick path has no flag checks of its own
		p.enforceExpiries(context.Background())
		p.reconcileShaping(context.Background(), shp, "br-lan")
		if len(shp.calls) != 0 {
			t.Fatal("a dark arm touched shaping")
		}
	}
}

// With the flags on, the arm is constructed and owns the site scope it was given.
func TestPhase3ArmIsConstructedWhenLive(t *testing.T) {
	cfg := iamv2.PMSConfig{MasterEnabled: true, CheckoutGraceEnabled: true}
	p := newPhase3(cfg, &acctd{}, "tenant-1", "site-1")
	if p == nil {
		t.Fatal("the Phase-3 arm was not constructed with the flags on")
	}
	if p.tenant != "tenant-1" || p.site != "site-1" {
		t.Fatalf("arm scope = %s/%s", p.tenant, p.site)
	}
}

// The reconciliation TEARS DOWN before it SHAPES. The other order leaves a window in which access that has
// ended is still being forwarded while capacity is handed out.
func TestReconcileTearsDownBeforeShaping(t *testing.T) {
	p := &phase3{tenant: "t", site: "s"}
	shp := &fakeShaper{}
	plan := planFixture()
	p.applyPlanForTest(context.Background(), shp, plan, "br-lan")

	var firstAdd, lastDelete = -1, -1
	for i, c := range shp.calls {
		if c.op == "add" && firstAdd == -1 {
			firstAdd = i
		}
		if c.op == "delete" {
			lastDelete = i
		}
	}
	if firstAdd == -1 || lastDelete == -1 {
		t.Fatalf("expected both adds and deletes, got %+v", shp.calls)
	}
	if lastDelete > firstAdd {
		t.Fatalf("shaping was applied before teardown finished: %+v", shp.calls)
	}
}

// Rates come from the plan (i.e. from the Entitlement's pinned Service Plan revision), the session's own
// bridge is used when it has one, and a session with no rates is left unshaped rather than given a free pass.
func TestReconcileUsesPlanRatesAndBridges(t *testing.T) {
	p := &phase3{tenant: "t", site: "s"}
	shp := &fakeShaper{}
	p.applyPlanForTest(context.Background(), shp, planFixture(), "br-fallback")

	var addedGuest, addedGrace bool
	for _, c := range shp.calls {
		if c.op == "add" && c.ip == "10.0.0.1" {
			addedGuest = true
			if c.down != 8000 || c.up != 3000 || c.bridge != "br-guest" {
				t.Fatalf("guest session shaped as %+v", c)
			}
		}
		if c.op == "add" && c.ip == "10.0.0.2" {
			addedGrace = true
			// no bridge on this session: the configured fallback is used rather than guessing
			if c.bridge != "br-fallback" || c.down != 4000 {
				t.Fatalf("grace session shaped as %+v", c)
			}
		}
		if c.op == "add" && c.ip == "10.0.0.9" {
			t.Fatal("a session with no rates was shaped — that would be a silent full-speed pass")
		}
	}
	if !addedGuest || !addedGrace {
		t.Fatalf("expected both entitled sessions to be shaped: %+v", shp.calls)
	}
}

// planFixture is a plan with: an entitled guest session on its own bridge, an entitled grace session with no
// bridge of its own, an entitled session with no rates, and two sessions to tear down.
func planFixture() enforce.Plan {
	return enforce.Plan{
		Shape: []enforce.SessionShape{
			{SessionID: "s1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 8000, UpKbps: 3000},
			{SessionID: "s2", IP: "10.0.0.2", DownKbps: 4000, UpKbps: 1500},
			{SessionID: "s9", IP: "10.0.0.9", Bridge: "br-guest"}, // no rates
		},
		Tear: []enforce.SessionShape{
			{SessionID: "s3", IP: "10.0.0.3", Bridge: "br-guest"},
			{SessionID: "s4", IP: "10.0.0.4"},
		},
	}
}

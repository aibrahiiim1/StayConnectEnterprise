package main

// Review findings P1-P3 on the continuity governor: scope is revalidated every tick, a shared allowance remembers what a departed
// session used, and a restored session regains its MAC binding before it is renewed.

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// macGate is a fakeGate that, like nftGate, takes a session's MAC only from a map the caller fills with BindMAC, and refreshes the
// MAC-bound element only when it knows the MAC at Authorize time.
type macGate struct {
	*fakeGate
	mu     sync.Mutex
	macs   map[string]string
	macTTL map[string]time.Duration
}

func newMacGate() *macGate {
	return &macGate{fakeGate: newFakeGate(), macs: map[string]string{}, macTTL: map[string]time.Duration{}}
}

func (g *macGate) BindMAC(bridge string, ip net.IP, mac string) {
	g.mu.Lock()
	g.macs[gk(bridge, ip)] = mac
	g.mu.Unlock()
}

func (g *macGate) Authorize(ctx context.Context, bridge string, ip net.IP, ttl time.Duration) error {
	if err := g.fakeGate.Authorize(ctx, bridge, ip, ttl); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.macs[gk(bridge, ip)] != "" {
		g.macTTL[gk(bridge, ip)] = ttl
	}
	return nil
}

func (g *macGate) forget() {
	g.mu.Lock()
	g.macs = map[string]string{}
	g.macTTL = map[string]time.Duration{}
	g.mu.Unlock()
}

// P1: Phase 3 going inactive, or another scope being adopted, stops renewal at once.
func TestContinuity_ScopeChangeOrInactiveStopsRenewal(t *testing.T) {
	for name, change := range map[string]func(p *phase3Shaping){
		"phase 3 inactive": func(p *phase3Shaping) { p.mode.Active = false },
		"another tenant":   func(p *phase3Shaping) { p.mode.TenantID = "00000000-0000-0000-0000-0000000000aa" },
		"another site":     func(p *phase3Shaping) { p.mode.SiteID = "00000000-0000-0000-0000-0000000000bb" },
		"newer assignment": func(p *phase3Shaping) { p.mode.AssignGen = 99 },
	} {
		t.Run(name, func(t *testing.T) {
			h := newContH(t)
			a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
			h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
			h.run(2*time.Minute, nil)
			if !h.authorized(a.IP) {
				t.Fatal("precondition: online on continuity")
			}
			h.p.mu.Lock()
			change(h.p)
			h.p.mu.Unlock()
			var lost time.Duration
			h.run(3*time.Minute, func(el time.Duration) {
				if lost == 0 && !h.authorized(a.IP) {
					lost = el
				}
			})
			if lost == 0 {
				t.Fatal("the governor kept renewing a scope that is no longer in force")
			}
			if h.p.cont.st != nil {
				t.Fatal("the record of the previous scope must be cleared")
			}
		})
	}
}

// P1: the probe asks about the scope in force NOW, not the one at startup.
func TestContinuity_ProbeUsesTheCurrentScope(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
	h.run(90*time.Second, nil)
	h.probe.mu.Lock()
	if len(h.probe.scopes) == 0 || h.probe.scopes[len(h.probe.scopes)-1] != testTenant+"/"+testSite {
		h.probe.mu.Unlock()
		t.Fatalf("probe scopes %v", h.probe.scopes)
	}
	h.probe.mu.Unlock()
}

// P2: two sessions share one 10 MiB allowance; one uses 6 MiB and leaves; the other may use only 4 MiB more.
func TestContinuity_ASharedAllowanceRemembersWhatADepartedSessionUsed(t *testing.T) {
	h := newContH(t)
	a1 := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	a2 := h.guest("c-2", "10.0.0.12", "96:48:f9:7a:9b:12", "ent-a")
	ss := []shapeplan.Session{a1, a2}
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, ss, map[string]int64{"ent-a": 10 << 20}, nil) }, ss...)
	h.run(65*time.Second, nil)
	h.tc.carry("br-guest", a1.IP, 6<<20)
	h.step(5 * time.Second)
	if !h.authorized(a1.IP) || !h.authorized(a2.IP) {
		t.Fatal("6 MiB of 10 must end nothing")
	}
	if _, err := (&server{phase3: h.p}).endFlows(context.Background(), netIP(a1.IP), "", &nopFlusher{}); err != nil {
		t.Fatal(err)
	}
	h.run(15*time.Second, nil) // metering ticks with only the survivor
	h.tc.carry("br-guest", a2.IP, 3<<20)
	h.step(5 * time.Second)
	if !h.authorized(a2.IP) {
		t.Fatal("6 + 3 = 9 MiB of 10: the survivor must still be online")
	}
	if got := h.p.contStatus().Budgets["ent-a"]; got < 9<<20 {
		t.Fatalf("budget used = %d, want at least 9 MiB (the departed session's 6 MiB must be remembered)", got)
	}
	h.tc.carry("br-guest", a2.IP, 2<<20) // 6 + 5 = 11 MiB > 10
	h.step(5 * time.Second)
	if h.authorized(a2.IP) {
		t.Fatal("the shared allowance was exceeded (6 MiB by the departed device + 5 MiB): the survivor must be removed")
	}
}

// P2: what a departed session used survives a netd restart, and a database refusal counts the same as a disconnect.
func TestContinuity_DepartedUsageSurvivesARestart(t *testing.T) {
	h := newContH(t)
	a1 := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	a2 := h.guest("c-2", "10.0.0.12", "96:48:f9:7a:9b:12", "ent-a")
	ss := []shapeplan.Session{a1, a2}
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, ss, map[string]int64{"ent-a": 10 << 20}, nil) }, ss...)
	h.run(65*time.Second, nil)
	h.tc.carry("br-guest", a1.IP, 6<<20)
	h.step(5 * time.Second)
	h.probe.mu.Lock()
	h.probe.out["c-1"] = true
	h.probe.mu.Unlock()
	h.step(5 * time.Second)
	if h.authorized(a1.IP) {
		t.Fatal("precondition: the database refused c-1")
	}
	h.p.cont = &continuity{statePath: h.p.cont.statePath, journalPath: h.p.cont.journalPath, probe: h.probe}
	if why := h.p.contRestore(); why != "" {
		t.Fatalf("restore: %s", why)
	}
	h.tc.carry("br-guest", a2.IP, 5<<20)
	h.step(5 * time.Second)
	if h.authorized(a2.IP) {
		t.Fatal("after a restart the departed session's 6 MiB was forgotten: the survivor was allowed 5 MiB more")
	}
}

// P3: after a netd-only restart the gate has no MAC bindings; continuity must restore them from the record before renewing.
func TestContinuity_RenewalRestoresTheMACBindingAfterARestart(t *testing.T) {
	h := newContH(t)
	g := newMacGate()
	h.p.gate = g
	h.g = g.fakeGate
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
	h.run(2*time.Minute, nil)
	if err := h.p.cont.saveState(); err != nil {
		t.Fatal(err)
	}
	// netd restarts: a gate with an empty MAC map, the record restored, the kernel element still present.
	g.forget()
	h.p.cont = &continuity{statePath: h.p.cont.statePath, journalPath: h.p.cont.journalPath, probe: h.probe}
	if why := h.p.contRestore(); why != "" {
		t.Fatalf("restore: %s", why)
	}
	g.fakeGate.mu.Lock()
	g.fakeGate.leases[gk("br-guest", netIP(a.IP))] = 20 * time.Second // running down: renewal is due
	g.fakeGate.mu.Unlock()
	h.step(5 * time.Second)
	if !h.authorized(a.IP) {
		t.Fatal("renewal did not happen")
	}
	g.mu.Lock()
	ttl := g.macTTL[gk("br-guest", netIP(a.IP))]
	g.mu.Unlock()
	if ttl <= 0 {
		t.Fatal("the MAC-bound element was not refreshed: the binding was not restored from the record before the renewal")
	}
}

// Restore refuses while Phase 3 is not active.
func TestContinuity_RestoreRefusedWhilePhase3IsInactive(t *testing.T) {
	h := newContH(t)
	a := h.guest("c-1", "10.0.0.11", "96:48:f9:7a:9b:11", "ent-a")
	h.healthy(60, func() *shapeplan.Continuity { return block(time.Hour, []shapeplan.Session{a}, nil, nil) }, a)
	if err := h.p.cont.saveState(); err != nil {
		t.Fatal(err)
	}
	h.p.cont = &continuity{statePath: h.p.cont.statePath, journalPath: h.p.cont.journalPath, probe: h.probe}
	h.p.mode.Active = false
	if why := h.p.contRestore(); why == "" || h.p.cont.st != nil {
		t.Fatalf("a record was restored into an inactive Phase 3 (why=%q)", why)
	}
}

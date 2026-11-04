package main

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/onegate/onegate/data-plane/internal/shape"
	"github.com/onegate/onegate/data-plane/internal/shapeplan"
)

// collidingEntitlements returns two entitlement ids whose hashes land on the same group minor.
func collidingEntitlements(t *testing.T) (string, string) {
	t.Helper()
	seen := map[int]string{}
	for i := 0; i < 100000; i++ {
		id := fmt.Sprintf("ent-%d", i)
		m, _ := shape.GroupMinorFor(id)
		if prev, ok := seen[m]; ok {
			return prev, id
		}
		seen[m] = id
	}
	t.Fatal("no colliding pair found")
	return "", ""
}

// Two entitlements whose hashes collide get separate group classes, each at its own allowance, never one pooled class.
func TestGroups_CollidingEntitlementsGetSeparateGroups(t *testing.T) {
	a, b := collidingEntitlements(t)
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	sa := allocSession("a", "10.0.0.11", a, "SHARED")
	sb := allocSession("b", "10.0.0.12", b, "SHARED")
	sa.DownKbps, sb.DownKbps = 10000, 30000
	if res, err := p.submit(context.Background(), envelope(1, sa, sb), time.Now()); err != nil || res.Failed != 0 || res.Shaped != 2 {
		t.Fatalf("pass: %+v %v", res, err)
	}
	ga, gb := tc.parentOf("br-guest", "10.0.0.11"), tc.parentOf("br-guest", "10.0.0.12")
	if ga == "" || gb == "" || ga == gb {
		t.Fatalf("colliding entitlements share a group: %q and %q", ga, gb)
	}
	if ra, _ := tc.groupRate("br-guest", ga); ra != 10000 {
		t.Fatalf("group of %s at %d, want 10000", a, ra)
	}
	if rb, _ := tc.groupRate("br-guest", gb); rb != 30000 {
		t.Fatalf("group of %s at %d, want 30000", b, rb)
	}
}

// A package change with no other device on the old package keeps the session's class where it is and re-rates the group: nobody is cut off.
func TestGroups_AnEntitlementChangeAloneAdoptsTheGroupInPlace(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11")
	s := allocSession("a", "10.0.0.11", "ent-free", "SHARED")
	s.DownKbps, s.UpKbps = 4000, 4000
	if _, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	group := tc.parentOf("br-guest", "10.0.0.11")
	g.mu.Lock()
	g.calls = nil
	g.mu.Unlock()
	tc.mu.Lock()
	tc.calls = nil
	tc.mu.Unlock()
	s.EntitlementID, s.DownKbps, s.UpKbps = "ent-fullstay", 10000, 10000
	if res, err := p.submit(context.Background(), envelope(2, s), time.Now()); err != nil || res.Failed != 0 || res.Shaped != 1 {
		t.Fatalf("pass after the change: %+v %v", res, err)
	}
	if got := tc.parentOf("br-guest", "10.0.0.11"); got != group {
		t.Fatalf("the class moved to %q, want it kept under %q", got, group)
	}
	if rate, _ := tc.groupRate("br-guest", group); rate != 10000 {
		t.Fatalf("group rate %d, want the new package's 10000", rate)
	}
	g.mu.Lock()
	for _, c := range g.calls {
		if len(c) >= 6 && c[:6] == "revoke" {
			t.Fatalf("the device was cut off for a change that needed no move: %v", g.calls)
		}
	}
	g.mu.Unlock()
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for _, c := range tc.calls {
		if c.op == "delete" {
			t.Fatalf("the class was rebuilt: %+v", c)
		}
	}
}

// After a restart the slots are read back from where the installed classes hang, including a probed one, so nothing is rebuilt.
func TestGroups_ARestartedWriterKeepsEverySlot(t *testing.T) {
	a, b := collidingEntitlements(t)
	tc, g := newFakeTC(), newFakeGate()
	tc.reportParents = true
	p := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	sa := allocSession("a", "10.0.0.11", a, "SHARED")
	sb := allocSession("b", "10.0.0.12", b, "SHARED")
	if _, err := p.submit(context.Background(), envelope(1, sa, sb), time.Now()); err != nil {
		t.Fatal(err)
	}
	ga, gb := tc.parentOf("br-guest", "10.0.0.11"), tc.parentOf("br-guest", "10.0.0.12")

	fresh := allocWriter(t, tc, g, "10.0.0.11", "10.0.0.12")
	fresh.epochs, fresh.classes = p.epochs, p.classes
	got := map[string]int{}
	fresh.assignGroups(context.Background(), map[string]map[int]shapeplan.Session{"br-guest": {
		mustMinor(t, "10.0.0.11"): sa, mustMinor(t, "10.0.0.12"): sb}})
	for id, m := range fresh.groupSlots {
		got[id] = m
	}
	if want, _ := groupMinorOf(ga); got[a] != want {
		t.Fatalf("%s read back as %x, installed under %s", a, got[a], ga)
	}
	if want, _ := groupMinorOf(gb); got[b] != want {
		t.Fatalf("%s read back as %x, installed under %s", b, got[b], gb)
	}
}

func mustMinor(t *testing.T, ip string) int {
	t.Helper()
	m, ok := shape.MinorForIP(net.ParseIP(ip))
	if !ok {
		t.Fatalf("no minor for %s", ip)
	}
	return m
}

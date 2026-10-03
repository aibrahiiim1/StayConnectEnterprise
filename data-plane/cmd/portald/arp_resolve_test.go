package main

// RESOLVING THE SOURCE DEVICE WITHOUT TRUSTING IT, AND WITHOUT REFUSING A GUEST FOR A COLD CACHE.
//
// The hardware address behind a room sign-in used to come from a single read of the kernel's neighbour cache,
// and a miss was answered as "this device is not on a guest network" — the technical refusal that tells a guest
// to go to Reception. A miss does not mean that: the cache has no entry until the kernel has had reason to
// resolve the address, and it reaps entries that go unused. The guest who finds no entry is the one who has just
// taken a DHCP lease and submitted the form immediately.

import (
	"context"
	"net"
	"testing"
	"time"
)

func resolverHandler(cache arpLookup, nudge arpNudge) *handler {
	return &handler{arpCache: cache, arpNudge: nudge, arpSleep: func(time.Duration) {}}
}

// A WARM CACHE IS ANSWERED IMMEDIATELY, and the kernel is not disturbed.
func TestDeviceMACUsesTheCacheWhenItHasTheEntry(t *testing.T) {
	want, _ := net.ParseMAC("02:00:00:ab:cd:01")
	nudged := false
	h := resolverHandler(
		func(net.IP) (net.HardwareAddr, bool) { return want, true },
		func(net.IP) { nudged = true },
	)
	got, ok := h.deviceMAC(context.Background(), net.ParseIP("10.77.0.25"))
	if !ok || got.String() != want.String() {
		t.Fatalf("a cached entry was not used: %v %v", got, ok)
	}
	if nudged {
		t.Error("the kernel was asked to resolve an address it had already resolved")
	}
}

// A COLD CACHE IS RESOLVED, NOT REFUSED. This is the defect: the guest below was previously turned away.
func TestDeviceMACAsksTheKernelWhenTheCacheIsCold(t *testing.T) {
	want, _ := net.ParseMAC("02:00:00:ab:cd:02")
	resolved := false
	nudges := 0
	h := resolverHandler(
		func(net.IP) (net.HardwareAddr, bool) {
			if !resolved {
				return nil, false
			}
			return want, true
		},
		func(net.IP) { nudges++; resolved = true },
	)
	got, ok := h.deviceMAC(context.Background(), net.ParseIP("10.77.0.25"))
	if !ok || got.String() != want.String() {
		t.Fatalf("a resolvable device was refused: %v %v", got, ok)
	}
	if nudges != 1 {
		t.Errorf("the kernel was asked %d times; once is enough", nudges)
	}
}

// AND A DEVICE THAT GENUINELY CANNOT BE PLACED IS STILL REFUSED — bounded, so the refusal still lands inside the
// response-time budget that keeps every refusal indistinguishable.
func TestDeviceMACGivesUpOnADeviceTheKernelCannotPlace(t *testing.T) {
	slept := time.Duration(0)
	h := &handler{
		arpCache: func(net.IP) (net.HardwareAddr, bool) { return nil, false },
		arpNudge: func(net.IP) {},
		arpSleep: func(d time.Duration) { slept += d },
	}
	if _, ok := h.deviceMAC(context.Background(), net.ParseIP("10.77.0.25")); ok {
		t.Fatal("an unresolvable device was given a hardware address")
	}
	if slept > arpResolveWindow {
		t.Errorf("resolution waited %v, past its own %v bound", slept, arpResolveWindow)
	}
	// The bound has to stay well inside the budget, or a refusal could land after the pad that makes every
	// refusal leave at the same moment.
	if arpResolveWindow >= phase3FailureBudget-phase3EnforcementReserve {
		t.Errorf("the resolve window (%v) is not inside the failure budget (%v less %v reserve)",
			arpResolveWindow, phase3FailureBudget, phase3EnforcementReserve)
	}
}

// A CANCELLED REQUEST STOPS WAITING. A guest who closes the page must not hold a goroutine for the whole window.
func TestDeviceMACStopsWhenTheRequestIsAbandoned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := resolverHandler(func(net.IP) (net.HardwareAddr, bool) { return nil, false }, func(net.IP) {})
	if _, ok := h.deviceMAC(ctx, net.ParseIP("10.77.0.25")); ok {
		t.Fatal("an abandoned request produced a hardware address")
	}
}

// THE ADDRESS IS NEVER TAKEN FROM THE CLIENT. There is no path into this resolver that carries a client-supplied
// value: its only inputs are the peer address and the kernel's own table. This pins that the lookup is the only
// source, so a future change that threads a request field through here fails here first.
func TestDeviceMACHasNoClientSuppliedSource(t *testing.T) {
	h := resolverHandler(nil, func(net.IP) { t.Fatal("the kernel was asked without a cache to read") })
	if _, ok := h.deviceMAC(context.Background(), net.ParseIP("10.77.0.25")); ok {
		t.Fatal("a hardware address was produced with no neighbour table to read it from")
	}
}

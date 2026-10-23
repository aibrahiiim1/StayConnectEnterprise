package main

// A cold neighbour cache must be resolved, not refused, and the address is never taken from the client.

import (
	"context"
	"net"
	"testing"
	"time"
)

func resolverHandler(cache arpLookup, nudge arpNudge) *handler {
	return &handler{arpCache: cache, arpNudge: nudge, arpSleep: func(time.Duration) {}}
}

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

// A cold cache is resolved, not refused.
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

// A device that cannot be placed is still refused, bounded inside the response-time budget.
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
	// The bound stays well inside the budget so refusals leave at the same moment.
	if arpResolveWindow >= phase3FailureBudget-phase3EnforcementReserve {
		t.Errorf("the resolve window (%v) is not inside the failure budget (%v less %v reserve)",
			arpResolveWindow, phase3FailureBudget, phase3EnforcementReserve)
	}
}

// A cancelled request stops waiting.
func TestDeviceMACStopsWhenTheRequestIsAbandoned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := resolverHandler(func(net.IP) (net.HardwareAddr, bool) { return nil, false }, func(net.IP) {})
	if _, ok := h.deviceMAC(ctx, net.ParseIP("10.77.0.25")); ok {
		t.Fatal("an abandoned request produced a hardware address")
	}
}

// The only inputs are the peer address and the kernel table; a change threading a request field through here fails this test.
func TestDeviceMACHasNoClientSuppliedSource(t *testing.T) {
	h := resolverHandler(nil, func(net.IP) { t.Fatal("the kernel was asked without a cache to read") })
	if _, ok := h.deviceMAC(context.Background(), net.ParseIP("10.77.0.25")); ok {
		t.Fatal("a hardware address was produced with no neighbour table to read it from")
	}
}

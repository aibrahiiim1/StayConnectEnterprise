package main

// AD-24 (b): an address that leaves the Phase-3 set without a Revoke through the gate (its kernel lease lapsed) has its
// open flows ended, scoped to that address; revoked addresses are not flushed twice, live ones never, and a failure retries.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func lapseGate(t *testing.T) (*flushingGate, *fakeGate, *fakeFlows) {
	inner := newFakeGate()
	fl := &fakeFlows{}
	g := newFlushingGate(inner, fl)
	g.flushLapsed = true
	return g, inner, fl
}

func TestALapsedAuthorizationEndsOnlyThatAddressesFlows(t *testing.T) {
	g, inner, fl := lapseGate(t)
	ctx := context.Background()
	a, b := net.ParseIP("10.0.0.5"), net.ParseIP("10.0.0.6")
	_ = g.Authorize(ctx, "br-guest", a, time.Minute)
	_ = g.Authorize(ctx, "br-guest", b, time.Minute)
	if _, err := g.List(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fl.flushed) != 0 {
		t.Fatalf("live authorizations were flushed: %v", fl.flushed)
	}
	// The kernel expires a's element: it vanishes from the set with no Revoke.
	delete(inner.authorized, gk("br-guest", a))
	delete(inner.leases, gk("br-guest", a))
	if _, err := g.List(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fl.flushed) != 1 || fl.flushed[0] != "10.0.0.5" {
		t.Fatalf("expected exactly 10.0.0.5 to be flushed, got %v", fl.flushed)
	}
	// Not flushed again on later passes.
	_, _ = g.List(ctx)
	_, _ = g.List(ctx)
	if len(fl.flushed) != 1 {
		t.Fatalf("a lapsed address was flushed repeatedly: %v", fl.flushed)
	}
}

func TestARevokedAddressIsNotFlushedASecondTimeByTheSweep(t *testing.T) {
	g, _, fl := lapseGate(t)
	ctx := context.Background()
	ip := net.ParseIP("10.0.0.5")
	_ = g.Authorize(ctx, "br-guest", ip, time.Minute)
	_, _ = g.List(ctx)
	_ = g.Revoke(ctx, "br-guest", ip)
	_, _ = g.List(ctx)
	if len(fl.flushed) != 1 {
		t.Fatalf("revocation flushed once; the sweep must not repeat it: %v", fl.flushed)
	}
}

func TestAnUnreadableSetConcludesNothing(t *testing.T) {
	g, inner, fl := lapseGate(t)
	ctx := context.Background()
	ip := net.ParseIP("10.0.0.5")
	_ = g.Authorize(ctx, "br-guest", ip, time.Minute)
	_, _ = g.List(ctx)
	inner.listErr = errors.New("nft busy")
	if _, err := g.List(ctx); err == nil {
		t.Fatal("the error was swallowed")
	}
	inner.listErr = nil
	if _, err := g.List(ctx); err != nil || len(fl.flushed) != 0 {
		t.Fatalf("an unreadable pass was read as 'everything lapsed': err=%v flushed=%v", err, fl.flushed)
	}
}

func TestAFailedLapseFlushIsRetried(t *testing.T) {
	g, inner, fl := lapseGate(t)
	ctx := context.Background()
	ip := net.ParseIP("10.0.0.5")
	_ = g.Authorize(ctx, "br-guest", ip, time.Minute)
	_, _ = g.List(ctx)
	delete(inner.authorized, gk("br-guest", ip))
	fl.err = errors.New("netlink: EPERM")
	_, _ = g.List(ctx)
	fl.err = nil
	_, _ = g.List(ctx)
	if n := len(fl.flushed); n != 2 || fl.flushed[1] != "10.0.0.5" {
		t.Fatalf("the failed flush was not retried: %v", fl.flushed)
	}
}

func TestAnAddressStillAuthorizedOnAnotherBridgeIsNotFlushed(t *testing.T) {
	g, inner, fl := lapseGate(t)
	ctx := context.Background()
	ip := net.ParseIP("10.0.0.5")
	_ = g.Authorize(ctx, "br-a", ip, time.Minute)
	_ = g.Authorize(ctx, "br-b", ip, time.Minute)
	_, _ = g.List(ctx)
	delete(inner.authorized, gk("br-a", ip))
	_, _ = g.List(ctx)
	if len(fl.flushed) != 0 {
		t.Fatalf("flushing by address would end the other network's live flows: %v", fl.flushed)
	}
}

func TestTheLapseSweepHasAnOffSwitch(t *testing.T) {
	g, inner, fl := lapseGate(t)
	g.flushLapsed = false
	ctx := context.Background()
	ip := net.ParseIP("10.0.0.5")
	_ = g.Authorize(ctx, "br-guest", ip, time.Minute)
	_, _ = g.List(ctx)
	delete(inner.authorized, gk("br-guest", ip))
	_, _ = g.List(ctx)
	if len(fl.flushed) != 0 {
		t.Fatalf("flushed with the switch off: %v", fl.flushed)
	}
}

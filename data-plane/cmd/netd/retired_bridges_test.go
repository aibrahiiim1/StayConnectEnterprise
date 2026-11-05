package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/onegate/onegate/data-plane/internal/netcfg"
)

func retiredApplier(inflight string, live map[string]bool, intent []netcfg.GuestNetwork, intentErr error) *applier {
	return &applier{
		legacyBridge:  "br-lan",
		inFlightFn:    func(context.Context) (string, string, error) { return inflight, "pending_confirmation", nil },
		liveBridgesFn: func() map[string]bool { return live },
		activeIntentFn: func(context.Context) (string, string, []netcfg.GuestNetwork, error) {
			return "rev-1", "", intent, intentErr
		},
	}
}

// Only a bridge that is gone AND that no switched-on network of the confirmed configuration names is retired.
func TestRetiredBridgeCheck_OnlyAGoneAndUnnamedBridgeIsRetired(t *testing.T) {
	intent := []netcfg.GuestNetwork{
		{BridgeName: "br-g95-56c8", Enabled: true},
		{BridgeName: "br-g95-fa3b", Enabled: false},
	}
	check := retiredApplier("", map[string]bool{"br-g95-56c8": true}, intent, nil).retiredBridgeCheck(context.Background())
	if check == nil {
		t.Fatal("no answer although nothing is in flight and every fact was readable")
	}
	for bridge, want := range map[string]bool{
		"br-g95-fa3b": true,  // replaced: gone, and its network is switched off
		"br-g95-56c8": false, // live
		"br-g-0000":   true,  // gone and named by nothing
		"br-lan":      false, // the legacy bridge is never managed here
		"ens192":      false, // not a guest bridge
		"":            false,
	} {
		if got := check(bridge); got != want {
			t.Errorf("retired(%q) = %v, want %v", bridge, got, want)
		}
	}
	// A switched-on network whose bridge is momentarily absent (being rebuilt) is never retired.
	rebuilt := retiredApplier("", map[string]bool{}, intent, nil).retiredBridgeCheck(context.Background())
	if rebuilt("br-g95-56c8") {
		t.Fatal("a confirmed, switched-on network's bridge was taken for a removed one")
	}
}

// While an apply is in flight, or a fact is unreadable, nothing is retired.
func TestRetiredBridgeCheck_NeverDuringAnApplyOrOnUnreadableState(t *testing.T) {
	if retiredApplier("rev-2", map[string]bool{}, nil, nil).retiredBridgeCheck(context.Background()) != nil {
		t.Fatal("bridges were judged while an apply is awaiting confirmation")
	}
	if retiredApplier("", map[string]bool{}, nil, errors.New("db down")).retiredBridgeCheck(context.Background()) != nil {
		t.Fatal("bridges were judged without the confirmed configuration")
	}
	a := retiredApplier("", map[string]bool{}, nil, nil)
	a.inFlightFn = func(context.Context) (string, string, error) { return "", "", errors.New("unreadable") }
	if a.retiredBridgeCheck(context.Background()) != nil {
		t.Fatal("bridges were judged without knowing whether an apply is in flight")
	}
}

// A session on a removed client network is denied and ended once, never retried on a bridge that no longer exists.
func TestApplier_ASessionOnARemovedNetworkIsEnded(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := ownershipWriter(t, tc, g, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}})
	fe := newFakeEnforcement()
	p.enforcement = fe
	p.retired = func(context.Context) func(string) bool { return func(b string) bool { return b == "br-guest" } }

	res, err := p.submit(context.Background(), envelope(1, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatalf("a session on a removed network was shaped or authorized: %+v", res)
	}
	if len(fe.ended) != 1 || fe.ended[0] != "s-1" || fe.state["s-1"] != "ended" {
		t.Fatalf("the session was not ended: %v", fe.ended)
	}
}

// With no retired bridges the same session is enforced as before.
func TestApplier_ALiveNetworkIsUnaffectedByTheRetiredCheck(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := ownershipWriter(t, tc, g, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}})
	fe := newFakeEnforcement()
	p.enforcement = fe
	p.retired = func(context.Context) func(string) bool { return func(string) bool { return false } }

	res, err := p.submit(context.Background(), envelope(1, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 1 || len(fe.ended) != 0 {
		t.Fatalf("a session on a live network was not enforced normally: %+v ended=%v", res, fe.ended)
	}
}

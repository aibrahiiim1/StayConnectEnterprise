package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

type fakePolicyStore struct {
	revisionStore
	p     enforcementPolicy
	found bool
	err   error
}

func (f *fakePolicyStore) LoadEnforcementPolicy(context.Context) (enforcementPolicy, bool, error) {
	return f.p, f.found, f.err
}

func policyApplier(t *testing.T, ps *fakePolicyStore) (*applier, *fakeKernel) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.st = ps
	a.upstreamFn = func() []string { return []string{"172.21.60.1", "172.21.60.2"} }
	return a, k
}

func TestNeverReadPolicyRendersAsBefore(t *testing.T) {
	a, _ := policyApplier(t, &fakePolicyStore{err: errors.New("db down")})
	a.refreshEnforcementPolicy(context.Background())
	if got := a.renderTopo().PrivateReach; got.Deny || len(got.Allow) != 0 {
		t.Fatalf("an unreadable policy must not newly deny anything: %+v", got)
	}
	if _, ok := a.currentPolicy(); ok {
		t.Fatal("policy reported as loaded")
	}
}

func TestMissingPolicyTableIsTheOldBehaviour(t *testing.T) {
	a, _ := policyApplier(t, &fakePolicyStore{found: false})
	a.refreshEnforcementPolicy(context.Background())
	if a.renderTopo().PrivateReach.Deny {
		t.Fatal("a schema without migration 0190 must keep the previous ruleset")
	}
}

func TestPolicyDeniesAndSeedsTheUpstreamAndFollowsTheSetting(t *testing.T) {
	ps := &fakePolicyStore{found: true, p: enforcementPolicy{PrivateDeny: true, AllowUpstream: true, PrivateAllow: []string{"10.9.0.0/16"}, MACBinding: false, CutoffSeconds: 5}}
	a, k := policyApplier(t, ps)
	a.setInForce(testIntent(), "")
	a.refreshEnforcementPolicy(context.Background())
	pr := a.renderTopo().PrivateReach
	if !pr.Deny {
		t.Fatal("deny not applied")
	}
	want := netcfg.NormalizeAllow([]string{"10.9.0.0/16", "172.21.60.1", "172.21.60.2"})
	if got := netcfg.NormalizeAllow(pr.Allow); len(got) != len(want) {
		t.Fatalf("allow = %v, want %v (the hotel's allow-list plus its own gateway and resolvers)", got, want)
	}

	// The periodic pass installs it; a later setting change is installed on the next pass without a revision.
	a.reassertNftStructure(context.Background(), "periodic")
	fp1 := k.fp
	if fp1 != netcfg.RenderFingerprint(testIntent(), a.renderTopo()) {
		t.Fatalf("live fingerprint is not the policy render")
	}
	ps.p.PrivateAllow = nil
	ps.p.AllowUpstream = false
	a.reassertNftStructure(context.Background(), "periodic")
	if k.fp == fp1 || k.fp != netcfg.RenderFingerprint(testIntent(), a.renderTopo()) {
		t.Fatalf("a changed setting was not re-asserted")
	}
	if len(a.renderTopo().PrivateReach.Allow) != 0 {
		t.Fatalf("allow-list should be empty now: %v", a.renderTopo().PrivateReach.Allow)
	}
	// Off-switch: the old ruleset comes back.
	ps.p.PrivateDeny = false
	a.reassertNftStructure(context.Background(), "periodic")
	if a.renderTopo().PrivateReach.Deny || k.fp != netcfg.RenderFingerprint(testIntent(), testTopo()) {
		t.Fatal("switching the policy off did not restore the previous ruleset")
	}
}

func TestAReadFailureKeepsThePolicyInForce(t *testing.T) {
	ps := &fakePolicyStore{found: true, p: enforcementPolicy{PrivateDeny: true, AllowUpstream: true, PrivateAllow: []string{"10.9.0.0/16"}}}
	a, _ := policyApplier(t, ps)
	a.refreshEnforcementPolicy(context.Background())
	ps.err = errors.New("db down")
	a.upstreamFn = func() []string { return nil }
	a.refreshEnforcementPolicy(context.Background())
	if !a.renderTopo().PrivateReach.Deny {
		t.Fatal("a transient read failure lifted the deny")
	}
}

func TestATransientUpstreamLookupFailureDoesNotShrinkTheExceptions(t *testing.T) {
	ps := &fakePolicyStore{found: true, p: enforcementPolicy{PrivateDeny: true, AllowUpstream: true}}
	a, _ := policyApplier(t, ps)
	a.refreshEnforcementPolicy(context.Background())
	a.upstreamFn = func() []string { return nil }
	a.refreshEnforcementPolicy(context.Background())
	if n := len(a.renderTopo().PrivateReach.Allow); n != 2 {
		t.Fatalf("upstream exceptions shrank to %d after one failed lookup", n)
	}
}

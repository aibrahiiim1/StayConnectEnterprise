package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/enforce"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

func testArm(t *testing.T, counterPath string) *phase3 {
	t.Helper()
	c := newPlanCounter(counterPath)
	c.start()
	return &phase3{
		tenant: "tenant-1", site: "site-1",
		scope: planScope{TenantID: "tenant-1", SiteID: "site-1", ApplianceID: "appl-1",
			AssignmentID: "asg-1", AssignmentGe: 7},
		plans: c,
	}
}

func derivedPlan() enforce.Plan {
	return enforce.Plan{
		Tear: []enforce.SessionShape{{SessionID: "ended-1", IP: "10.0.0.8", Bridge: "br-guest"}},
		Shape: []enforce.SessionShape{
			{SessionID: "live-1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 8000, UpKbps: 3000},
			{SessionID: "live-2", IP: "10.0.0.2", DownKbps: 4000, UpKbps: 1500}, // no bridge: the fallback applies
		},
	}
}

// "Not mentioned" must never end access: a truncated body would look like a mass revocation.
func TestEnvelopeStatesTheWholeDesiredState(t *testing.T) {
	p := testArm(t, filepath.Join(t.TempDir(), "gen.json"))
	env := p.buildEnvelope(derivedPlan(), []string{"br-empty"}, "br-lan", time.Now())

	if len(env.Sessions) != 3 {
		t.Fatalf("envelope carried %d sessions, want all 3", len(env.Sessions))
	}
	byID := map[string]shapeplan.Session{}
	for _, s := range env.Sessions {
		byID[s.SessionID] = s
	}
	if byID["ended-1"].Entitled {
		t.Fatal("a session being torn down was marked entitled")
	}
	if !byID["live-1"].Entitled || byID["live-1"].DownKbps != 8000 {
		t.Fatalf("an entitled session lost its rates: %+v", byID["live-1"])
	}
	if byID["live-2"].Bridge != "br-lan" {
		t.Fatalf("the fallback bridge was not applied: %+v", byID["live-2"])
	}
	if env.DesiredStateHash != shapeplan.HashDesiredState(env.ManagedBridges, env.Sessions) {
		t.Fatal("the envelope hash does not cover its own bridges and sessions")
	}
	// A bridge with no sessions must still be declared: nothing else tells the applier to look there.
	if got := env.ManagedBridges; len(got) != 3 ||
		got[0] != "br-empty" || got[1] != "br-guest" || got[2] != "br-lan" {
		t.Fatalf("declared bridges = %v, want the session bridges, the fallback and the empty one", got)
	}
	if env.ContractVersion != shapeplan.ContractVersion {
		t.Fatalf("contract version = %q", env.ContractVersion)
	}
	if env.TenantID != "tenant-1" || env.SiteID != "site-1" || env.ApplianceID != "appl-1" || env.AssignmentGen != 7 {
		t.Fatalf("the envelope is not scoped to this appliance: %+v", env)
	}
	if !env.ExpiresAt.After(env.GeneratedAt) {
		t.Fatalf("the plan has no validity window: %v -> %v", env.GeneratedAt, env.ExpiresAt)
	}
}

// The hash must not depend on row order, or an identical desired state would hash differently each tick.
func TestDesiredStateHashIsOrderIndependentAndContentSensitive(t *testing.T) {
	a := []shapeplan.Session{
		{SessionID: "s1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 100, UpKbps: 50, Entitled: true},
		{SessionID: "s2", IP: "10.0.0.2", Bridge: "br-guest", Entitled: false},
	}
	b := []shapeplan.Session{a[1], a[0]}
	if shapeplan.HashDesiredState(nil, a) != shapeplan.HashDesiredState(nil, b) {
		t.Fatal("the same desired state hashed differently in a different order")
	}
	changed := []shapeplan.Session{a[0], {SessionID: "s2", IP: "10.0.0.2", Bridge: "br-guest", Entitled: true}}
	if shapeplan.HashDesiredState(nil, a) == shapeplan.HashDesiredState(nil, changed) {
		t.Fatal("flipping a session from revoked to entitled did not change the hash")
	}
	if shapeplan.HashDesiredState(nil, a) == shapeplan.HashDesiredState(nil, a[:1]) {
		t.Fatal("a truncated desired state hashed the same as the full one")
	}
	if shapeplan.HashDesiredState([]string{"br-a", "br-b"}, a) == shapeplan.HashDesiredState([]string{"br-a"}, a) {
		t.Fatal("trimming the managed-bridge list did not change the hash")
	}
}

func TestPlanGenerationSurvivesAProducerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gen.json")

	p := testArm(t, path)
	var last int64
	for i := 0; i < 3; i++ {
		env := p.buildEnvelope(derivedPlan(), []string{"br-empty"}, "br-lan", time.Now())
		if env.PlanGeneration <= last {
			t.Fatalf("generation went backwards: %d after %d", env.PlanGeneration, last)
		}
		last = env.PlanGeneration
	}

	restarted := testArm(t, path)
	env := restarted.buildEnvelope(derivedPlan(), []string{"br-empty"}, "br-lan", time.Now())
	if env.PlanGeneration <= last {
		t.Fatalf("a restarted producer re-issued generation %d after %d", env.PlanGeneration, last)
	}
	if env.ProducerRuntimeGen == 0 {
		t.Fatal("a restarted producer did not claim a new runtime generation")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the generation was not persisted: %v", err)
	}
	var st counterState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Generation != env.PlanGeneration {
		t.Fatalf("persisted generation %d, issued %d", st.Generation, env.PlanGeneration)
	}
}

// A netd refusal must surface with its reason, neither as a kernel problem nor as success.
func TestRefusedPlanIsReportedWithItsReason(t *testing.T) {
	p := testArm(t, "")
	p.enf = nil

	refusing := &fakeNetd{result: shapingResult{Accepted: false, Reason: shapeplan.ReasonWrongSite}}
	res, err := refusing.SubmitShapingPlan(context.Background(), p.buildEnvelope(derivedPlan(), []string{"br-empty"}, "br-lan", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted {
		t.Fatal("the fake claimed acceptance")
	}

	p.degraded = ""
	if !res.Accepted {
		p.degraded = "netd refused the shaping plan: " + res.Reason
	}
	if p.Degraded() == "" {
		t.Fatal("a refused plan was not surfaced as degraded")
	}

	down := &fakeNetd{err: errors.New("netd socket unavailable")}
	if _, err := down.SubmitShapingPlan(context.Background(), shapeplan.Envelope{}); err == nil {
		t.Fatal("expected a transport failure")
	}
}

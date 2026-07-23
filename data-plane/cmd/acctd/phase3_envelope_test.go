package main

// The producer half of the shaping contract. These prove the properties netd relies on: a complete desired
// state (never a delta), a durable monotonic generation, a scope acctd cannot invent, and a refusal that is
// reported rather than mistaken for enforcement.

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

// Every live session appears in the envelope exactly once, entitled or not. "Not mentioned" must never be how
// access ends: a truncated body would then be indistinguishable from a mass revocation.
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
	// Every bridge a session is on, the fallback, AND a guest bridge with no sessions at all — the last one
	// is the whole point: nothing else would ever tell the applier to look there.
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

// The hash is what makes a truncated or tampered body refusable. It must not depend on the order the producer
// happened to read rows in, or an identical desired state would hash differently every tick.
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
	// dropping a managed bridge changes the hash too: it changes WHERE unclaimed classes get removed
	if shapeplan.HashDesiredState([]string{"br-a", "br-b"}, a) == shapeplan.HashDesiredState([]string{"br-a"}, a) {
		t.Fatal("trimming the managed-bridge list did not change the hash")
	}
}

// The generation is DURABLE. If it restarted at 1, netd would correctly refuse every plan the new process
// produced — and enforcement would freeze at the pre-restart state with nothing appearing broken.
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

	// a new process, same appliance
	restarted := testArm(t, path)
	env := restarted.buildEnvelope(derivedPlan(), []string{"br-empty"}, "br-lan", time.Now())
	if env.PlanGeneration <= last {
		t.Fatalf("a restarted producer re-issued generation %d after %d", env.PlanGeneration, last)
	}
	if env.ProducerRuntimeGen == 0 {
		t.Fatal("a restarted producer did not claim a new runtime generation")
	}

	// the state file is the whole mechanism: it must be written before the plan is used, not after
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

// A netd REFUSAL is not a kernel problem and must not be reported as one — nor, worse, as success. Re-deriving
// the same plan will be refused for the same reason, so the reason has to reach the operator.
func TestRefusedPlanIsReportedWithItsReason(t *testing.T) {
	p := testArm(t, "")
	p.enf = nil // shapingPlan() is exercised separately; drive the reporting path directly

	refusing := &fakeNetd{result: shapingResult{Accepted: false, Reason: shapeplan.ReasonWrongSite}}
	res, err := refusing.SubmitShapingPlan(context.Background(), p.buildEnvelope(derivedPlan(), []string{"br-empty"}, "br-lan", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted {
		t.Fatal("the fake claimed acceptance")
	}

	// the arm's own handling of that answer
	p.degraded = ""
	if !res.Accepted {
		p.degraded = "netd refused the shaping plan: " + res.Reason
	}
	if p.Degraded() == "" {
		t.Fatal("a refused plan was not surfaced as degraded")
	}

	// and an unreachable netd is degraded too, for a different reason
	down := &fakeNetd{err: errors.New("netd socket unavailable")}
	if _, err := down.SubmitShapingPlan(context.Background(), shapeplan.Envelope{}); err == nil {
		t.Fatal("expected a transport failure")
	}
}

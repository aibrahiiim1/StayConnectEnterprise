package shapeplan

import (
	"testing"
	"time"
)

// Unit tests of the wire contract on its own (audit Phase 6 CC4): until now it was reached only through netd's
// and acctd's tests.

var testScope = Scope{TenantID: "t1", SiteID: "s1", ApplianceID: "a1", AssignGen: 3}

func validEnv(now time.Time) Envelope {
	env := Envelope{
		ContractVersion: ContractVersion, TenantID: "t1", SiteID: "s1", ApplianceID: "a1",
		AssignmentGen: 3, PlanGeneration: 10, GeneratedAt: now, ExpiresAt: now.Add(90 * time.Second),
		ManagedBridges: []string{"br-g20", "br-lan"},
		Sessions: []Session{
			{SessionID: "s-1", DeviceID: "d-1", IP: "10.20.0.5", Bridge: "br-g20", DownKbps: 8000, UpKbps: 2000, Entitled: true},
			{SessionID: "s-2", DeviceID: "d-2", IP: "10.20.0.6", Bridge: "br-g20", Entitled: false, EndReason: "EXPIRED"},
		},
	}
	env.DesiredStateHash = HashDesiredState(env.ManagedBridges, env.Sessions)
	return env
}

func TestValidateAcceptsAWellFormedPlan(t *testing.T) {
	now := time.Now()
	if reason, ok := Validate(validEnv(now), testScope, Accepted{}, false, now); !ok {
		t.Fatalf("a well-formed plan was refused: %s", reason)
	}
}

func TestValidateRefusals(t *testing.T) {
	now := time.Now()
	rehash := func(e *Envelope) { e.DesiredStateHash = HashDesiredState(e.ManagedBridges, e.Sessions) }
	cases := []struct {
		name   string
		mutate func(*Envelope)
		want   string
	}{
		{"contract", func(e *Envelope) { e.ContractVersion = "phase3-shaping/1" }, ReasonUnsupportedContract},
		{"tenant", func(e *Envelope) { e.TenantID = "other" }, ReasonWrongTenant},
		{"site", func(e *Envelope) { e.SiteID = "" }, ReasonWrongSite},
		{"appliance", func(e *Envelope) { e.ApplianceID = "other" }, ReasonWrongAppliance},
		{"assignment", func(e *Envelope) { e.AssignmentGen = 2 }, ReasonStaleAssignment},
		{"generation", func(e *Envelope) { e.PlanGeneration = 0 }, ReasonInvalidGeneration},
		{"expired", func(e *Envelope) { e.ExpiresAt = now.Add(-time.Second) }, ReasonExpiredPlan},
		{"no bridges", func(e *Envelope) { e.ManagedBridges = nil; rehash(e) }, ReasonNoManagedBridges},
		{"tampered session", func(e *Envelope) { e.Sessions[0].DownKbps = 1_000_000 }, ReasonHashMismatch},
		{"tampered boundary", func(e *Envelope) { t := now.Add(time.Hour); e.Sessions[0].AccessEndsAt = &t }, ReasonHashMismatch},
		{"option-like bridge", func(e *Envelope) { e.ManagedBridges = append(e.ManagedBridges, "-all"); rehash(e) }, ReasonBadBridgeName},
		{"newline bridge", func(e *Envelope) { e.ManagedBridges = append(e.ManagedBridges, "br\nx"); rehash(e) }, ReasonBadBridgeName},
		{"long bridge", func(e *Envelope) { e.ManagedBridges = append(e.ManagedBridges, "br-0123456789abc"); rehash(e) }, ReasonBadBridgeName},
	}
	for _, c := range cases {
		e := validEnv(now)
		c.mutate(&e)
		if reason, ok := Validate(e, testScope, Accepted{}, false, now); ok || reason != c.want {
			t.Errorf("%s: got (%q, %v), want %q", c.name, reason, ok, c.want)
		}
	}
}

func TestValidateGenerationHistory(t *testing.T) {
	now := time.Now()
	env := validEnv(now)
	last := Accepted{Generation: 10, Hash: env.DesiredStateHash}
	if _, ok := Validate(env, testScope, last, true, now); !ok {
		t.Fatal("an exact resubmission of the accepted generation was refused")
	}
	older := validEnv(now)
	older.PlanGeneration = 9
	if r, ok := Validate(older, testScope, last, true, now); ok || r != ReasonStaleGeneration {
		t.Fatalf("an older generation: (%q, %v)", r, ok)
	}
	conflict := validEnv(now)
	conflict.Sessions[0].UpKbps = 1
	conflict.DesiredStateHash = HashDesiredState(conflict.ManagedBridges, conflict.Sessions)
	if r, ok := Validate(conflict, testScope, last, true, now); ok || r != ReasonGenerationConflict {
		t.Fatalf("two payloads under one generation: (%q, %v)", r, ok)
	}
	// An empty managed-bridge entry is tolerated (the applier skips it), as before.
	blank := validEnv(now)
	blank.ManagedBridges = append(blank.ManagedBridges, "")
	blank.DesiredStateHash = HashDesiredState(blank.ManagedBridges, blank.Sessions)
	if r, ok := Validate(blank, testScope, Accepted{}, false, now); !ok {
		t.Fatalf("an empty bridge entry was refused: %s", r)
	}
}

func TestHashIsOrderIndependent(t *testing.T) {
	now := time.Now()
	a := validEnv(now)
	b := validEnv(now)
	b.ManagedBridges = []string{"br-lan", "br-g20"}
	b.Sessions = []Session{a.Sessions[1], a.Sessions[0]}
	if HashDesiredState(a.ManagedBridges, a.Sessions) != HashDesiredState(b.ManagedBridges, b.Sessions) {
		t.Fatal("the same desired state in another order hashed differently")
	}
}

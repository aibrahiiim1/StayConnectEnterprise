package shapeplan

import (
	"testing"
	"time"
)

func sampleBlock(now time.Time) Continuity {
	end := now.Add(time.Hour)
	c := Continuity{WindowSeconds: 3600, AsOf: now,
		Sessions: []ContinuitySession{{SessionID: "s1", HardEnd: &end}, {SessionID: "s2"}},
		Budgets:  []ContinuityBudget{{EntitlementID: "e1", RemainingBytes: 1000}}}
	c.ContentsHash = HashContinuity(c)
	return c
}

func TestContinuityIsOutsideTheDesiredStateHash(t *testing.T) {
	now := time.Now()
	s := []Session{{SessionID: "a", Entitled: true}}
	h := HashDesiredState([]string{"br"}, s)
	env := Envelope{ManagedBridges: []string{"br"}, Sessions: s}
	blk := sampleBlock(now)
	env.Continuity = &blk
	if HashDesiredState(env.ManagedBridges, env.Sessions) != h {
		t.Fatal("a continuity block changed the desired-state hash: the fast path would never apply")
	}
}

func TestValidateContinuity(t *testing.T) {
	now := time.Now()
	good := sampleBlock(now)
	if r, ok := ValidateContinuity(good, now); !ok {
		t.Fatal(r)
	}
	cases := map[string]func(c *Continuity){
		"altered budget": func(c *Continuity) { c.Budgets[0].RemainingBytes *= 10 },
		"altered end":    func(c *Continuity) { e := now.Add(48 * time.Hour); c.Sessions[0].HardEnd = &e },
		"window zero":    func(c *Continuity) { c.WindowSeconds = 0 },
		"window huge":    func(c *Continuity) { c.WindowSeconds = MaxContinuityWindowSeconds + 1 },
		"stale":          func(c *Continuity) { c.AsOf = now.Add(-MaxBudgetAge - time.Second) },
		"future":         func(c *Continuity) { c.AsOf = now.Add(time.Hour) },
	}
	for name, mutate := range cases {
		c := sampleBlock(now)
		mutate(&c)
		if name == "window huge" || name == "stale" || name == "future" || name == "window zero" {
			c.ContentsHash = HashContinuity(c) // a self-consistent but out-of-range block
		}
		if _, ok := ValidateContinuity(c, now); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOffContinuityIsAuthenticAndDistinct(t *testing.T) {
	off := OffContinuity()
	if !off.IsOff() {
		t.Fatal("not off")
	}
	if _, ok := ValidateContinuity(*off, time.Now()); ok {
		t.Fatal("an off block must not pass as a window")
	}
	b := sampleBlock(time.Now())
	if b.IsOff() {
		t.Fatal("a real block is not off")
	}
	forged := Continuity{}
	if forged.IsOff() {
		t.Fatal("an unhashed empty block must not count as an explicit off")
	}
}

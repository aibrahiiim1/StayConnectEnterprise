package main

import (
	"context"
	"testing"
	"time"
)

// GDPR audit C5: the Overview warns while the database accepts its default password, checks at most every six
// hours, and raises nothing when the answer is not known.
func TestTheDefaultDatabasePasswordIsWarnedAbout(t *testing.T) {
	calls := 0
	found, ok := true, true
	c := &defaultDBPasswordCheck{probe: func(context.Context) (bool, bool) { calls++; return found, ok }}
	if !c.inUse(context.Background()) || !c.inUse(context.Background()) || calls != 1 {
		t.Fatalf("default accepted: inUse must be true and probed once, got %d probes", calls)
	}
	found = false
	c.checked = time.Now().Add(-defaultDBPasswordEvery)
	if c.inUse(context.Background()) || calls != 2 {
		t.Fatalf("a refused default must clear the warning on the next check (%d probes)", calls)
	}
	// An unreachable database keeps the last known answer and never invents one.
	found, ok = true, false
	c.checked = time.Now().Add(-defaultDBPasswordEvery)
	if c.inUse(context.Background()) {
		t.Fatal("an unknown answer must not raise the warning")
	}
	if (&defaultDBPasswordCheck{probe: func(context.Context) (bool, bool) { return true, false }}).inUse(context.Background()) {
		t.Fatal("never known: no warning")
	}
	var none *defaultDBPasswordCheck
	if none.inUse(context.Background()) {
		t.Fatal("no check configured: no warning")
	}

	items := deriveAttention(attentionInput{DefaultDBPassword: true})
	var raised bool
	for _, it := range items {
		raised = raised || it.ID == "db-default-password"
	}
	if !raised {
		t.Fatalf("the Overview must carry the warning: %v", items)
	}
	for _, it := range deriveAttention(attentionInput{}) {
		if it.ID == "db-default-password" {
			t.Fatal("no warning without the finding")
		}
	}
}

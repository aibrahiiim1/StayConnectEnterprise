package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// The plan tells netd what acctd stored, per session, so a regroup deletes a class only after its bytes are accounted.
func TestStoredCountersAreStampedOnThePlan(t *testing.T) {
	p := &phase3{}
	p.publishStored(map[string]ingestMark{"s1": {epoch: 7, up: 100, down: 900}})
	sessions := []shapeplan.Session{{SessionID: "s1"}, {SessionID: "s2"}}
	p.stampStored(sessions)
	if s := sessions[0]; s.ObservedEpoch != 7 || s.ObservedUp != 100 || s.ObservedDown != 900 {
		t.Fatalf("stored counters not stamped: %+v", s)
	}
	if s := sessions[1]; s.ObservedEpoch != 0 || s.ObservedUp != 0 || s.ObservedDown != 0 {
		t.Fatalf("a session with nothing stored was stamped: %+v", s)
	}
	// Not desired state: the stamp does not change the plan's hash, so the unchanged-plan fast path keeps working.
	if shapeplan.HashDesiredState(nil, sessions) != shapeplan.HashDesiredState(nil, []shapeplan.Session{{SessionID: "s1"}, {SessionID: "s2"}}) {
		t.Fatal("the stored counters changed the desired-state hash")
	}
}

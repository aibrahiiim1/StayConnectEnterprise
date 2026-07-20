package stayengine

import (
	"regexp"
	"testing"
)

func stay(status, room string, lv int, applied ...string) *StayView {
	set := map[string]bool{}
	for _, a := range applied {
		set[a] = true
	}
	return &StayView{ID: "stay-1", Status: status, Room: room, LifecycleVersion: lv,
		AppliedIdentity: func(id string) bool { return set[id] }}
}

func TestResolve_GuestIn(t *testing.T) {
	gi := InboxEvent{EventIdentity: "e1", EventType: EvGuestIn, Reservation: "R1", Room: "101"}

	// no existing stay → create
	if d := Resolve(gi, nil); d.Op != OpCreateStay || d.NewStatus != "IN_HOUSE" {
		t.Fatalf("new GI must create IN_HOUSE stay, got %+v", d)
	}
	// existing IN_HOUSE, same room → update (idempotent-ish attribute refresh)
	if d := Resolve(gi, stay("IN_HOUSE", "101", 1)); d.Op != OpUpdateStay {
		t.Fatalf("GI same room → update, got %+v", d)
	}
	// existing IN_HOUSE, different room → room move (episode preserved)
	if d := Resolve(gi, stay("IN_HOUSE", "202", 1)); d.Op != OpRoomMove || !d.RoomMove {
		t.Fatalf("GI new room → room move, got %+v", d)
	}
	// CHECKED_OUT → reinstate (exactly one lifecycle bump)
	if d := Resolve(gi, stay("CHECKED_OUT", "101", 2)); d.Op != OpReinstate || !d.Reinstate || d.NewStatus != "IN_HOUSE" {
		t.Fatalf("GI on checked-out → reinstate, got %+v", d)
	}
	// RESERVED → becomes IN_HOUSE
	if d := Resolve(gi, stay("RESERVED", "101", 1)); d.Op != OpUpdateStay || d.NewStatus != "IN_HOUSE" {
		t.Fatalf("GI on reserved → IN_HOUSE, got %+v", d)
	}
}

func TestResolve_GuestChange(t *testing.T) {
	gc := InboxEvent{EventIdentity: "e2", EventType: EvGuestChange, Reservation: "R1", Room: "101"}
	if d := Resolve(gc, nil); d.Op != OpManualReview || d.ReviewCode != "GC_UNKNOWN_STAY" {
		t.Fatalf("GC unknown stay → manual review, got %+v", d)
	}
	if d := Resolve(gc, stay("IN_HOUSE", "101", 1)); d.Op != OpUpdateStay {
		t.Fatalf("GC same room → update, got %+v", d)
	}
	moved := InboxEvent{EventIdentity: "e3", EventType: EvGuestChange, Reservation: "R1", Room: "303"}
	if d := Resolve(moved, stay("IN_HOUSE", "101", 1)); d.Op != OpRoomMove {
		t.Fatalf("GC new room → room move, got %+v", d)
	}
	if d := Resolve(gc, stay("CHECKED_OUT", "101", 1)); d.Op != OpManualReview || d.ReviewCode != "GC_ON_TERMINAL_STAY" {
		t.Fatalf("GC on checked-out → manual review, got %+v", d)
	}
}

func TestResolve_GuestOut(t *testing.T) {
	go_ := InboxEvent{EventIdentity: "e4", EventType: EvGuestOut, Reservation: "R1", Room: "101"}
	if d := Resolve(go_, nil); d.Op != OpManualReview || d.ReviewCode != "GO_UNKNOWN_STAY" {
		t.Fatalf("GO unknown → review, got %+v", d)
	}
	if d := Resolve(go_, stay("IN_HOUSE", "101", 1)); d.Op != OpCheckout || d.NewStatus != "CHECKED_OUT" {
		t.Fatalf("GO in-house → checkout, got %+v", d)
	}
	// repeated GO on an already-departed stay → idempotent skip
	if d := Resolve(go_, stay("CHECKED_OUT", "101", 1)); d.Op != OpSkipDuplicate {
		t.Fatalf("GO on checked-out → skip duplicate, got %+v", d)
	}
}

func TestResolve_Idempotent(t *testing.T) {
	gi := InboxEvent{EventIdentity: "dup", EventType: EvGuestIn, Reservation: "R1", Room: "101"}
	// the SAME event identity already applied → deterministic no-op regardless of type/state
	if d := Resolve(gi, stay("IN_HOUSE", "101", 1, "dup")); d.Op != OpSkipDuplicate {
		t.Fatalf("already-applied identity → skip duplicate, got %+v", d)
	}
}

func TestResolve_Deterministic(t *testing.T) {
	gi := InboxEvent{EventIdentity: "e", EventType: EvGuestIn, Reservation: "R1", Room: "202"}
	cur := stay("IN_HOUSE", "101", 1)
	if Resolve(gi, cur) != Resolve(gi, cur) {
		t.Fatal("Resolve must be deterministic for the same (event, state)")
	}
}

// TestResolve_ReviewCodesBounded proves every emitted review code is a bounded machine code (matches the
// stay_events.review_code grammar), so nothing PII/free-text can reach a terminal outcome.
func TestResolve_ReviewCodesBounded(t *testing.T) {
	re := regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	cases := []Decision{
		Resolve(InboxEvent{EventType: EvGuestChange}, nil),
		Resolve(InboxEvent{EventType: EvGuestOut}, nil),
		Resolve(InboxEvent{EventType: EvGuestChange}, stay("CHECKED_OUT", "1", 1)),
		Resolve(InboxEvent{EventType: EvGuestOut}, stay("RESERVED", "1", 1)),
		Resolve(InboxEvent{EventType: EvGuestIn}, stay("CANCELLED", "1", 1)),
		Resolve(InboxEvent{EventType: "ZZ"}, nil),
	}
	for _, d := range cases {
		if d.Op == OpManualReview && !re.MatchString(d.ReviewCode) {
			t.Errorf("review code %q is not a bounded machine code", d.ReviewCode)
		}
	}
}

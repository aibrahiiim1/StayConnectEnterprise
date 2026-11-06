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

	if d := Resolve(gi, nil); d.Op != OpCreateStay || d.NewStatus != "IN_HOUSE" {
		t.Fatalf("new GI must create IN_HOUSE stay, got %+v", d)
	}
	if d := Resolve(gi, stay("IN_HOUSE", "101", 1)); d.Op != OpUpdateStay {
		t.Fatalf("GI same room → update, got %+v", d)
	}
	if d := Resolve(gi, stay("IN_HOUSE", "202", 1)); d.Op != OpRoomMove || !d.RoomMove {
		t.Fatalf("GI new room → room move, got %+v", d)
	}
	if d := Resolve(gi, stay("CHECKED_OUT", "101", 2)); d.Op != OpReinstate || !d.Reinstate || d.NewStatus != "IN_HOUSE" {
		t.Fatalf("GI on checked-out → reinstate, got %+v", d)
	}
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
	if d := Resolve(go_, stay("CHECKED_OUT", "101", 1)); d.Op != OpSkipDuplicate {
		t.Fatalf("GO on checked-out → skip duplicate, got %+v", d)
	}
}

// Protel announces a room move as check-out plus check-in of the same reservation (FIAS 2.20 p. 46, either order). Only a
// different room of the same reservation makes it a move; timing plays no part.
func TestResolve_RoomMoveAnnouncedAsCheckoutAndCheckin(t *testing.T) {
	outOld := InboxEvent{EventIdentity: "o", EventType: EvGuestOut, Reservation: "R1", Room: "101"}
	inNew := InboxEvent{EventIdentity: "i", EventType: EvGuestIn, Reservation: "R1", Room: "202"}

	// Check-in first: the stay is already in 202, so the old room's check-out ends nothing.
	if d := Resolve(outOld, stay("IN_HOUSE", "202", 1)); d.Op != OpVacateOldRoom || d.NewStatus != "" {
		t.Fatalf("GO of a room the reservation has left: got %+v, want VACATE_OLD_ROOM", d)
	}
	// Check-out first: a real checkout of 101, then the check-in to 202 reinstates as a move.
	if d := Resolve(outOld, stay("IN_HOUSE", "101", 1)); d.Op != OpCheckout {
		t.Fatalf("GO of the stay's own room must still check out, got %+v", d)
	}
	if d := Resolve(inNew, stay("CHECKED_OUT", "101", 1)); d.Op != OpReinstate || !d.Reinstate || !d.RoomMove {
		t.Fatalf("GI into another room after the checkout: got %+v, want a reinstatement marked as a room move", d)
	}
	// The same room after a checkout is a reinstatement (contract 7.2), not a move.
	if d := Resolve(InboxEvent{EventIdentity: "s", EventType: EvGuestIn, Reservation: "R1", Room: "101"}, stay("CHECKED_OUT", "101", 1)); d.RoomMove {
		t.Fatalf("a same-room check-in after a checkout is not a room move: %+v", d)
	}
	// Without a known room on either side nothing is inferred.
	if d := Resolve(InboxEvent{EventIdentity: "n", EventType: EvGuestOut, Reservation: "R1"}, stay("IN_HOUSE", "101", 1)); d.Op != OpCheckout {
		t.Fatalf("a GO without a room must check out, got %+v", d)
	}
	if d := Resolve(outOld, stay("IN_HOUSE", "", 1)); d.Op != OpCheckout {
		t.Fatalf("a stay with no recorded room must check out, got %+v", d)
	}
	if d := Resolve(inNew, stay("CHECKED_OUT", "", 1)); d.RoomMove {
		t.Fatalf("a checked-out stay with no recorded room is not a move: %+v", d)
	}
}

func TestResolve_Idempotent(t *testing.T) {
	gi := InboxEvent{EventIdentity: "dup", EventType: EvGuestIn, Reservation: "R1", Room: "101"}
	if d := Resolve(gi, stay("IN_HOUSE", "101", 1, "dup")); d.Op != OpSkipDuplicate {
		t.Fatalf("already-applied identity → skip duplicate, got %+v", d)
	}
}

func TestResolve_Deterministic(t *testing.T) {
	gi := InboxEvent{EventIdentity: "e", EventType: EvGuestIn, Reservation: "R1", Room: "202"}
	// Compared with the expected decision over separately built inputs, so the test can fail (audit P4 CC2).
	want := Decision{Op: OpRoomMove, RoomMove: true}
	for i := 0; i < 3; i++ {
		if got := Resolve(gi, stay("IN_HOUSE", "101", 1)); got != want {
			t.Fatalf("run %d: Resolve = %+v, want %+v", i, got, want)
		}
	}
}

// Review codes must match the stay_events.review_code grammar so no PII or free text reaches an outcome.
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

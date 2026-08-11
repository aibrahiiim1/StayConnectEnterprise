// Package stayengine is the Increment-4 transactional Stay ingestion engine. It consumes ONLY durable inbox
// rows (iam_v2.stay_events that are LIVE, or RESYNC rows whose generation has been published) and resolves
// each into an authoritative Stay operation, moving the event PENDING→terminal exactly once. Stay identity
// and lifecycle resolution live HERE — never in the read-only connector, whose StayResolutionCandidate is a
// non-authoritative hint only. The engine issues NO financial command and implements NO PMS Posting.
package stayengine

import "strings"

// EventType is the closed set of domain event types the engine ingests (authoritative Protel map).
type EventType string

const (
	EvGuestIn     EventType = "GI" // guest in
	EvGuestChange EventType = "GC" // guest change
	EvGuestOut    EventType = "GO" // guest out
)

// InboxEvent is a parsed durable stay_events row (its typed JSON payload), the ONLY input the engine trusts.
type InboxEvent struct {
	EventIdentity string // external_event_identity — the idempotency key
	EventType     EventType
	Reservation   string // G# (interface-scoped; never globally unique)
	Room          string // RN (interface-scoped; never globally unique, changes on a Room Move)
	LastName      string
	FirstName     string
	Folio         string
	ArrivalRaw    string
	DepartureRaw  string
	// Sharers is the full occupancy list when the connector reports one. Sharing a Stay is legal and ordinary;
	// exactly one occupant is the primary.
	Sharers []Sharer
}

// StayView is the CURRENT persisted state of the Stay for this reservation within one interface (nil if none).
// AppliedIdentity reports whether a given event identity has ALREADY been applied to this Stay (idempotency).
type StayView struct {
	ID               string
	Status           string // RESERVED | IN_HOUSE | CHECKED_OUT | POST_STAY_ACTIVE | CANCELLED | NO_SHOW
	LifecycleVersion int
	Room             string
	AppliedIdentity  func(identity string) bool
}

// Op is the resolved, authoritative Stay operation for one event.
type Op string

const (
	OpCreateStay    Op = "CREATE_STAY"    // new Stay (new reservation), IN_HOUSE
	OpUpdateStay    Op = "UPDATE_STAY"    // in-place attribute/name/date correction on the same episode
	OpRoomMove      Op = "ROOM_MOVE"      // same Stay, new room (RN changed) — episode preserved
	OpCheckout      Op = "CHECKOUT"       // IN_HOUSE → CHECKED_OUT
	OpReinstate     Op = "REINSTATE"      // CHECKED_OUT → IN_HOUSE, lifecycle_version += 1 (new episode)
	OpSkipDuplicate Op = "SKIP_DUPLICATE" // idempotent no-op (already applied / already in target state)
	OpManualReview  Op = "MANUAL_REVIEW"  // ambiguous / unsafe to apply automatically → human review
)

// Decision is the engine's resolved outcome for one event. It is DETERMINISTIC in (event, current state).
type Decision struct {
	Op         Op
	NewStatus  string // target Stay status when the op changes it ("" = unchanged)
	Reinstate  bool   // true ⇒ lifecycle_version must increment by exactly 1 (CHECKED_OUT→IN_HOUSE)
	RoomMove   bool   // true ⇒ the normalized room changes on the same episode
	ReviewCode string // bounded machine code for MANUAL_REVIEW (^[A-Z][A-Z0-9_]{0,63}$)
}

// Resolve deterministically maps (event, current Stay state) to an authoritative operation. It NEVER decides
// from the connector hint; it uses only the durable event + the persisted Stay. Idempotency is first: an
// event identity already applied to the Stay is SKIP_DUPLICATE. Unknown/ambiguous transitions fail closed to
// MANUAL_REVIEW rather than guessing.
func Resolve(ev InboxEvent, cur *StayView) Decision {
	// idempotency: the same durable event applied twice is a deterministic no-op.
	if cur != nil && cur.AppliedIdentity != nil && cur.AppliedIdentity(ev.EventIdentity) {
		return Decision{Op: OpSkipDuplicate}
	}
	roomChanged := cur != nil && normRoom(cur.Room) != normRoom(ev.Room) && normRoom(ev.Room) != ""

	switch ev.EventType {
	case EvGuestIn:
		switch {
		case cur == nil:
			return Decision{Op: OpCreateStay, NewStatus: "IN_HOUSE"}
		case cur.Status == "CHECKED_OUT":
			// re-arrival on a departed reservation → a NEW episode (exactly one lifecycle_version bump).
			return Decision{Op: OpReinstate, NewStatus: "IN_HOUSE", Reinstate: true}
		case cur.Status == "IN_HOUSE":
			if roomChanged {
				return Decision{Op: OpRoomMove, RoomMove: true}
			}
			return Decision{Op: OpUpdateStay}
		case cur.Status == "RESERVED":
			return Decision{Op: OpUpdateStay, NewStatus: "IN_HOUSE"}
		default:
			return Decision{Op: OpManualReview, ReviewCode: "GI_ON_TERMINAL_STAY"}
		}

	case EvGuestChange:
		switch {
		case cur == nil:
			// a change for a Stay we have never seen is unsafe to fabricate.
			return Decision{Op: OpManualReview, ReviewCode: "GC_UNKNOWN_STAY"}
		case cur.Status == "IN_HOUSE":
			if roomChanged {
				return Decision{Op: OpRoomMove, RoomMove: true}
			}
			return Decision{Op: OpUpdateStay}
		case cur.Status == "RESERVED":
			return Decision{Op: OpUpdateStay}
		default:
			// a change to a checked-out / cancelled / no-show Stay is ambiguous → review.
			return Decision{Op: OpManualReview, ReviewCode: "GC_ON_TERMINAL_STAY"}
		}

	case EvGuestOut:
		switch {
		case cur == nil:
			return Decision{Op: OpManualReview, ReviewCode: "GO_UNKNOWN_STAY"}
		case cur.Status == "IN_HOUSE":
			return Decision{Op: OpCheckout, NewStatus: "CHECKED_OUT"}
		case cur.Status == "CHECKED_OUT":
			return Decision{Op: OpSkipDuplicate} // already departed — idempotent
		default:
			return Decision{Op: OpManualReview, ReviewCode: "GO_ON_NON_INHOUSE_STAY"}
		}

	default:
		return Decision{Op: OpManualReview, ReviewCode: "UNKNOWN_EVENT_TYPE"}
	}
}

func normRoom(s string) string { return strings.TrimSpace(s) }

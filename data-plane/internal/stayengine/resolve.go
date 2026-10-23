// Package stayengine resolves durable inbox events (iam_v2.stay_events) into authoritative Stay operations.
package stayengine

import "github.com/stayconnect/enterprise/data-plane/internal/namenorm"

// EventType is the closed set of Protel event types the engine ingests.
type EventType string

const (
	EvGuestIn     EventType = "GI" // guest in
	EvGuestChange EventType = "GC" // guest change
	EvGuestOut    EventType = "GO" // guest out
)

// InboxEvent is a parsed durable stay_events row, the only input the engine trusts.
type InboxEvent struct {
	EventIdentity string // external_event_identity, the idempotency key
	EventType     EventType
	Reservation   string // G#, interface-scoped
	Room          string // RN, interface-scoped; changes on a room move
	LastName      string
	FirstName     string
	Folio         string
	ArrivalRaw    string
	DepartureRaw  string
	// Sharers is the full occupancy list when reported; exactly one occupant is the primary.
	Sharers []Sharer
	// Guest attributes, nil when the record did not state them.
	TravelAgent *string
	VIP         *bool
}

// StayView is the persisted Stay for a reservation within one interface (nil if none).
// AppliedIdentity reports whether an event identity was already applied to it.
type StayView struct {
	ID               string
	Status           string // RESERVED | IN_HOUSE | CHECKED_OUT | POST_STAY_ACTIVE | CANCELLED | NO_SHOW
	LifecycleVersion int
	Room             string
	AppliedIdentity  func(identity string) bool
}

// Op is the resolved Stay operation for one event.
type Op string

const (
	OpCreateStay    Op = "CREATE_STAY"
	OpUpdateStay    Op = "UPDATE_STAY" // in-place correction on the same episode
	OpRoomMove      Op = "ROOM_MOVE"   // same Stay, new room
	OpCheckout      Op = "CHECKOUT"
	OpReinstate     Op = "REINSTATE" // CHECKED_OUT to IN_HOUSE, new episode
	OpSkipDuplicate Op = "SKIP_DUPLICATE"
	OpManualReview  Op = "MANUAL_REVIEW"
)

// Decision is the engine's outcome for one event, deterministic in (event, current state).
type Decision struct {
	Op         Op
	NewStatus  string // target Stay status when the op changes it ("" = unchanged)
	Reinstate  bool   // lifecycle_version must increment by exactly 1
	RoomMove   bool
	ReviewCode string // bounded machine code (^[A-Z][A-Z0-9_]{0,63}$)
}

// Resolve maps (event, current Stay state) to an operation using only the durable event and the persisted
// Stay. Unknown or ambiguous transitions fail closed to MANUAL_REVIEW.
func Resolve(ev InboxEvent, cur *StayView) Decision {
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
			return Decision{Op: OpManualReview, ReviewCode: "GC_UNKNOWN_STAY"}
		case cur.Status == "IN_HOUSE":
			if roomChanged {
				return Decision{Op: OpRoomMove, RoomMove: true}
			}
			return Decision{Op: OpUpdateStay}
		case cur.Status == "RESERVED":
			return Decision{Op: OpUpdateStay}
		default:
			return Decision{Op: OpManualReview, ReviewCode: "GC_ON_TERMINAL_STAY"}
		}

	case EvGuestOut:
		switch {
		case cur == nil:
			return Decision{Op: OpManualReview, ReviewCode: "GO_UNKNOWN_STAY"}
		case cur.Status == "IN_HOUSE":
			return Decision{Op: OpCheckout, NewStatus: "CHECKED_OUT"}
		case cur.Status == "CHECKED_OUT":
			return Decision{Op: OpSkipDuplicate}
		default:
			return Decision{Op: OpManualReview, ReviewCode: "GO_ON_NON_INHOUSE_STAY"}
		}

	default:
		return Decision{Op: OpManualReview, ReviewCode: "UNKNOWN_EVENT_TYPE"}
	}
}

// normRoom must match the authentication query, which upper-cases the typed room; alphanumeric rooms
// like "a12" would otherwise never match.
func normRoom(s string) string { return namenorm.Room(s) }

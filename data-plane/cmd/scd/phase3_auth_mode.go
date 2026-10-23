package main

import (
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// The configured room sign-in mode (tenants.auth_methods.pms.mode) is enforced here, not only by the portal:
//
//	room_any          any one of first name, surname or reservation number (single `verification` value
//	                  is compared against all three)
//	room_lastname     ONLY last_name
//	room_firstname    ONLY first_name
//	room_reservation  ONLY reservation_number
//	either / unset    last_name OR reservation_number, exactly one; unrecognised stored values read the same
//
// A refused shape is refused, not trimmed: dropping the extra field would make "which field did you send"
// something a caller can probe.
const (
	roomModeAny         = "room_any"
	roomModeLastName    = "room_lastname"
	roomModeFirstName   = "room_firstname"
	roomModeReservation = "room_reservation"
	roomModeEither      = "either"
)

// roomSignInMode is the stored mode, normalised the way the portal reads it.
func roomSignInMode(cfg *tenantcfg.AuthMethods) string {
	if cfg == nil || cfg.PMS == nil {
		return roomModeEither
	}
	switch m := strings.TrimSpace(cfg.PMS.Mode); m {
	case roomModeAny, roomModeLastName, roomModeFirstName, roomModeReservation:
		return m
	default:
		return roomModeEither
	}
}

// roomEvidence is the identifier evidence one resolve request carries, after the site's mode is applied.
type roomEvidence struct {
	Last, First, Res string
	// Violation is a request that carries an identifier field the site's mode does not accept. Its fields
	// are emptied: nothing it carried is compared against anything.
	Violation bool
}

// applyRoomSignInMode decides only which fields may be compared; blank-field completeness stays with the handler.
// Name fields arrive normalised, the reservation trimmed, verification raw.
func applyRoomSignInMode(mode, verification, last, first, res string) roomEvidence {
	has := func(s string) bool { return strings.TrimSpace(s) != "" }
	v, l, f, r := has(verification), has(last), has(first), has(res)
	switch mode {
	case roomModeAny:
		l2, f2, r2 := combineVerification(verification, last, first, res)
		return roomEvidence{Last: l2, First: f2, Res: r2}
	case roomModeLastName:
		if v || f || r {
			return roomEvidence{Violation: true}
		}
		return roomEvidence{Last: last}
	case roomModeFirstName:
		if v || l || r {
			return roomEvidence{Violation: true}
		}
		return roomEvidence{First: first}
	case roomModeReservation:
		if v || l || f {
			return roomEvidence{Violation: true}
		}
		return roomEvidence{Res: res}
	default: // legacy either
		if v || f || (l && r) {
			return roomEvidence{Violation: true}
		}
		return roomEvidence{Last: last, Res: res}
	}
}

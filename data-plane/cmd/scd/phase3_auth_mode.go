package main

import (
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// THE CONFIGURED ROOM SIGN-IN MODE IS ENFORCED HERE, NOT ONLY DRAWN BY THE PORTAL.
//
// Hotel Admin's Room sign-in page stores which detail a guest types beside the room number
// (tenants.auth_methods.pms.mode). The portal used it to decide which field the typed value goes in, and scd
// compared whatever fields arrived. So on a site configured for "room number + surname" a client that posted
// reservation_number directly -- not the portal, anything that can reach portald -- was compared on the
// reservation number and admitted. The contract (docs/user-guide/hotel-admin-config-manual.md, Room sign-in:
// "only that detail is accepted") was a portal convention, not a rule.
//
// The rule, per mode:
//
//	room_any          room number + any one of first name, surname or reservation number. Unchanged: the
//	                  single `verification` value is compared against all three (combineVerification), and
//	                  explicit fields are compared as sent.
//	room_lastname     ONLY last_name. Any other identifier field in the request refuses it.
//	room_firstname    ONLY first_name.
//	room_reservation  ONLY reservation_number.
//	either / unset    the legacy setting, read as the portal reads it: last_name OR reservation_number, exactly
//	                  one of them. The portal decides which by the value's shape and sends one; a request that
//	                  carries both, a first name or a combined `verification` value is not what that setting
//	                  accepts. An unrecognised stored value is read the same way, because that is what the
//	                  portal draws for it (PMSPromptKeys[mode] || either).
//
// A REFUSED SHAPE IS REFUSED, NOT TRIMMED. Dropping the extra field and comparing the allowed one would still
// answer a request no portal built, and would make "which field did you send" part of what a caller can probe.
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

// applyRoomSignInMode applies mode to the request's fields. The name fields arrive already normalised and the
// reservation number trimmed (as resolveHandler prepares them); verification is the raw room_any value.
//
// It decides only WHICH fields may be compared. Whether the evidence is complete (a blank allowed field) is
// still the handler's incomplete-evidence refusal, so a guest who left the box empty keeps the answer they had.
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

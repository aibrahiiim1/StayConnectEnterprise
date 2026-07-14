package pms

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ----------------------------------------------------------------------------
// Tier-1: per-kind field-map defaults. The loader merges these with the
// tenant-supplied overrides (Tier-2) before handing the result to the
// provider via Configure.
// ----------------------------------------------------------------------------

// Canonical field names every provider should be able to populate. Tier-2
// overrides use these as the LHS keys.
const (
	FRoomNumber        = "room_number"
	FFirstName         = "first_name"
	FLastName          = "last_name"
	FReservationNumber = "reservation_number"
	FCheckIn           = "check_in"
	FCheckOut          = "check_out"
	FGuestEmail        = "guest_email"
	FGuestDisplayName  = "guest_display_name"
)

// DefaultFIASFieldMap maps canonical names to FIAS Field-IDs (Appendix C).
// Used by Protel/Opera/Suite8/Fidelio when no per-tenant overrides exist.
func DefaultFIASFieldMap() FieldMap {
	return FieldMap{
		FRoomNumber:        "RN", // Room number
		FReservationNumber: "G#", // Reservation number
		FLastName:          "GN", // Guest Name (configurable in PMS)
		FFirstName:         "GF", // Guest First Name
		FCheckIn:           "GA", // Guest Arrival Date
		FCheckOut:          "GD", // Guest Departure Date
	}
}

// DefaultRESTFieldMap is the placeholder for Mews/Apaleo etc. Real REST
// providers will override with provider-specific dotted paths in 4.5.x.
func DefaultRESTFieldMap() FieldMap {
	return FieldMap{
		FRoomNumber:        "room.number",
		FReservationNumber: "reservation.id",
		FLastName:          "guest.lastName",
		FFirstName:         "guest.firstName",
		FCheckIn:           "stay.startUtc",
		FCheckOut:          "stay.endUtc",
		FGuestEmail:        "guest.email",
	}
}

// MergeFieldMap returns defaults overlaid with non-empty overrides. The
// loader calls this before Configure so providers always receive a fully
// populated map and never have to default missing keys themselves.
func MergeFieldMap(defaults, override FieldMap) FieldMap {
	out := make(FieldMap, len(defaults)+len(override))
	for k, v := range defaults {
		out[k] = v
	}
	for k, v := range override {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// ----------------------------------------------------------------------------
// Normalization application
// ----------------------------------------------------------------------------

// commonTitles is a small set of name prefixes we strip when
// NameStripTitles is enabled. Lowercased + trailing dot removed for matching.
var commonTitles = map[string]struct{}{
	"mr": {}, "mrs": {}, "ms": {}, "miss": {}, "mx": {}, "dr": {}, "prof": {},
	"sir": {}, "madam": {}, "lord": {}, "lady": {},
}

// ApplyRoomFormat applies a Sprintf format ("%03d", "Suite-%s", ...) to a
// room number AFTER NormalizeRoom, when format is non-empty. Numeric formats
// (%d) only fire when the room is purely digits; otherwise the input is
// returned unchanged so alphanumeric room ids ("A12") pass through.
func ApplyRoomFormat(format, room string) string {
	if format == "" || room == "" {
		return room
	}
	// %d / %03d / %05d / %-d → numeric format, only valid for all-digit rooms.
	if strings.ContainsAny(format, "d") && allDigits(room) {
		n, err := strconv.Atoi(room)
		if err == nil {
			return fmt.Sprintf(format, n)
		}
	}
	if strings.Contains(format, "%s") {
		return fmt.Sprintf(format, room)
	}
	return room
}

// ApplyNameNormalization optionally strips common titles before the lower /
// diacritic-fold pass NormalizeName already does.
func ApplyNameNormalization(n Normalization, name string) string {
	if n.NameStripTitles {
		name = stripTitle(name)
	}
	return NormalizeName(name)
}

// ApplyReservationCase honors n.ReservationCase ("upper"|"lower"|"preserve").
// Default (empty) = upper, matching NormalizeReservation.
func ApplyReservationCase(n Normalization, res string) string {
	res = strings.TrimSpace(res)
	switch n.ReservationCase {
	case "lower":
		return strings.ToLower(res)
	case "preserve":
		return res
	default:
		return strings.ToUpper(res)
	}
}

func stripTitle(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	first := strings.ToLower(strings.TrimRight(parts[0], "."))
	if _, isTitle := commonTitles[first]; isTitle {
		return strings.Join(parts[1:], " ")
	}
	return name
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// ----------------------------------------------------------------------------
// Stay-policy application
// ----------------------------------------------------------------------------

// EffectiveWindow expands the PMS-reported [check_in, check_out) by the
// configured early/late grace minutes. Either bound can be zero (unknown);
// grace is only applied when the bound is non-zero.
func EffectiveWindow(p StayPolicy, checkIn, checkOut time.Time) (time.Time, time.Time) {
	in := checkIn
	if !in.IsZero() && p.EarlyCheckinMinutes > 0 {
		in = in.Add(-time.Duration(p.EarlyCheckinMinutes) * time.Minute)
	}
	out := checkOut
	if !out.IsZero() && p.LateCheckoutMinutes > 0 {
		out = out.Add(time.Duration(p.LateCheckoutMinutes) * time.Minute)
	}
	return in, out
}

// MinRemainingOK returns false when the stay is about to end too soon to
// hand out a meaningful session. Defaults to 60s when not configured.
func MinRemainingOK(p StayPolicy, checkOut time.Time, now time.Time) bool {
	if checkOut.IsZero() {
		return true
	}
	min := p.MinRemainingSeconds
	if min <= 0 {
		min = 60
	}
	return checkOut.Sub(now) >= time.Duration(min)*time.Second
}

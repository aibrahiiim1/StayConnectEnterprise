package pms

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Tier-1 per-kind field-map defaults, merged with tenant overrides (Tier-2) before Configure.

// Canonical field names; Tier-2 overrides use them as the left-hand keys.
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

// DefaultRESTFieldMap is a placeholder; REST providers override with provider-specific dotted paths.
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

// MergeFieldMap: providers always receive a fully populated map.
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

// commonTitles are the prefixes stripped when NameStripTitles is on (lowercased, trailing dot removed).
var commonTitles = map[string]struct{}{
	"mr": {}, "mrs": {}, "ms": {}, "miss": {}, "mx": {}, "dr": {}, "prof": {},
	"sir": {}, "madam": {}, "lord": {}, "lady": {},
}

// ApplyRoomFormat applies a Sprintf format after NormalizeRoom; numeric formats fire only for all-digit rooms so ids
// like "A12" pass through.
func ApplyRoomFormat(format, room string) string {
	if format == "" || room == "" {
		return room
	}
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

func ApplyNameNormalization(n Normalization, name string) string {
	if n.NameStripTitles {
		name = stripTitle(name)
	}
	return NormalizeName(name)
}

// ReservationCase is upper|lower|preserve; empty means upper, matching NormalizeReservation.
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

// EffectiveWindow widens [check_in, check_out) by the grace minutes; grace applies only to non-zero (known) bounds.
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

// MinRemainingOK is false when the stay ends too soon for a meaningful session (default 60s).
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

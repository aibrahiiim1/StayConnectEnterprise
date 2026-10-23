package pmsd

import (
	"errors"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// ErrRecordMalformed drives continuity→GAP_DETECTED and sync→RESYNC_REQUIRED with zero admission; it never carries raw bytes.
var ErrRecordMalformed = errors.New("pmsd: malformed FIAS record")

// ErrIdentityAbsent: the record is well-formed but has no RN or G#. Do not treat it as malformed: a real
// Protel roster legitimately holds in-house rooms with no reservation number (house-use, out-of-order), and
// faulting each one livelocks the connector (fault, fresh DR, same records, fault) with zero admitted events.
// A duplicate identity field is still malformed. The caller skips an absent one; it never reaches the queue or inbox.
var ErrIdentityAbsent = errors.New("pmsd: record carries no mandatory Stay identity")

// identityAbsentError names the absent field code (RN, G#): protocol vocabulary, safe to log. No value is carried.
type identityAbsentError struct{ Missing string }

func (e identityAbsentError) Error() string {
	return "pmsd: missing mandatory identity field " + e.Missing
}
func (e identityAbsentError) Unwrap() error { return ErrIdentityAbsent }

// ParsedRecord's Fields keep source order, duplicates, empty values and unknown codes (fingerprint-only,
// never surfaced to the typed model).
type ParsedRecord struct {
	RecordType RecordType
	Fields     []FieldPair
}

// parseStrictRecord, unlike the permissive pms helpers, never skips malformed tokens: any grammar violation
// returns ErrRecordMalformed so the caller fails closed.
//
// Grammar (body = record-id tail):
//   - record-id  = two chars forming a supported RecordType.
//   - tail       = "" | "|" | "|" field ("|" field)* "|".
//   - field      = two-char code (graphic ASCII 0x21..0x7e) + value (no control byte; empty is legal).
//
// Internal empty or one-character segments and control bytes are rejected; duplicates and empty values are preserved.
func parseStrictRecord(body string) (ParsedRecord, error) {
	if len(body) < 2 {
		return ParsedRecord{}, ErrRecordMalformed
	}
	rt := RecordType(body[:2])
	if !rt.Valid() {
		return ParsedRecord{}, ErrRecordMalformed
	}
	rest := body[2:]
	if rest == "" {
		return ParsedRecord{RecordType: rt}, nil
	}
	if rest[0] != '|' {
		return ParsedRecord{}, ErrRecordMalformed
	}
	rest = rest[1:]
	if rest == "" {
		// "XX|": a valid control frame such as "LA|" or "DR|"
		return ParsedRecord{RecordType: rt}, nil
	}
	if rest[len(rest)-1] != '|' {
		return ParsedRecord{}, ErrRecordMalformed
	}
	rest = rest[:len(rest)-1]
	if rest == "" {
		return ParsedRecord{}, ErrRecordMalformed
	}
	segs := strings.Split(rest, "|")
	fields := make([]FieldPair, 0, len(segs))
	for _, s := range segs {
		if len(s) < 2 {
			return ParsedRecord{}, ErrRecordMalformed
		}
		code := s[:2]
		val := s[2:]
		if !validFieldCode(code) || hasControlBytes(val) {
			return ParsedRecord{}, ErrRecordMalformed
		}
		fields = append(fields, FieldPair{Code: code, Value: val})
	}
	return ParsedRecord{RecordType: rt, Fields: fields}, nil
}

// intendedDomain only picks the failure policy for an already-rejected frame, never dispatches an action. A
// malformed domain record (GI/GC/GO) is a recoverable gap/resync; a malformed control or unknown record ends ownership.
func intendedDomain(body string) bool {
	if len(body) < 2 {
		return false
	}
	return RecordType(body[:2]).IsDomain()
}

// validFieldCode requires graphic ASCII (0x21..0x7e), admitting '#' as in G# and V#.
func validFieldCode(code string) bool {
	if len(code) != 2 {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 0x21 || code[i] > 0x7e {
			return false
		}
	}
	return true
}

func (pr ParsedRecord) pairs() []FieldPair { return pr.Fields }

// reservationAbsent separates a departure Protel announced (live GO carries G#) from a room a roster snapshot
// merely mentions (RESYNC GO never carries G#, since an empty room has no booking to name).
func reservationAbsent(pr ParsedRecord) bool {
	for _, p := range pr.Fields {
		if p.Code == fcReservation && strings.TrimSpace(p.Value) != "" {
			return false
		}
	}
	return true
}

// Protel field codes: RN/G# are identity, the rest at-most-once evidence; unknown codes are fingerprint-only.
const (
	fcRoom        = "RN"
	fcReservation = "G#"
	fcLastName    = "GN"
	fcFirstName   = "GF"
	fcFolio       = "FO"
	fcArrival     = "GA"
	fcDeparture   = "GD"
	// Property-mapped: A0 travel agent (AG), A1 VIP flag (VP). Attributes, not identity; an unusable one is
	// dropped as not stated rather than faulting the feed.
	fcTravelAgent = "A0"
	fcVIP         = "A1"
)

// A longer attribute value is dropped as not stated, never truncated.
const (
	maxTravelAgentLen = 128
	maxVIPFlagLen     = 32
)

// vipFromFlag: empty or an explicit no is not VIP; any other value (yes, or a code such as V1) is VIP.
func vipFromFlag(v string) bool {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "", "0", "N", "NO", "F", "FALSE":
		return false
	}
	return true
}

type typedDomainFields struct {
	Reservation string
	Room        string
	LastName    string
	FirstName   string
	Folio       string
	Arrival     string
	Departure   string
	// nil = not stated (or unusable); never "false" or "".
	TravelAgent *string
	VIP         *bool
}

// extractTypedDomainFields fails closed on ambiguous duplicates: RN and G# exactly once, GN/GF/FO/GA/GD at
// most once, never resolved first-wins or last-wins. Unknown codes may repeat.
func extractTypedDomainFields(pr ParsedRecord) (typedDomainFields, error) {
	var f typedDomainFields
	counts := map[string]int{}
	attrs := map[string][]string{}
	for _, p := range pr.Fields {
		switch p.Code {
		case fcRoom:
			f.Room = p.Value
		case fcReservation:
			f.Reservation = p.Value
		case fcLastName:
			f.LastName = p.Value
		case fcFirstName:
			f.FirstName = p.Value
		case fcFolio:
			f.Folio = p.Value
		case fcArrival:
			f.Arrival = p.Value
		case fcDeparture:
			f.Departure = p.Value
		case fcTravelAgent, fcVIP:
			attrs[p.Code] = append(attrs[p.Code], p.Value)
			continue
		default:
			continue
		}
		counts[p.Code]++
	}
	// More than once is ambiguity (distrust the feed); zero is just an unkeyable Stay (see ErrIdentityAbsent).
	if counts[fcRoom] > 1 || counts[fcReservation] > 1 {
		return typedDomainFields{}, ErrRecordMalformed
	}
	// Room is always mandatory; reservation is mandatory except on a checkout. Protel sends GO with RN only
	// (a room has one current occupancy); requiring G# would discard real checkouts and leave departed Stays
	// IN_HOUSE and authenticating. The engine resolves a room-only GO and refuses to guess between sharers
	// (resolveCheckoutByRoom).
	if counts[fcRoom] == 0 {
		return typedDomainFields{}, identityAbsentError{Missing: fcRoom}
	}
	if counts[fcReservation] == 0 && pr.RecordType != RecGO {
		return typedDomainFields{}, identityAbsentError{Missing: fcReservation}
	}
	for _, code := range []string{fcLastName, fcFirstName, fcFolio, fcArrival, fcDeparture} {
		if counts[code] > 1 {
			return typedDomainFields{}, ErrRecordMalformed
		}
	}
	// Guest attributes are best-effort, not continuity: a duplicated, overlong or control-byte value is just not
	// stated. Faulting the feed over a marketing attribute would stop every guest's sign-in.
	if v := attrs[fcTravelAgent]; len(v) == 1 && len(v[0]) <= maxTravelAgentLen && !hasControlBytes(v[0]) {
		ta := strings.TrimSpace(v[0])
		f.TravelAgent = &ta
	}
	if v := attrs[fcVIP]; len(v) == 1 && len(v[0]) <= maxVIPFlagLen && !hasControlBytes(v[0]) {
		vip := vipFromFlag(v[0])
		f.VIP = &vip
	}
	return f, nil
}

// roomKey is the single room-identity contract; every room set in this package goes through it. Two keyings
// would count one room twice (e.g. "A12"), inflating rooms_named so a partial sweep looks complete, and
// completeness licenses closing a guest's stay.
func roomKey(s string) string { return pms.NormalizeRoom(s) }

// roomOf also covers records deliberately never admitted, so sweep coverage reflects what was observed.
func roomOf(pr ParsedRecord) string {
	for _, p := range pr.Fields {
		if p.Code == fcRoom {
			return roomKey(p.Value)
		}
	}
	return ""
}

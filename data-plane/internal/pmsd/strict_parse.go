package pmsd

import (
	"errors"
	"strings"
)

// ErrRecordMalformed is the single typed parse error for a FIAS record body that violates the strict grammar.
// The adapter treats it EXACTLY like a validation failure: continuity→GAP_DETECTED, sync→RESYNC_REQUIRED,
// zero event admission. It never carries the raw record bytes.
var ErrRecordMalformed = errors.New("pmsd: malformed FIAS record")

// ErrIdentityAbsent means the record is WELL-FORMED but carries no mandatory Stay identity (no RN, or no G#).
//
// THIS IS NOT THE SAME FAULT AS A MALFORMED RECORD, and conflating the two livelocked the connector against a
// real Protel roster. A DR resync of this property returns in-house rooms of which a minority carry no
// reservation number at all — house-use rooms, out-of-order rooms, occupancy the PMS tracks without a booking.
// The Phase-0 spike measured it directly: 365 in-house records, 355 of them carrying a G#.
//
// Treated as malformed, each of those ten drove a continuity fault and a fresh DR — whose response contained
// the same ten records, which drove another. The observed result on the live appliance was transport CONNECTED
// and healthy, a heartbeat, a complete DS…DE resync, and PERMANENTLY zero admitted events, cycling
// GAP_DETECTED → RESYNC_REQUIRED → IN_SYNC → GAP_DETECTED every few seconds. Nothing was corrupt; the feed was
// exactly what the property's PMS legitimately holds.
//
// So the two are separated. A DUPLICATE identity field is still malformed — it means the frame cannot be
// interpreted, and the safe response is to distrust the feed. An ABSENT one means this record cannot key a
// Stay and therefore is not admissible, but says nothing about the health of the feed carrying it. The
// caller skips it and continues; it never reaches the queue or the inbox either way.
var ErrIdentityAbsent = errors.New("pmsd: record carries no mandatory Stay identity")

// identityAbsentError names WHICH mandatory field was absent. The field CODE ("RN", "G#") is protocol
// vocabulary, not guest data, so it is safe to log — and it is the difference between "this PMS sends
// checkouts without a reservation number" and "this PMS sends records without a room", which are different
// problems with different answers. The value is never carried.
type identityAbsentError struct{ Missing string }

func (e identityAbsentError) Error() string {
	return "pmsd: missing mandatory identity field " + e.Missing
}
func (e identityAbsentError) Unwrap() error { return ErrIdentityAbsent }

// ParsedRecord is the ONE strict parsed representation of a FIAS record body used for pmsd ingestion. The
// Record ID is bound ONCE through RecordType (never re-emitted as a fake FieldPair). Fields preserves every
// valid field code/value in SOURCE ORDER, including DUPLICATE occurrences, present-but-empty values, and
// unknown-but-well-formed codes (the last are used for fingerprinting only, never surfaced to the typed
// domain model).
type ParsedRecord struct {
	RecordType RecordType
	Fields     []FieldPair
}

// parseStrictRecord parses a framed FIAS record body (STX/ETX already stripped by the framing layer) under a
// STRICT grammar. Unlike the permissive pms.ParseFieldPairs/ParseFields helpers it does not silently skip
// malformed tokens — any grammar violation returns ErrRecordMalformed so the caller can fail closed.
//
// Grammar (body = record-id tail):
//   - record-id  = exactly two chars forming a SUPPORTED RecordType (RecordType.Valid()).
//   - tail       = "" (record-id only) | "|" (record-id + delimiter, zero fields) | "|" field ("|" field)* "|".
//   - field      = two-char code + value; the code chars must be graphic ASCII (0x21..0x7e, so no space and
//     no control byte); the value may contain spaces but no control byte; a present-but-empty value is legal.
//
// Consequences: the normal final trailing "|" is accepted; an internal empty segment ("||") is rejected; a
// one-character segment is rejected; a malformed field id (space/control in the code) is rejected; control
// bytes anywhere are rejected. Duplicate occurrences and present-but-empty values are PRESERVED.
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
		// bare record id with no delimiter and no fields (e.g. a minimal control tick)
		return ParsedRecord{RecordType: rt}, nil
	}
	if rest[0] != '|' {
		// junk immediately after the record id (no field delimiter)
		return ParsedRecord{}, ErrRecordMalformed
	}
	rest = rest[1:] // drop the delimiter after the record id
	if rest == "" {
		// "XX|" — record id + delimiter, zero fields (valid control frame like "LA|", "DR|")
		return ParsedRecord{RecordType: rt}, nil
	}
	// a fielded tail must end with the normal trailing pipe
	if rest[len(rest)-1] != '|' {
		return ParsedRecord{}, ErrRecordMalformed
	}
	rest = rest[:len(rest)-1]
	if rest == "" {
		// "XX||" — an internal empty segment, not a field
		return ParsedRecord{}, ErrRecordMalformed
	}
	segs := strings.Split(rest, "|")
	fields := make([]FieldPair, 0, len(segs))
	for _, s := range segs {
		if len(s) < 2 {
			// empty ("||") or one-character segment
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

// intendedDomain reports whether a frame that FAILED strict parsing was an intended DOMAIN record (GI/GC/GO),
// used ONLY to pick the failure policy for an already-rejected frame — never to dispatch a valid action. A
// malformed domain record is a recoverable feed-quality fault (gap/resync, keep the link); anything else (a
// malformed control record or an unrecognized record id) is a protocol-level fault that ends ownership.
func intendedDomain(body string) bool {
	if len(body) < 2 {
		return false
	}
	return RecordType(body[:2]).IsDomain()
}

// validFieldCode reports whether a 2-char field code is well-formed: both bytes must be graphic ASCII
// (0x21..0x7e), which admits letters, digits and FIAS punctuation like '#' (G#, V#) while rejecting spaces
// and control bytes.
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

// pairs returns the parsed fields as the []FieldPair the complete-record fingerprint consumes (order,
// duplicates and present-but-empty preserved).
func (pr ParsedRecord) pairs() []FieldPair { return pr.Fields }

// domain typed field codes (authoritative Protel map). RN/G# are identity; the rest are at-most-once
// evidence. Any code outside this set is unknown → fingerprint-only, never surfaced to the typed model.
const (
	fcRoom        = "RN"
	fcReservation = "G#"
	fcLastName    = "GN"
	fcFirstName   = "GF"
	fcFolio       = "FO"
	fcArrival     = "GA"
	fcDeparture   = "GD"
)

// typedDomainFields is the at-most-once typed projection of a domain ParsedRecord.
type typedDomainFields struct {
	Reservation string
	Room        string
	LastName    string
	FirstName   string
	Folio       string
	Arrival     string
	Departure   string
}

// extractTypedDomainFields projects a strict domain ParsedRecord (GI/GC/GO) onto the typed model, FAILING
// CLOSED on ambiguous duplicates. RN and G# must each occur EXACTLY once; GN/GF/FO/GA/GD may occur AT MOST
// once. Duplicate identity/evidence fields are NOT resolved by first-wins or last-wins and are never
// collapsed into a map — they return ErrRecordMalformed so the caller drives a continuity fault and admits
// zero events. Unknown well-formed codes may repeat freely (they remain fingerprint-only).
func extractTypedDomainFields(pr ParsedRecord) (typedDomainFields, error) {
	var f typedDomainFields
	counts := map[string]int{}
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
		default:
			continue // unknown well-formed code: fingerprint-only, not counted for ambiguity
		}
		counts[p.Code]++
	}
	// RN and G# are mandatory identity → exactly once. More than once is ambiguity and the record cannot be
	// interpreted at all; zero times is a record that simply does not describe a keyable Stay. The first is a
	// reason to distrust the feed, the second is not — see ErrIdentityAbsent.
	if counts[fcRoom] > 1 || counts[fcReservation] > 1 {
		return typedDomainFields{}, ErrRecordMalformed
	}
	// ROOM IS ALWAYS MANDATORY. RESERVATION IS MANDATORY EXCEPT ON A CHECKOUT.
	//
	// This property's Protel sends every GO carrying RN only — 45 of 45 observed on the live link, with no G#
	// on any of them — which is a coherent thing for a PMS to say: "the guest in room N has left" identifies
	// the departure completely, because a room has at most one current occupancy. Requiring G# on a GO meant
	// every real checkout was discarded before it reached the engine, so a Stay that had genuinely departed
	// stayed IN_HOUSE forever and kept authenticating.
	//
	// The reservation is still required on GI and GC, where it is the key the Stay is created and updated
	// under, and where its absence really would mean an unusable record. Resolving a room-only GO to a Stay is
	// the engine's job and it refuses to guess between sharers; see resolveCheckoutByRoom.
	if counts[fcRoom] == 0 {
		return typedDomainFields{}, identityAbsentError{Missing: fcRoom}
	}
	if counts[fcReservation] == 0 && pr.RecordType != RecGO {
		return typedDomainFields{}, identityAbsentError{Missing: fcReservation}
	}
	// the remaining typed evidence fields are at-most-once
	for _, code := range []string{fcLastName, fcFirstName, fcFolio, fcArrival, fcDeparture} {
		if counts[code] > 1 {
			return typedDomainFields{}, ErrRecordMalformed
		}
	}
	return f, nil
}

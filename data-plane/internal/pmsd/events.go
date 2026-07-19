package pmsd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// RecordType is the CLOSED set of FIAS records pmsd recognizes, split into DOMAIN records (guest Stay
// mutations that flow to the Stay ingestion queue) and CONTROL observations (link/heartbeat/resync that
// only drive connector axes and are NEVER queued as guest events).
type RecordType string

const (
	// domain (guest Stay mutations → ingestion queue)
	RecGI RecordType = "GI" // guest in
	RecGC RecordType = "GC" // guest change
	RecGO RecordType = "GO" // guest out

	// control observations (connector axes / resync only). ONLY Phase-0-verified FIAS wire records appear
	// here — there is deliberately no "HB" or other speculative record; an unrecognized record id fails the
	// strict parser and terminates the ownership cycle rather than being treated as a verified heartbeat.
	RecLS RecordType = "LS" // link start
	RecLA RecordType = "LA" // link alive (heartbeat)
	RecLE RecordType = "LE" // link end
	RecDR RecordType = "DR" // resync request
	RecDS RecordType = "DS" // database resync start
	RecDE RecordType = "DE" // database resync end
)

var domainRecords = map[RecordType]struct{}{RecGI: {}, RecGC: {}, RecGO: {}}
var controlRecords = map[RecordType]struct{}{RecLS: {}, RecLA: {}, RecLE: {}, RecDR: {}, RecDS: {}, RecDE: {}}

func (r RecordType) IsDomain() bool  { _, ok := domainRecords[r]; return ok }
func (r RecordType) IsControl() bool { _, ok := controlRecords[r]; return ok }
func (r RecordType) Valid() bool     { return r.IsDomain() || r.IsControl() }

var (
	ErrEventInvalid = errors.New("pmsd: invalid protocol event")
)

// field bounds (defensive; the adapter parses from a framed record, but the queue re-validates)
const (
	maxRawTimestampLen = 32
	maxReservationLen  = 64
	maxRoomLen         = 16
	maxFolioLen        = 64
	maxGuestLen        = 128
	maxCursorLen       = 4096
	maxExtIdentityLen  = 128
	maxEvidenceHexLen  = 64 // sha256 hex
)

// hasControlBytes rejects STX/ETX and other control characters in a typed string field (they belong only
// inside the raw frame, which never reaches a typed Event).
func hasControlBytes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// Validate enforces the typed Event contract BEFORE it may consume queue capacity. Domain events require a
// verified external identity; control observations must not carry guest fields. All string fields are
// bounded and free of control bytes; identity fields must be canonical UUIDs; normalization version > 0.
func (e Event) Validate() error {
	if !e.RecordType.Valid() {
		return ErrEventInvalid
	}
	if _, err := parseUUID16(e.InterfaceID); err != nil {
		return ErrEventInvalid
	}
	if _, err := parseUUID16(e.RevisionID); err != nil {
		return ErrEventInvalid
	}
	if _, err := parseUUID16(e.SecretGenerationID); err != nil {
		return ErrEventInvalid
	}
	if e.NormalizationVer <= 0 {
		return ErrEventInvalid
	}
	// identity-critical + bounded fields — an overlong identity value is REJECTED (never truncated into a
	// possibly-colliding value); the caller treats a rejection as a continuity fault → resync.
	for _, f := range []struct {
		v string
		n int
	}{
		{e.PMSEventTimestampRaw, maxRawTimestampLen}, {e.ArrivalRaw, maxRawTimestampLen}, {e.DepartureRaw, maxRawTimestampLen},
		{e.ReservationRef, maxReservationLen}, {e.RoomNumber, maxRoomLen}, {e.FolioRef, maxFolioLen},
		{e.GuestLastName, maxGuestLen}, {e.GuestFirstName, maxGuestLen},
		{e.Cursor, maxCursorLen},
	} {
		if len(f.v) > f.n || hasControlBytes(f.v) {
			return ErrEventInvalid
		}
	}
	if e.NormalizedAt.IsZero() {
		return ErrEventInvalid
	}
	if e.RecordType.IsDomain() {
		// a guest Stay mutation must carry: a 64-hex keyed-HMAC source-event fingerprint (== external
		// identity) with a positive fingerprint-key version, a 64-hex keyed-HMAC source evidence with a
		// positive key version, and non-empty room/reservation. The connector NEVER carries an authoritative
		// Stay identity — StayResolutionCandidate is an optional non-authoritative hint (Increment 4 resolves
		// the Stay/lifecycle transactionally), so it is not validated as a mandatory identity here.
		if !isHex64(e.SourceEventFingerprint) || e.FingerprintKeyVersion <= 0 {
			return ErrEventInvalid
		}
		if e.ExternalEventIdentity != e.SourceEventFingerprint {
			return ErrEventInvalid
		}
		if !isHex64(e.SourceEvidenceHash) || e.EvidenceKeyVersion <= 0 {
			return ErrEventInvalid
		}
		if strings.TrimSpace(e.RoomNumber) == "" || strings.TrimSpace(e.ReservationRef) == "" {
			return ErrEventInvalid
		}
	} else {
		// control observations must NOT carry guest fields (they are not guest events)
		if e.ReservationRef != "" || e.RoomNumber != "" || e.FolioRef != "" || e.GuestLastName != "" || e.GuestFirstName != "" {
			return ErrEventInvalid
		}
	}
	return nil
}

// isHex64 reports whether s is exactly 64 lowercase/uppercase hex chars (a SHA-256 digest).
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32
}

// ComputeEvidenceHMAC derives the source-evidence digest with a KEYED HMAC-SHA256 (not a plain,
// dictionary-testable SHA), binding it to a key generation/version. The key is caller-owned and never
// stored, logged, or embedded in the Event — only the resulting hex digest + key version are.
func ComputeEvidenceHMAC(key []byte, keyVersion int, data string) (hexDigest string, version int) {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(data))
	return hex.EncodeToString(m.Sum(nil)), keyVersion
}

// FieldPair is one (code, value) occurrence exactly as parsed from a FIAS record — a 2-char field code and
// its (possibly empty) value. The list is ORDERED and keeps DUPLICATE occurrences so the fingerprint
// distinguishes "GNa|GNb" from "GNb|GNa" and from a single "GNa".
type FieldPair struct {
	Code  string
	Value string
}

// SourceEvent is the canonical representation of the COMPLETE parsed FIAS record used to derive the
// idempotency fingerprint. It carries every field code+value the source sent (in order, with duplicates and
// present-but-empty preserved), the record type, interface, normalization version, protocol profile, and a
// verified source sequence/timestamp when available. Unknown field codes are NOT surfaced to the typed
// domain Event, but they DO influence this fingerprint (so a change to any source token changes the HMAC).
// Only the STX/ETX framing bytes are excluded.
type SourceEvent struct {
	InterfaceID          string
	RecordType           RecordType
	NormalizationVersion int
	ProtocolProfile      string // e.g. "protel-fias/v1"
	Sequence             string // verified source sequence/cursor/timestamp when available, else ""
	Pairs                []FieldPair
}

func newSourceEvent(interfaceID string, rt RecordType, normVer int, profile, sequence string, pairs []FieldPair) SourceEvent {
	return SourceEvent{InterfaceID: interfaceID, RecordType: rt, NormalizationVersion: normVer,
		ProtocolProfile: profile, Sequence: sequence, Pairs: pairs}
}

// canonical produces a deterministic, boundary-safe encoding. Every token is length-prefixed so no value
// can impersonate a boundary; field pairs are encoded IN THE SOURCE ORDER (never sorted) so duplicate and
// reordered occurrences are distinguishable, and a present-but-empty value ("GN") is distinct from an
// absent field (no pair emitted for it).
func (se SourceEvent) canonical() string {
	var b strings.Builder
	enc := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
		b.WriteByte(0x1f)
	}
	enc("source-event:v2")
	enc(se.ProtocolProfile)
	enc(se.InterfaceID)
	enc(string(se.RecordType))
	enc("nv" + strconv.Itoa(se.NormalizationVersion))
	enc("seq" + se.Sequence)
	enc("n=" + strconv.Itoa(len(se.Pairs))) // pair count binds the boundary between header and body
	for _, p := range se.Pairs {
		enc("c=" + p.Code)
		enc("v=" + p.Value)
	}
	return b.String()
}

// ComputeSourceFingerprint derives the idempotency fingerprint with a DEDICATED keyed HMAC (purpose
// PMS_EVENT_IDENTITY). An exact retransmission yields the same fingerprint (idempotent); any meaningful
// normalized payload change yields a different one. An empty key fails closed (returns "", 0).
func ComputeSourceFingerprint(identityKey []byte, keyVersion int, se SourceEvent) (fingerprint string, version int) {
	if len(identityKey) == 0 || keyVersion <= 0 {
		return "", 0
	}
	m := hmac.New(sha256.New, identityKey)
	_, _ = m.Write([]byte("PMS_EVENT_IDENTITY\x00"))
	_, _ = m.Write([]byte(se.canonical()))
	return hex.EncodeToString(m.Sum(nil)), keyVersion
}

// DeriveStayResolutionCandidate is a NON-AUTHORITATIVE grouping hint only — never a Stay primary identity, a
// unique constraint, a lifecycle/episode decision, Folio/Room ownership, or sufficient to apply a Stay
// mutation. It is scoped by tenant/site/interface + reservation (G#) ONLY. Room (RN) is excluded (not
// globally unique; changes on a Room Move within one Stay), and arrival (GA) is excluded (it can be
// corrected within the same Stay). The Increment-4 Stay engine performs the authoritative, transactional
// resolution (existing Stay / Room Move / new lifecycle / Reinstatement / Manual Review); this value is
// only a starting point it may consult.
func DeriveStayResolutionCandidate(tenantID, siteID, interfaceID, reservation string) string {
	h := sha256.New()
	for _, part := range []string{"stay-resolution-candidate:v1", tenantID, siteID, interfaceID, reservation} {
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// nonZeroTime is a small helper for adapters that need a non-zero normalized timestamp.
func nonZeroTime(t time.Time, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

// loadLocation validates an IANA timezone name.
func loadLocation(tz string) (*time.Location, error) { return time.LoadLocation(tz) }

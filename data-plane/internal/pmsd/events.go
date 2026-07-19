package pmsd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
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

	// control observations (connector axes / resync only)
	RecLS RecordType = "LS" // link start ack
	RecLA RecordType = "LA" // link alive (heartbeat)
	RecDR RecordType = "DR" // resync request
	RecDS RecordType = "DS" // database resync start
	RecDE RecordType = "DE" // database resync end
	RecHB RecordType = "HB" // heartbeat/link tick
)

var domainRecords = map[RecordType]struct{}{RecGI: {}, RecGC: {}, RecGO: {}}
var controlRecords = map[RecordType]struct{}{RecLS: {}, RecLA: {}, RecDR: {}, RecDS: {}, RecDE: {}, RecHB: {}}

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
		{e.GuestLastName, maxGuestLen}, {e.GuestFirstName, maxGuestLen}, {e.GuestName, maxGuestLen},
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
		// positive key version, a logical-stay key, and non-empty room/reservation.
		if !isHex64(e.SourceEventFingerprint) || e.FingerprintKeyVersion <= 0 {
			return ErrEventInvalid
		}
		if e.ExternalEventIdentity != e.SourceEventFingerprint {
			return ErrEventInvalid
		}
		if !isHex64(e.SourceEvidenceHash) || e.EvidenceKeyVersion <= 0 {
			return ErrEventInvalid
		}
		if !isHex64(e.LogicalStayKey) {
			return ErrEventInvalid
		}
		if strings.TrimSpace(e.RoomNumber) == "" || strings.TrimSpace(e.ReservationRef) == "" {
			return ErrEventInvalid
		}
	} else {
		// control observations must NOT carry guest fields (they are not guest events)
		if e.ReservationRef != "" || e.RoomNumber != "" || e.FolioRef != "" || e.GuestName != "" {
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

// fieldVal captures a normalized field's value AND whether it was present in the source record — an empty
// present field is semantically distinct from an absent one.
type fieldVal struct {
	value   string
	present bool
}

// SourceEvent is the canonical, versioned representation of a single PMS source event used to derive the
// idempotency fingerprint. It contains EVERY verified normalized field the record represents (not just
// reservation/room/dates) plus presence/absence, the normalization version, the source-protocol profile,
// and any authoritative sequence/cursor when actually available.
type SourceEvent struct {
	InterfaceID          string
	RecordType           RecordType
	NormalizationVersion int
	ProtocolProfile      string // e.g. "protel-fias/v1"
	Sequence             string // verified source sequence/cursor when available, else ""
	fields               map[string]fieldVal
}

func newSourceEvent(interfaceID string, rt RecordType, normVer int, profile, sequence string) SourceEvent {
	return SourceEvent{InterfaceID: interfaceID, RecordType: rt, NormalizationVersion: normVer,
		ProtocolProfile: profile, Sequence: sequence, fields: map[string]fieldVal{}}
}

// set records a normalized field: (value, present). present=false means the source omitted the field.
func (se SourceEvent) set(name, value string, present bool) {
	se.fields[name] = fieldVal{value, present}
}

// canonical produces a deterministic, boundary-safe encoding. Fields are sorted; present-but-empty is
// distinguished from absent; unit separators prevent field-boundary shifting.
func (se SourceEvent) canonical() string {
	var b strings.Builder
	enc := func(s string) {
		// length-prefix each token so no value can impersonate a boundary
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
		b.WriteByte(0x1f)
	}
	enc("source-event:v1")
	enc(se.ProtocolProfile)
	enc(se.InterfaceID)
	enc(string(se.RecordType))
	enc("nv" + strconv.Itoa(se.NormalizationVersion))
	enc("seq" + se.Sequence)
	names := make([]string, 0, len(se.fields))
	for n := range se.fields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fv := se.fields[n]
		if fv.present {
			enc("F+" + n + "=" + fv.value)
		} else {
			enc("F-" + n) // absent marker (distinct from present-but-empty "F+name=")
		}
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

// DeriveLogicalStayKey is the SEPARATE Stay-resolution identity (never the Event idempotency key). It is
// scoped by tenant/site/interface + reservation (G#) + lifecycle evidence; room (RN) is evidence only and
// never part of the identity (it is not globally unique and changes on a Room Move within one Stay).
func DeriveLogicalStayKey(tenantID, siteID, interfaceID, reservation, lifecycleEvidence string) string {
	h := sha256.New()
	for _, part := range []string{"logical-stay:v1", tenantID, siteID, interfaceID, reservation, lifecycleEvidence} {
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// deriveDisplayName builds a deterministic "Last, First" display name from typed components.
func deriveDisplayName(last, first string) string {
	last = strings.TrimSpace(last)
	first = strings.TrimSpace(first)
	switch {
	case last == "" && first == "":
		return ""
	case first == "":
		return last
	case last == "":
		return first
	default:
		return last + ", " + first
	}
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

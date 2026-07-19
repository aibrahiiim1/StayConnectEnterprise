package pmsd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	for _, f := range []struct {
		v string
		n int
	}{
		{e.PMSTimestampRaw, maxRawTimestampLen}, {e.ReservationRef, maxReservationLen},
		{e.RoomNumber, maxRoomLen}, {e.FolioRef, maxFolioLen}, {e.GuestName, maxGuestLen},
		{e.Cursor, maxCursorLen}, {e.ExternalEventIdentity, maxExtIdentityLen},
	} {
		if len(f.v) > f.n || hasControlBytes(f.v) {
			return ErrEventInvalid
		}
	}
	if len(e.SourceEvidenceHash) > maxEvidenceHexLen {
		return ErrEventInvalid
	}
	if e.SourceEvidenceHash != "" {
		if _, err := hex.DecodeString(e.SourceEvidenceHash); err != nil || e.EvidenceKeyVersion <= 0 {
			return ErrEventInvalid
		}
	}
	if e.NormalizedAt.IsZero() {
		return ErrEventInvalid
	}
	if e.RecordType.IsDomain() {
		// a guest Stay mutation must carry a verified external identity
		if strings.TrimSpace(e.ExternalEventIdentity) == "" {
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

// ComputeEvidenceHMAC derives the source-evidence digest with a KEYED HMAC-SHA256 (not a plain,
// dictionary-testable SHA), binding it to a key generation/version. The key is caller-owned and never
// stored, logged, or embedded in the Event — only the resulting hex digest + key version are.
func ComputeEvidenceHMAC(key []byte, keyVersion int, data string) (hexDigest string, version int) {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(data))
	return hex.EncodeToString(m.Sum(nil)), keyVersion
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

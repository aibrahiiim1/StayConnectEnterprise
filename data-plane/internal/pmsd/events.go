package pmsd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// RecordType is the closed set of FIAS records: domain records (guest Stay mutations, queued) and control
// observations (link/heartbeat/resync, never queued as guest events).
type RecordType string

const (
	RecGI RecordType = "GI" // guest in
	RecGC RecordType = "GC" // guest change
	RecGO RecordType = "GO" // guest out

	// Only verified FIAS wire records appear here: an unrecognized record id fails the strict parser and ends the
	// ownership cycle rather than counting as a heartbeat.
	RecLS RecordType = "LS" // link start
	RecLA RecordType = "LA" // link alive (heartbeat)
	RecLE RecordType = "LE" // link end
	RecDR RecordType = "DR" // resync request
	RecDS RecordType = "DS" // database resync start
	RecDE RecordType = "DE" // database resync end

	// A PA is read only to hand back verbatim to the waiting financial path (financial_relay.go); pmsd never interprets or sends one.
	RecPA RecordType = "PA" // posting answer
)

var domainRecords = map[RecordType]struct{}{RecGI: {}, RecGC: {}, RecGO: {}}
var controlRecords = map[RecordType]struct{}{RecLS: {}, RecLA: {}, RecLE: {}, RecDR: {}, RecDS: {}, RecDE: {}}

func (r RecordType) IsDomain() bool  { _, ok := domainRecords[r]; return ok }
func (r RecordType) IsControl() bool { _, ok := controlRecords[r]; return ok }
func (r RecordType) Valid() bool     { return r.IsDomain() || r.IsControl() || r == RecPA }

var (
	ErrEventInvalid = errors.New("pmsd: invalid protocol event")
)

const (
	maxRawTimestampLen = 32
	maxReservationLen  = 64
	maxRoomLen         = 16
	maxFolioLen        = 64
	maxGuestLen        = 128
	maxCursorLen       = 4096
	maxExtIdentityLen  = 128
	maxEvidenceHexLen  = 64 // sha256 hex
	maxSharers         = 32
)

// EventSharer matches the JSON shape stayengine.Sharer reads.
type EventSharer struct {
	ExternalGuestID string `json:"external_guest_id"`
	FirstName       string `json:"first_name"`
	LastName        string `json:"last_name"`
	IsPrimary       bool   `json:"is_primary"`
}

// hasControlBytes rejects STX/ETX and other control characters, which belong only in the raw frame.
func hasControlBytes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// Validate enforces the typed Event contract before it may consume queue capacity.
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
	// SecretGenerationID is optional: a credential_mode=NONE revision has no secret generation, and requiring one
	// rejects every record. When pinned it must still be a canonical UUID.
	if e.SecretGenerationID != "" {
		if _, err := parseUUID16(e.SecretGenerationID); err != nil {
			return ErrEventInvalid
		}
	}
	if e.NormalizationVer <= 0 {
		return ErrEventInvalid
	}
	// An overlong identity value is rejected, never truncated into a possibly-colliding value; callers treat it as a continuity fault.
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
	if e.TravelAgent != nil && (len(*e.TravelAgent) > maxTravelAgentLen || hasControlBytes(*e.TravelAgent)) {
		return ErrEventInvalid
	}
	if e.NormalizedAt.IsZero() {
		return ErrEventInvalid
	}
	if len(e.Sharers) > maxSharers {
		return ErrEventInvalid
	}
	for _, sh := range e.Sharers {
		for _, v := range []string{sh.ExternalGuestID, sh.FirstName, sh.LastName} {
			if len(v) > maxGuestLen || hasControlBytes(v) {
				return ErrEventInvalid
			}
		}
	}
	if e.RecordType.IsDomain() {
		// StayResolutionCandidate is an optional hint; the connector never carries an authoritative Stay identity.
		if !isHex64(e.SourceEventFingerprint) || e.FingerprintKeyVersion <= 0 {
			return ErrEventInvalid
		}
		if e.ExternalEventIdentity != e.SourceEventFingerprint {
			return ErrEventInvalid
		}
		if !isHex64(e.SourceEvidenceHash) || e.EvidenceKeyVersion <= 0 {
			return ErrEventInvalid
		}
		// A checkout (GO) may name the room alone; the engine resolves which Stay and refuses when it is not unique.
		if strings.TrimSpace(e.RoomNumber) == "" {
			return ErrEventInvalid
		}
		if e.RecordType != RecGO && strings.TrimSpace(e.ReservationRef) == "" {
			return ErrEventInvalid
		}
	} else {
		if e.ReservationRef != "" || e.RoomNumber != "" || e.FolioRef != "" || e.GuestLastName != "" || e.GuestFirstName != "" ||
			e.TravelAgent != nil || e.VIP != nil {
			return ErrEventInvalid
		}
	}
	return nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32
}

// ComputeEvidenceHMAC is keyed (not a plain, dictionary-testable SHA); the key is never stored, logged or embedded in the Event.
func ComputeEvidenceHMAC(key []byte, keyVersion int, data string) (hexDigest string, version int) {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(data))
	return hex.EncodeToString(m.Sum(nil)), keyVersion
}

// FieldPair is one field occurrence as parsed; lists keep order and duplicates so the fingerprint distinguishes "GNa|GNb" from "GNb|GNa".
type FieldPair struct {
	Code  string
	Value string
}

// SourceEvent is the complete parsed FIAS record the idempotency fingerprint derives from. Unknown field codes
// are not surfaced on the typed Event but still change the fingerprint; only STX/ETX framing is excluded.
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

// canonical is a deterministic, boundary-safe encoding: tokens are length-prefixed, pairs keep source order (never
// sorted), and a present-but-empty value is distinct from an absent field.
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

// ComputeSourceFingerprint is a keyed HMAC (purpose PMS_EVENT_IDENTITY); an empty key fails closed (returns "", 0).
func ComputeSourceFingerprint(identityKey []byte, keyVersion int, se SourceEvent) (fingerprint string, version int) {
	if len(identityKey) == 0 || keyVersion <= 0 {
		return "", 0
	}
	m := hmac.New(sha256.New, identityKey)
	_, _ = m.Write([]byte("PMS_EVENT_IDENTITY\x00"))
	_, _ = m.Write([]byte(se.canonical()))
	return hex.EncodeToString(m.Sum(nil)), keyVersion
}

// DeriveStayResolutionCandidate is a non-authoritative grouping hint, never a Stay identity or sufficient to apply
// a mutation. It is keyed by reservation only: room changes on a Room Move and arrival can be corrected.
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

// eventPayloadJSON builds the typed payload for a durable inbox row: never the raw frame or a secret.
// On a marshal error it falls back to an empty object rather than leaking anything.
func eventPayloadJSON(ev Event) []byte {
	p := struct {
		Reservation string        `json:"reservation"`
		Room        string        `json:"room"`
		LastName    string        `json:"last_name"`
		FirstName   string        `json:"first_name"`
		Folio       string        `json:"folio,omitempty"`
		ArrivalRaw  string        `json:"arrival_raw,omitempty"`
		Departure   string        `json:"departure_raw,omitempty"`
		Candidate   string        `json:"stay_resolution_candidate,omitempty"`
		Sharers     []EventSharer `json:"sharers,omitempty"`
		// Pointers so an absent value is distinct from a stated "" (no travel agent).
		TravelAgent *string `json:"travel_agent,omitempty"`
		VIP         *bool   `json:"vip,omitempty"`
	}{
		Reservation: ev.ReservationRef, Room: ev.RoomNumber,
		LastName: ev.GuestLastName, FirstName: ev.GuestFirstName,
		Folio: ev.FolioRef, ArrivalRaw: ev.ArrivalRaw, Departure: ev.DepartureRaw,
		Candidate:   ev.StayResolutionCandidate,
		Sharers:     ev.Sharers,
		TravelAgent: ev.TravelAgent, VIP: ev.VIP,
	}
	b, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func nonZeroTime(t time.Time, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

func loadLocation(tz string) (*time.Location, error) { return time.LoadLocation(tz) }

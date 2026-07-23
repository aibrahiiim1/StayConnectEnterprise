// Package pms abstracts hotel Property-Management-System integrations used
// for guest auth (room number + verification data).
//
// The validation rule is: room_number + ONE OF (first_name | last_name |
// reservation_number) must match. Modes:
//
//   - ModeRoomLastName    : exactly the last name
//   - ModeRoomFirstName   : exactly the first name
//   - ModeRoomReservation : exactly the reservation number
//   - ModeEither          : ANY of the three matches → pass
//
// The portal sends ALL filled secondary fields and the configured mode; the
// provider does the matching. This keeps tenant-policy decisions out of the
// portal.
//
// Phase 4.5 ships Stub + ProtelFIAS. Opera/Fidelio (also FIAS-family),
// Mews and Apaleo (REST) plug in by implementing the same Provider iface.
package pms

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
)

type Mode string

const (
	ModeRoomLastName    Mode = "room_lastname"
	ModeRoomFirstName   Mode = "room_firstname"
	ModeRoomReservation Mode = "room_reservation"
	ModeEither          Mode = "either"
)

func ValidMode(m string) bool {
	switch Mode(m) {
	case ModeRoomLastName, ModeRoomFirstName, ModeRoomReservation, ModeEither:
		return true
	}
	return false
}

// Query is what the portal/scd hands the provider. Strings are normalized
// (trimmed) by the caller; the provider performs case-insensitive name
// matches as appropriate.
type Query struct {
	RoomNumber        string
	FirstName         string
	LastName          string
	ReservationNumber string
	Mode              Mode
}

// Result is what the provider returns when validation succeeds.
type Result struct {
	Valid         bool
	GuestName     string // formatted display name (PMS-decided)
	FirstName     string
	LastName      string
	CheckIn       time.Time // local PMS time, zero if unknown
	CheckOut      time.Time
	RoomNumber    string // canonical (provider may normalize "0103" → "103")
	ReservationID string
	Email         string // optional, when PMS exposes it
}

type Provider interface {
	// Name is the per-tenant unique identifier of this configured instance
	// (matches pms_providers.name); registries are keyed on this.
	Name() string
	// Kind is the provider family (matches pms_providers.kind), used by the
	// loader to route construction.
	Kind() string
	// Configure (re)applies a config bundle. Safe to call multiple times;
	// the loader calls it after construction and on subsequent reloads.
	Configure(cfg ProviderConfig) error
	// Config returns the last-applied bundle so scd can read normalization
	// and stay-policy without re-fetching the DB row.
	Config() ProviderConfig
	// Health returns the current connection / cache snapshot.
	Health() Health
	// ValidateGuest performs the per-guest check.
	ValidateGuest(ctx context.Context, q Query) (*Result, error)
}

// Starter is implemented by providers that need a background goroutine
// (e.g. ProtelFIAS keeps a TCP link alive). The loader calls Start after
// Configure. Stub providers implement only Provider.
type Starter interface {
	Start(ctx context.Context)
}

// Tester is implemented by providers that can be probed for connectivity
// without committing real data. The admin endpoint /v1/admin/pms/{name}/test
// dispatches here.
type Tester interface {
	TestConnection(ctx context.Context) error
}

// Cacher exposes the in-memory lookup cache for admin debugging. Returns at
// most `limit` entries; pass 0 for the implementation default.
type Cacher interface {
	CacheSnapshot(limit int) []Reservation
}

// Stopper is implemented by providers that own background resources
// (e.g. a persistent TCP link). scd calls Stop during a live reload so the
// old goroutines exit before the new provider instances take over.
type Stopper interface {
	Stop()
}

// ---- Tier-2 config bundle (one row in pms_providers) ----------------------

// ProviderConfig is the per-tenant config record handed to Configure.
// Fields irrelevant to a given Kind should be zero-valued.
type ProviderConfig struct {
	Name          string
	Kind          string
	Connection    ConnectionConfig
	FieldMap      FieldMap
	Normalization Normalization
	StayPolicy    StayPolicy
}

type ConnectionConfig struct {
	Host       string
	Port       int
	UseTLS     bool
	AuthKey    string         // FIAS IfcAuthKey
	BaseURL    string         // REST family (Mews/Apaleo)
	APIKey     string         // REST
	PropertyID string         // Mews property / Apaleo accountCode
	Extra      map[string]any // escape hatch for kind-specific knobs
}

// FieldMap routes our canonical field names to PMS-specific paths/codes.
// Empty string = "use the provider's built-in default".
//
// Canonical keys we recognize:
//
//	"room_number", "first_name", "last_name", "reservation_number",
//	"check_in", "check_out", "guest_email", "guest_display_name"
type FieldMap map[string]string

// Normalization is per-tenant input shaping applied before matching.
type Normalization struct {
	RoomFormat      string `json:"room_format"`       // Sprintf format e.g. "%03d"; applied after NormalizeRoom
	NameStripTitles bool   `json:"name_strip_titles"` // strip Mr./Mrs./Dr. prefixes from incoming names
	ReservationCase string `json:"reservation_case"`  // "upper" | "lower" | "preserve" (default upper)
}

// StayPolicy bounds the access window relative to the PMS-reported stay.
type StayPolicy struct {
	EarlyCheckinMinutes int `json:"early_checkin_minutes"` // allow this many minutes before check_in
	LateCheckoutMinutes int `json:"late_checkout_minutes"` // allow this many minutes after check_out
	MinRemainingSeconds int `json:"min_remaining_seconds"` // refuse if less than this remains in the stay
}

// Health is the per-provider observability snapshot. Phase 4.5.5b syncs to DB.
type Health struct {
	Status         string    `json:"status"`                    // idle | connecting | connected | degraded | down
	ConnectedSince time.Time `json:"connected_since,omitempty"` // zero when not connected
	LastRecordAt   time.Time `json:"last_record_at,omitempty"`  // last GI/GC/GO arrived (FIAS); zero for stub
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitempty"`
	CacheSize      int       `json:"cache_size"` // entries currently in the lookup cache
}

var (
	ErrNotFound     = errors.New("pms: no matching reservation")
	ErrUpstreamFail = errors.New("pms: upstream PMS unreachable")
	ErrCheckedOut   = errors.New("pms: reservation outside stay window")
)

type Registry struct{ providers map[string]Provider }

func NewRegistry() *Registry            { return &Registry{providers: map[string]Provider{}} }
func (r *Registry) Register(p Provider) { r.providers[p.Name()] = p }
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// ---- Input normalization helpers shared by all providers --------------------

// NormalizeRoom strips whitespace, leaves digits + letters intact, lowercases.
// "  103 " → "103"; " A12 " → "a12".
func NormalizeRoom(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// NormalizeName lowercases + trims + collapses inner whitespace + strips
// diacritics. Accent-insensitive matching is important for international
// guests whose names may be entered without diacritics on a phone keypad.
//
// Diacritic stripping uses a small mapping for the common Latin set; for
// production we'd swap to golang.org/x/text/unicode/norm if requirements
// expand to non-Latin scripts.
func NormalizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		if r == '\'' {
			continue // O'Brien → obrien
		}
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		r = unicode.ToLower(r)
		if folded, ok := diacriticFold[r]; ok {
			r = folded
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// diacriticFold maps the most common accented Latin lowercase letters to
// their unaccented equivalents. Extended in steps as needed.
var diacriticFold = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ä': 'a', 'ã': 'a', 'å': 'a', 'ā': 'a',
	'ç': 'c', 'ć': 'c', 'č': 'c',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e', 'ė': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i',
	'ñ': 'n', 'ń': 'n',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o', 'ø': 'o', 'ō': 'o',
	'ş': 's', 'ś': 's', 'š': 's', 'ß': 's',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u', 'ū': 'u',
	'ý': 'y', 'ÿ': 'y',
	'ž': 'z', 'ź': 'z', 'ż': 'z',
}

// NormalizeReservation strips whitespace, uppercases. Reservation numbers
// are alphanumeric tokens with no semantic case.
func NormalizeReservation(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// MatchesQuery is a helper providers can use after they've located a
// candidate reservation by room number. It returns nil if the secondary
// field passes per the configured mode.
//
// Pass NormalizedX values for both inputs.
func MatchesQuery(mode Mode, qFirst, qLast, qRes, recFirst, recLast, recRes string) bool {
	first := qFirst != "" && qFirst == recFirst
	last := qLast != "" && qLast == recLast
	res := qRes != "" && qRes == recRes
	switch mode {
	case ModeRoomFirstName:
		return first
	case ModeRoomLastName:
		return last
	case ModeRoomReservation:
		return res
	case ModeEither:
		return first || last || res
	}
	return false
}

// Package pms abstracts hotel PMS integrations for guest auth: room_number plus one of first_name, last_name or
// reservation_number must match, per the configured mode (ModeRoomLastName, ModeRoomFirstName, ModeRoomReservation,
// ModeEither = any of the three). The portal sends all filled fields and the mode; the provider matches.
// This Provider interface and its scd loader (internal/pmsloader) are retired and kept for rollback; the live runtime
// is pmsd (internal/pmsd) with connectors in internal/pmsprovider. New connectors belong there. The FIAS framing
// helpers here remain in use by pmsd.
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

// Strings are normalized (trimmed) by the caller; the provider matches names case-insensitively.
type Query struct {
	RoomNumber        string
	FirstName         string
	LastName          string
	ReservationNumber string
	Mode              Mode
}

type Result struct {
	Valid         bool
	GuestName     string
	FirstName     string
	LastName      string
	CheckIn       time.Time
	CheckOut      time.Time
	RoomNumber    string
	ReservationID string
	Email         string
}

type Provider interface {
	// Name is the per-tenant unique name (pms_providers.name); registries are keyed on it.
	Name() string
	Kind() string
	// Configure is safe to call repeatedly (initial load and reloads).
	Configure(cfg ProviderConfig) error
	Config() ProviderConfig
	Health() Health
	ValidateGuest(ctx context.Context, q Query) (*Result, error)
}

type Starter interface {
	Start(ctx context.Context)
}

type Tester interface {
	TestConnection(ctx context.Context) error
}

type Cacher interface {
	CacheSnapshot(limit int) []Reservation
}

// Stop lets scd end old goroutines during a live reload before new instances take over.
type Stopper interface {
	Stop()
}

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
	AuthKey    string
	BaseURL    string
	APIKey     string
	PropertyID string
	Extra      map[string]any
}

// FieldMap routes canonical names to PMS paths/codes; empty means the provider's default. Canonical keys:
//
//	"room_number", "first_name", "last_name", "reservation_number",
//	"check_in", "check_out", "guest_email", "guest_display_name"
type FieldMap map[string]string

type Normalization struct {
	RoomFormat      string `json:"room_format"`
	NameStripTitles bool   `json:"name_strip_titles"`
	ReservationCase string `json:"reservation_case"`
}

type StayPolicy struct {
	EarlyCheckinMinutes int `json:"early_checkin_minutes"`
	LateCheckoutMinutes int `json:"late_checkout_minutes"`
	MinRemainingSeconds int `json:"min_remaining_seconds"`
}

type Health struct {
	Status         string    `json:"status"`
	ConnectedSince time.Time `json:"connected_since,omitempty"`
	LastRecordAt   time.Time `json:"last_record_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitempty"`
	CacheSize      int       `json:"cache_size"`
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

func NormalizeRoom(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// NormalizeName lowercases, trims, collapses whitespace and strips diacritics, so names typed without accents on a
// phone keypad still match.
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
			continue
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

func NormalizeReservation(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// MatchesQuery returns nil if the secondary field passes per the configured mode; pass normalized values for both
// inputs.
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

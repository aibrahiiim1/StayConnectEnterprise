package pms

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Stub is an in-memory PMS for dev/testing. Add reservations via Upsert;
// ValidateGuest does case-insensitive matching against the canonical fields.
type Stub struct {
	providerName string
	cfg          ProviderConfig

	mu   sync.RWMutex
	rows map[string]*Reservation // key = NormalizeRoom(room_number)
}

// Reservation mirrors what a real PMS would return for a stay.
type Reservation struct {
	RoomNumber        string    `json:"room_number"`
	FirstName         string    `json:"first_name"`
	LastName          string    `json:"last_name"`
	GuestDisplayName  string    `json:"guest_display_name,omitempty"` // optional, defaults to "First Last"
	ReservationNumber string    `json:"reservation_number"`
	CheckIn           time.Time `json:"check_in,omitempty"`
	CheckOut          time.Time `json:"check_out,omitempty"`
	Email             string    `json:"email,omitempty"`
}

// NewStub builds an unconfigured Stub. The loader calls Configure() right
// after, which sets the per-tenant name from the pms_providers row.
func NewStub(name string) *Stub {
	return &Stub{providerName: name, rows: map[string]*Reservation{}}
}

func (s *Stub) Name() string { return s.providerName }
func (s *Stub) Kind() string { return "stub" }

// Configure stores the per-tenant config. The Stub has no connection but
// honours the Normalization (RoomFormat / NameStripTitles) at match time.
func (s *Stub) Configure(cfg ProviderConfig) error {
	if cfg.Name != "" {
		s.providerName = cfg.Name
	}
	s.cfg = cfg
	return nil
}

func (s *Stub) Config() ProviderConfig { return s.cfg }

// Health for Stub is always "connected" — there's no link to drop.
func (s *Stub) Health() Health {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Health{Status: "connected", CacheSize: len(s.rows)}
}

// TestConnection: trivial OK; the in-memory cache has no link to dial.
func (s *Stub) TestConnection(_ context.Context) error { return nil }

// CacheSnapshot returns up to limit entries.
func (s *Stub) CacheSnapshot(limit int) []Reservation {
	if limit <= 0 {
		limit = 200
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Reservation, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, *r)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (s *Stub) Upsert(r Reservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := r
	s.rows[NormalizeRoom(cp.RoomNumber)] = &cp
}

func (s *Stub) Delete(roomNumber string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, NormalizeRoom(roomNumber))
}

func (s *Stub) ValidateGuest(_ context.Context, q Query) (*Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Apply per-tenant RoomFormat to BOTH sides so guests can type "103"
	// and match a stored "0103" (or vice versa) when the format is set.
	norm := s.cfg.Normalization
	queryRoom := NormalizeRoom(ApplyRoomFormat(norm.RoomFormat, q.RoomNumber))

	var r *Reservation
	for storedKey, rec := range s.rows {
		stored := NormalizeRoom(ApplyRoomFormat(norm.RoomFormat, rec.RoomNumber))
		if stored == queryRoom || storedKey == queryRoom {
			r = rec
			break
		}
	}
	if r == nil {
		return nil, ErrNotFound
	}

	// Apply name title-stripping (Mr./Mrs./...) before matching.
	qFirst := ApplyNameNormalization(norm, q.FirstName)
	qLast := ApplyNameNormalization(norm, q.LastName)
	qRes := ApplyReservationCase(norm, q.ReservationNumber)

	recFirst := ApplyNameNormalization(norm, r.FirstName)
	recLast := ApplyNameNormalization(norm, r.LastName)
	recRes := ApplyReservationCase(norm, r.ReservationNumber)

	if !MatchesQuery(q.Mode, qFirst, qLast, qRes, recFirst, recLast, recRes) {
		return nil, ErrNotFound
	}

	display := r.GuestDisplayName
	if display == "" {
		display = strings.TrimSpace(r.FirstName + " " + r.LastName)
	}

	return &Result{
		Valid:         true,
		GuestName:     display,
		FirstName:     r.FirstName,
		LastName:      r.LastName,
		CheckIn:       r.CheckIn,
		CheckOut:      r.CheckOut,
		RoomNumber:    r.RoomNumber,
		ReservationID: r.ReservationNumber,
		Email:         r.Email,
	}, nil
}

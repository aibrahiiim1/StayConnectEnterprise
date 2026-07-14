package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// maybeSeedPMSStubs is the env-gated seed entry point called from both
// main() startup and reloadPMS(). The 10-minute pmsReloadSafetyLoop builds
// a fresh *pms.Stub on every tick, so a startup-only seed is lost by the
// first reload. Gated by SCD_PMS_STUB_SEED=true — production leaves it off.
func maybeSeedPMSStubs(providers []pms.Provider) {
	if os.Getenv("SCD_PMS_STUB_SEED") != "true" {
		return
	}
	for _, p := range providers {
		if s, ok := p.(*pms.Stub); ok {
			seedPMSStubReservations(s)
			slog.Info("pms stub seeded", "name", s.Name())
		}
	}
}

// seedPMSStubReservations populates the in-memory Stub with a few sample
// reservations so the dev tenant has data to validate against without an
// external PMS. Reservations are anchored relative to "now" so the test
// suite always finds an active stay window.
func seedPMSStubReservations(s *pms.Stub) {
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)
	tomorrow := now.AddDate(0, 0, 2)

	s.Upsert(pms.Reservation{
		RoomNumber:        "101",
		FirstName:         "Alice",
		LastName:          "Anderson",
		ReservationNumber: "RES-1001",
		CheckIn:           yesterday,
		CheckOut:          tomorrow,
		Email:             "alice@example.com",
	})
	s.Upsert(pms.Reservation{
		RoomNumber:        "102",
		FirstName:         "Bob",
		LastName:          "O'Brien",
		ReservationNumber: "RES-1002",
		CheckIn:           yesterday,
		CheckOut:          tomorrow,
	})
	s.Upsert(pms.Reservation{
		RoomNumber:        "103",
		FirstName:         "Chloé",
		LastName:          "Dubois",
		ReservationNumber: "RES-1003",
		CheckIn:           yesterday,
		CheckOut:          tomorrow,
	})
	// A reservation that hasn't started yet — for the stay-window test.
	s.Upsert(pms.Reservation{
		RoomNumber:        "201",
		FirstName:         "Future",
		LastName:          "Guest",
		ReservationNumber: "RES-2001",
		CheckIn:           now.AddDate(0, 0, 7),
		CheckOut:          now.AddDate(0, 0, 10),
	})
	// A reservation that has already ended.
	s.Upsert(pms.Reservation{
		RoomNumber:        "202",
		FirstName:         "Past",
		LastName:          "Guest",
		ReservationNumber: "RES-2002",
		CheckIn:           now.AddDate(0, 0, -10),
		CheckOut:          now.AddDate(0, 0, -3),
	})
}

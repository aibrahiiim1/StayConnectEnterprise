package main

// Departures the engine could not place, shown as cases rather than rows. There is no action here by design:
// iam_v2.stay_events is one-way (p3_stay_event_appendonly refuses changes to a terminal row) and a checkout
// boundary must be an applied GO event, so a MANUAL_REVIEW departure is resolved only by the PMS sending one
// the engine can place. It never closes a stay on a planned date, treats shared rooms as duplicates, applies an
// old room-keyed departure to the current occupant, or hides a case to lower the count.

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type reconciliationCaseOut struct {
	CaseKey         string     `json:"case_key"`
	Reservation     string     `json:"reservation,omitempty"`
	Room            string     `json:"room,omitempty"`
	RepeatCount     int        `json:"repeat_count"`
	Generations     int        `json:"generations"`
	FirstSeenAt     time.Time  `json:"first_seen_at"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	EventAt         *time.Time `json:"event_at,omitempty"`
	LatestEventID   string     `json:"latest_event_id"`
	ReviewCode      string     `json:"review_code,omitempty"`
	CandidateStays  int        `json:"candidate_stays"`
	CandidateStayID *string    `json:"candidate_stay_id,omitempty"`
	RosterPresent   bool       `json:"roster_present"`
	ResolutionState string     `json:"resolution_state"`
	// True for every case: the screen states what would resolve it without re-deriving that in the browser.
	NeedsPMSEvidence bool `json:"needs_pms_evidence"`
	// The reservation number is withheld from roles without pms-stay-identity; the case key is then an opaque digest.
	IdentityHidden bool `json:"identity_hidden,omitempty"`
}

// Replaces a reservation-naming case key with a stable digest, so lists stay comparable without showing it.
func opaqueCaseKey(k string) string {
	sum := sha256.Sum256([]byte(k))
	return "case:" + hex.EncodeToString(sum[:8])
}

func (s *server) pmsReconciliationRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listReconciliationCases)
	r.Get("/summary", s.reconciliationSummary)
	r.Get("/rooms", s.listMultiOccupancyRooms)
	r.Get("/past-departure", s.listStaysPastDeparture)
	return r
}

const reconciliationCaseCols = `case_key, COALESCE(reservation,''), COALESCE(room,''),
       repeat_count, generations, first_seen_at, last_seen_at, event_at,
       latest_event_id::text, COALESCE(latest_review_code,''),
       candidate_stays, candidate_stay_id::text, roster_present, resolution_state`

func (s *server) scanReconciliationCases(w http.ResponseWriter, r *http.Request, extra string, args ...any) ([]reconciliationCaseOut, bool) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	all := append([]any{s.tenantID, s.siteID}, args...)
	rows, err := s.db.Query(ctx, `SELECT `+reconciliationCaseCols+`
		  FROM iam_v2.pms_reconciliation_cases
		 WHERE tenant_id=$1 AND site_id=$2 `+extra+`
		 ORDER BY repeat_count DESC, last_seen_at DESC
		 LIMIT 500`, all...)
	if err != nil {
		slog.Error("pms reconciliation: read failed", "code", "cases_unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "cases_unreadable", "the reconciliation data could not be read")
		return nil, false
	}
	defer rows.Close()

	identity := canSeeStayIdentity(r)
	out := []reconciliationCaseOut{}
	for rows.Next() {
		var c reconciliationCaseOut
		if err := rows.Scan(&c.CaseKey, &c.Reservation, &c.Room, &c.RepeatCount, &c.Generations,
			&c.FirstSeenAt, &c.LastSeenAt, &c.EventAt, &c.LatestEventID, &c.ReviewCode,
			&c.CandidateStays, &c.CandidateStayID, &c.RosterPresent, &c.ResolutionState); err != nil {
			slog.Error("pms reconciliation: read failed", "code", "cases_unreadable", "err", err)
			jsonErr(w, http.StatusInternalServerError, "cases_unreadable", "the reconciliation data could not be read")
			return nil, false
		}
		c.NeedsPMSEvidence = true
		if !identity {
			c.Reservation, c.CaseKey, c.IdentityHidden = "", opaqueCaseKey(c.CaseKey), true
		}
		out = append(out, c)
	}
	return out, true
}

func (s *server) listReconciliationCases(w http.ResponseWriter, r *http.Request) {
	extra := ""
	args := []any{}
	if st := strings.TrimSpace(r.URL.Query().Get("state")); st != "" {
		extra = "AND resolution_state = $3"
		args = append(args, st)
	}
	out, ok := s.scanReconciliationCases(w, r, extra, args...)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cases": out})
}

// The headline counts distinct departures and reports recorded_rows too. There is deliberately no "actionable"
// total: every case is waiting on the PMS.
func (s *server) reconciliationSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT resolution_state, count(*)::int, COALESCE(sum(repeat_count),0)::int
		  FROM iam_v2.pms_reconciliation_cases
		 WHERE tenant_id=$1 AND site_id=$2
		 GROUP BY resolution_state`, s.tenantID, s.siteID)
	if err != nil {
		slog.Error("pms reconciliation: read failed", "code", "summary_unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "summary_unreadable", "the reconciliation data could not be read")
		return
	}
	defer rows.Close()

	type bucket struct {
		State        string `json:"state"`
		Cases        int    `json:"cases"`
		RecordedRows int    `json:"recorded_rows"`
	}
	buckets := []bucket{}
	totalCases, totalRows := 0, 0
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.State, &b.Cases, &b.RecordedRows); err != nil {
			slog.Error("pms reconciliation: read failed", "code", "summary_unreadable", "err", err)
			jsonErr(w, http.StatusInternalServerError, "summary_unreadable", "the reconciliation data could not be read")
			return
		}
		totalCases += b.Cases
		totalRows += b.RecordedRows
		buckets = append(buckets, b)
	}

	var multiRooms, pastDeparture, pastDepartureAbsent int
	_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.pms_rooms_multi_occupancy
	                         WHERE tenant_id=$1 AND site_id=$2`, s.tenantID, s.siteID).Scan(&multiRooms)
	_ = s.db.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE NOT roster_present)::int
	                          FROM iam_v2.pms_stays_past_departure
	                         WHERE tenant_id=$1 AND site_id=$2`, s.tenantID, s.siteID).
		Scan(&pastDeparture, &pastDepartureAbsent)

	writeJSON(w, http.StatusOK, map[string]any{
		"cases": totalCases, "recorded_rows": totalRows,
		"by_state":                                buckets,
		"rooms_multi_occupancy":                   multiRooms,
		"stays_past_departure":                    pastDeparture,
		"stays_past_departure_absent_from_roster": pastDepartureAbsent,
	})
}

func (s *server) listMultiOccupancyRooms(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT room, stays_in_room, earliest_arrival, latest_planned_departure
		  FROM iam_v2.pms_rooms_multi_occupancy
		 WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY stays_in_room DESC, room LIMIT 500`, s.tenantID, s.siteID)
	if err != nil {
		slog.Error("pms reconciliation: read failed", "code", "rooms_unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "rooms_unreadable", "the reconciliation data could not be read")
		return
	}
	defer rows.Close()
	type roomOut struct {
		Room                   string     `json:"room"`
		StaysInRoom            int        `json:"stays_in_room"`
		EarliestArrival        *time.Time `json:"earliest_arrival,omitempty"`
		LatestPlannedDeparture *time.Time `json:"latest_planned_departure,omitempty"`
	}
	out := []roomOut{}
	for rows.Next() {
		var o roomOut
		if err := rows.Scan(&o.Room, &o.StaysInRoom, &o.EarliestArrival, &o.LatestPlannedDeparture); err != nil {
			slog.Error("pms reconciliation: read failed", "code", "rooms_unreadable", "err", err)
			jsonErr(w, http.StatusInternalServerError, "rooms_unreadable", "the reconciliation data could not be read")
			return
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": out})
}

func (s *server) listStaysPastDeparture(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT stay_id::text, COALESCE(room,''), COALESCE(reservation,''),
		       arrival, departure, days_past_departure, roster_present, room_now_holds_another_stay
		  FROM iam_v2.pms_stays_past_departure
		 WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY days_past_departure DESC, room LIMIT 500`, s.tenantID, s.siteID)
	if err != nil {
		slog.Error("pms reconciliation: read failed", "code", "stays_unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "stays_unreadable", "the reconciliation data could not be read")
		return
	}
	defer rows.Close()
	type stayOut struct {
		StayID        string     `json:"stay_id"`
		Room          string     `json:"room,omitempty"`
		Reservation   string     `json:"reservation,omitempty"`
		Arrival       *time.Time `json:"arrival,omitempty"`
		Departure     *time.Time `json:"departure,omitempty"`
		DaysPast      int        `json:"days_past_departure"`
		RosterPresent bool       `json:"roster_present"`
		// "Not listed" and "someone else is in that room now" differ: the second means the room was re-let.
		RoomReLet bool `json:"room_now_holds_another_stay"`
		// The reservation number was withheld.
		IdentityHidden bool `json:"identity_hidden,omitempty"`
	}
	identity := canSeeStayIdentity(r)
	out := []stayOut{}
	for rows.Next() {
		var o stayOut
		if err := rows.Scan(&o.StayID, &o.Room, &o.Reservation, &o.Arrival, &o.Departure,
			&o.DaysPast, &o.RosterPresent, &o.RoomReLet); err != nil {
			slog.Error("pms reconciliation: read failed", "code", "stays_unreadable", "err", err)
			jsonErr(w, http.StatusInternalServerError, "stays_unreadable", "the reconciliation data could not be read")
			return
		}
		if !identity {
			o.Reservation, o.IdentityHidden = "", true
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"stays": out})
}

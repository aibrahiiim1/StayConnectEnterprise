package main

// PMS RECONCILIATION — the departures nobody could place, as CASES rather than as rows.
//
// The property was told it had 12 026 messages needing attention. It had 397 unresolved departures, each
// restaged once per reconnect for weeks. Counting the rows was not a rounding error: it made the number
// useless, and a number nobody can act on is a warning nobody reads.
//
// WHAT THIS SCREEN WILL NOT DO. It will not close a stay because a planned departure date has passed — a
// planned date is a plan. It will not treat two stays in one room as a duplicate — sharing a room is
// ordinary. It will not apply an old room-keyed departure to whoever is in that room today. And it will not
// make a case disappear to lower the count: a case with no answer stays in the list, labelled with the
// evidence it is missing.
//
// THERE IS NO ACTION HERE, AND THAT IS THE CORRECT ANSWER RATHER THAN A MISSING FEATURE.
//
// This screen briefly offered "re-evaluate", which handed a recorded departure back to the ingestion engine.
// It could never have worked, and it should not: iam_v2.stay_events is strictly one-way (the
// p3_stay_event_appendonly trigger refuses any change to a terminal row's status), and separately a checkout
// boundary must be an APPLIED GO event pinned as the stay's application lineage. A MANUAL_REVIEW departure
// is neither and cannot become either.
//
// What those two invariants say together is worth stating plainly instead of working around: a departure the
// engine could not place is resolved by the PMS sending one it CAN place. The PMS is the source of truth for
// whether a guest has left, and a hotel system that checked a guest out on its own re-reading of an old
// message would be asserting something it does not know.
//
// The LIST was always the valuable half: 397 distinct departures instead of 12,271 rows, each labelled with
// the evidence it is waiting for. Counting honestly never needed a button.

import (
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
	// NeedsPMSEvidence is true for every case, and says so rather than implying some are actionable here.
	// It exists so the screen can state what WOULD resolve the case without re-deriving that in the browser.
	NeedsPMSEvidence bool `json:"needs_pms_evidence"`
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
		jsonErr(w, http.StatusInternalServerError, "cases_unreadable", err.Error())
		return nil, false
	}
	defer rows.Close()

	out := []reconciliationCaseOut{}
	for rows.Next() {
		var c reconciliationCaseOut
		if err := rows.Scan(&c.CaseKey, &c.Reservation, &c.Room, &c.RepeatCount, &c.Generations,
			&c.FirstSeenAt, &c.LastSeenAt, &c.EventAt, &c.LatestEventID, &c.ReviewCode,
			&c.CandidateStays, &c.CandidateStayID, &c.RosterPresent, &c.ResolutionState); err != nil {
			jsonErr(w, http.StatusInternalServerError, "cases_unreadable", err.Error())
			return nil, false
		}
		// Every case is answered by the PMS, whatever its classification. The state says which evidence.
		c.NeedsPMSEvidence = true
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

// reconciliationSummary is the honest headline: how many DISTINCT departures are outstanding, how many
// recorded copies sit behind them, and how the cases divide by what can actually be done about them.
//
// recorded_rows is reported rather than hidden. The gap between 397 and 12 271 is the single most useful
// fact about this feed, and rounding it away would be the same mistake in the other direction.
//
// There is no "actionable" total, because none of them is actionable HERE. Every case is waiting on the PMS,
// and a count implying otherwise would be the screen lying about what the property can do today.
func (s *server) reconciliationSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT resolution_state, count(*)::int, COALESCE(sum(repeat_count),0)::int
		  FROM iam_v2.pms_reconciliation_cases
		 WHERE tenant_id=$1 AND site_id=$2
		 GROUP BY resolution_state`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "summary_unreadable", err.Error())
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
			jsonErr(w, http.StatusInternalServerError, "summary_unreadable", err.Error())
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
		jsonErr(w, http.StatusInternalServerError, "rooms_unreadable", err.Error())
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
			jsonErr(w, http.StatusInternalServerError, "rooms_unreadable", err.Error())
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
		       arrival, departure, days_past_departure, roster_present
		  FROM iam_v2.pms_stays_past_departure
		 WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY days_past_departure DESC, room LIMIT 500`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "stays_unreadable", err.Error())
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
	}
	out := []stayOut{}
	for rows.Next() {
		var o stayOut
		if err := rows.Scan(&o.StayID, &o.Room, &o.Reservation, &o.Arrival, &o.Departure,
			&o.DaysPast, &o.RosterPresent); err != nil {
			jsonErr(w, http.StatusInternalServerError, "stays_unreadable", err.Error())
			return
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"stays": out})
}

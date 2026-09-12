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
// THE ONE ACTION IS "RE-EVALUATE", NOT "CLOSE". It hands the recorded event back to the same ingestion
// engine, which applies the same resolution rules and the same Checkout Converter it would have used the
// first time. This process has no checkout path of its own, deliberately: a second way to end a stay is a
// second set of rules about entitlements, grace and access, and those two sets would drift.

import (
	"encoding/json"
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
	ReofferCount    int        `json:"reoffer_count"`
	ResolutionState string     `json:"resolution_state"`
	// Actionable is the single question the screen asks of each row, answered here rather than in the
	// browser so the button and the server cannot disagree about what may be re-evaluated.
	Actionable bool `json:"actionable"`
}

func (s *server) pmsReconciliationRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listReconciliationCases)
	r.Get("/summary", s.reconciliationSummary)
	r.Get("/rooms", s.listMultiOccupancyRooms)
	r.Get("/past-departure", s.listStaysPastDeparture)
	r.Post("/{eventID}/re-evaluate", s.reofferStayEvent)
	return r
}

// actionable: only a case where two independent facts agree may be handed back to the engine.
//
// RESOLVABLE means exactly one candidate stay, that stay began before the departure was raised, and the
// PMS's own latest complete roster does not contain it. Every other state is either already settled
// (SUPERSEDED_ROOM_EMPTY), contradicted by fresh evidence (ROSTER_CONTRADICTS), or genuinely undecidable
// from what we hold (ROOM_SHARED, LATER_OCCUPANT, NEEDS_PMS_EVIDENCE). Re-offering those would simply
// produce the same review code again, which is noise dressed as progress.
func actionableState(state string) bool { return state == "RESOLVABLE" }

// mustJSON renders the captured evidence. It carries only the classification facts assembled above — no
// payload, no guest name, no reservation holder — so a marshalling failure is not a real case; an empty
// object is the honest fallback rather than an error the operator cannot act on.
func mustJSON(v map[string]any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

const reconciliationCaseCols = `case_key, COALESCE(reservation,''), COALESCE(room,''),
       repeat_count, generations, first_seen_at, last_seen_at, event_at,
       latest_event_id::text, COALESCE(latest_review_code,''),
       candidate_stays, candidate_stay_id::text, roster_present, reoffer_count, resolution_state`

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
			&c.CandidateStays, &c.CandidateStayID, &c.RosterPresent, &c.ReofferCount,
			&c.ResolutionState); err != nil {
			jsonErr(w, http.StatusInternalServerError, "cases_unreadable", err.Error())
			return nil, false
		}
		c.Actionable = actionableState(c.ResolutionState)
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
		Actionable   bool   `json:"actionable"`
	}
	buckets := []bucket{}
	totalCases, totalRows, actionable := 0, 0, 0
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.State, &b.Cases, &b.RecordedRows); err != nil {
			jsonErr(w, http.StatusInternalServerError, "summary_unreadable", err.Error())
			return
		}
		b.Actionable = actionableState(b.State)
		totalCases += b.Cases
		totalRows += b.RecordedRows
		if b.Actionable {
			actionable += b.Cases
		}
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
		"cases": totalCases, "recorded_rows": totalRows, "actionable_cases": actionable,
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

// reofferStayEvent hands ONE recorded event back to the ingestion engine.
//
// The evidence the operator was acting on is captured into the append-only log at the moment of the
// decision, because "the roster did not contain this stay" is a statement about a roster that will have
// been replaced by the time anybody reads the record.
//
// The state is re-read here rather than trusted from the browser: a case that was RESOLVABLE when the list
// was rendered may have been answered by a newer roster in the meantime, and acting on a stale screen is
// exactly how a resident guest gets checked out.
func (s *server) reofferStayEvent(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "eventID")
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if len(reason) < 3 {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"Say why this departure is being re-evaluated — it is recorded with your name.")
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the action could not be attributed to an operator")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	var state, caseKey, iface string
	var candidates int
	var rosterPresent bool
	var lastSync *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT resolution_state, case_key, pms_interface_id::text,
		       candidate_stays, roster_present, last_complete_sync_at
		  FROM iam_v2.pms_reconciliation_cases
		 WHERE tenant_id=$1 AND site_id=$2 AND latest_event_id=$3::uuid`,
		s.tenantID, s.siteID, eventID).
		Scan(&state, &caseKey, &iface, &candidates, &rosterPresent, &lastSync)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "case_not_found",
			"That departure is no longer an open case — it may have been answered since this list was loaded.")
		return
	}
	if !actionableState(state) {
		jsonErr(w, http.StatusConflict, "not_actionable",
			"This case is "+state+". Re-evaluating it would produce the same answer; it needs evidence, not another attempt.")
		return
	}

	evidence := map[string]any{
		"resolution_state": state,
		"case_key":         caseKey,
		"candidate_stays":  candidates,
		"roster_present":   rosterPresent,
	}
	if lastSync != nil {
		evidence["last_complete_sync_at"] = lastSync.UTC()
	}

	var ok bool
	if err := s.db.QueryRow(ctx, `
		SELECT iam_v2.pms_reoffer_stay_event($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7::jsonb)`,
		s.tenantID, s.siteID, iface, eventID, actor, reason, mustJSON(evidence)).Scan(&ok); err != nil {
		jsonErr(w, http.StatusInternalServerError, "reoffer_failed", err.Error())
		return
	}
	if !ok {
		jsonErr(w, http.StatusConflict, "not_reofferable",
			"That event is no longer awaiting review, so there is nothing to re-evaluate.")
		return
	}

	s.audit(r, "pms_reconciliation.re_evaluate", "stay_event", eventID, map[string]any{
		"case_key": caseKey, "resolution_state": state, "reason": reason,
		"candidate_stays": candidates, "roster_present": rosterPresent,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"re_evaluating": true,
		"note": "Handed back to the PMS ingestion engine. It applies the same rules it would have applied " +
			"when the departure arrived, including the checkout policy — this action did not close the stay " +
			"itself. The case disappears from this list once the engine has answered it.",
	})
}

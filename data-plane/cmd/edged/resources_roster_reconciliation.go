package main

// Roster reconciliation: the operator surface for closing stays the PMS no longer lists.
//
// WHY THIS IS A SEPARATE KEY FROM pms-reconciliation. Reading "which guests does the PMS and the mirror
// disagree about" is a question the desk asks hourly. Closing three hundred stays in one action is not the
// same power, and a role that may do the first has no business doing the second by accident. So the case
// list stays where it was, read-only for everyone, and this resource carries the action.
//
// WHAT CHANGED SINCE THE CASE LIST WAS WRITTEN. Its comment said there is no local action, because
// stay_events is one-way and a checkout boundary must be an APPLIED GO event. That is still true of an
// individual recorded departure, and nothing here rewrites one. What is new is that a COMPLETE published
// roster is authoritative evidence in its own right: it is the PMS stating, in full, who is in the building.
// Closing a stay it does not mention is a deduction from a complete statement, not a re-interpretation of a
// departure the engine already refused.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type reconcileRunOut struct {
	Outcome          string  `json:"outcome"`
	RosterSize       int     `json:"roster_size"`
	MirrorInHouse    int     `json:"mirror_in_house"`
	AbsentFromRoster int     `json:"absent_from_roster"`
	StaysClosed      int     `json:"stays_closed"`
	RoomsEnumerated  int     `json:"rooms_enumerated"`
	RoomsExpected    int     `json:"rooms_expected"`
	Protected        int     `json:"protected_by_newer_events"`
	RunID            *string `json:"run_id,omitempty"`
}

func (s *server) rosterReconciliationRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.rosterReconciliationState)
	r.Get("/runs", s.listReconciliationRuns)
	r.Post("/preview", s.previewRosterReconcile)
	// NO APPLY AND NO DISPOSE ROUTE. Reconciliation runs automatically on every complete published
	// generation, and the historical snapshot disposition was a one-time migration that has been executed
	// and can never have new input -- the connector no longer admits the records that created it. A manual
	// trigger for either would be a repair button for work that is not outstanding.
	r.Put("/settings", s.updateReconciliationSettings)
	r.Put("/connection-settings", s.updateConnectionSettings)
	return r
}

// rosterReconciliationState answers the one question the screen opens with: what would happen if I ran this
// now, and on what evidence. It is a DRY RUN, so opening the page never changes anything.
func (s *server) rosterReconciliationState(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	var (
		floor, tol, look, cap_ int
		version                int64
		isDefault              bool
	)
	if err := s.db.QueryRow(ctx, `SELECT roster_trust_min, inventory_tolerance, inventory_lookback,
		max_close_per_run, config_version, is_default
		FROM iam_v2.pms_reconciliation_settings_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID).Scan(&floor, &tol, &look, &cap_, &version, &isDefault); err != nil {
		jsonErr(w, http.StatusInternalServerError, "settings_unreadable", err.Error())
		return
	}

	iface, gen, ok := s.currentInterfaceAndGeneration(w, r)
	if !ok {
		return
	}

	preview, ok := s.runReconcile(w, r, iface, gen, false, "opened the reconciliation screen")
	if !ok {
		return
	}

	// ELIGIBLE SNAPSHOT ARTIFACTS ONLY, which is narrower than "unanswered" and deliberately so.
	//
	// This counted every unanswered review event, so it reported the one LIVE departure that names a
	// reservation with no arrival -- a genuine external question -- as though a bulk disposition could
	// answer it. It cannot, and must never try. Only a resync-admitted departure carrying no reservation is
	// a roster snapshot, and only those are counted here.
	var pending int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.stay_events e
		 WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.processing_status='MANUAL_REVIEW'
		   AND e.event_type='GO' AND e.admission_kind='RESYNC'
		   AND btrim(COALESCE(e.payload->>'reservation','')) = ''
		   AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions x WHERE x.stay_event_id=e.id)`,
		s.tenantID, s.siteID).Scan(&pending)

	// WHAT IS STANDING IN THE WAY, in the same response as the state itself. A blocker is derived from facts
	// already recorded -- there is nothing to acknowledge and nothing to clear -- so it appears exactly as
	// long as the condition lasts. Each one says whether guests are affected, because a PMS link alarm reads
	// as an outage unless it states plainly that the mirror is still authorising people.
	blockers := []map[string]any{}
	if rows, err := s.db.Query(ctx,
		`SELECT blocker, since, detail, guests_affected
		   FROM iam_v2.pms_integration_blockers($1::uuid,$2::uuid)`, s.tenantID, s.siteID); err == nil {
		defer rows.Close()
		for rows.Next() {
			var kind, detail string
			var since *time.Time
			var affected bool
			if err := rows.Scan(&kind, &since, &detail, &affected); err == nil {
				blockers = append(blockers, map[string]any{
					"blocker": kind, "since": since, "detail": detail, "guests_affected": affected,
				})
			}
		}
	}

	var cMinMs, cMaxMs, cStable, cLinkDown, cRefusals int
	var cVersion int64
	var cDefault bool
	_ = s.db.QueryRow(ctx, `SELECT backoff_min_ms, backoff_max_ms, stable_reset_seconds,
		link_down_alert_seconds, blocked_after_refusals, config_version, is_default
		FROM iam_v2.pms_connection_settings_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID).
		Scan(&cMinMs, &cMaxMs, &cStable, &cLinkDown, &cRefusals, &cVersion, &cDefault)

	writeJSON(w, http.StatusOK, map[string]any{
		"blockers": blockers,
		"connection_settings": map[string]any{
			"backoff_min_ms": cMinMs, "backoff_max_ms": cMaxMs,
			"stable_reset_seconds": cStable, "link_down_alert_seconds": cLinkDown,
			"blocked_after_refusals": cRefusals,
			"config_version":         cVersion, "is_default": cDefault,
		},
		"settings": map[string]any{
			"roster_trust_min": floor, "inventory_tolerance": tol,
			"inventory_lookback": look, "max_close_per_run": cap_,
			"config_version": version, "is_default": isDefault,
		},
		"generation":       gen,
		"preview":          preview,
		"undisposed_cases": pending,
		"pms_interface_id": iface,
	})
}

func (s *server) currentInterfaceAndGeneration(w http.ResponseWriter, r *http.Request) (string, int64, bool) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var iface string
	var gen int64
	err := s.db.QueryRow(ctx, `SELECT pms_interface_id::text, published_resync_generation
		  FROM iam_v2.pms_interface_runtime WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY published_resync_generation DESC LIMIT 1`, s.tenantID, s.siteID).Scan(&iface, &gen)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "no_interface_runtime",
			"no PMS interface runtime for this site")
		return "", 0, false
	}
	return iface, gen, true
}

// runReconcile is the single call site for the reconcile function. Everything that reaches it from here is a
// DRY RUN: the screen shows what a run would do, and the run that actually closes stays is pmsd's, on a
// complete published generation.
func (s *server) runReconcile(w http.ResponseWriter, r *http.Request, iface string, gen int64,
	apply bool, reason string) (*reconcileRunOut, bool) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	sess := sessFrom(r.Context())
	operator := "unknown"
	if sess != nil {
		operator = sess.Email
		if strings.TrimSpace(operator) == "" {
			operator = sess.OperatorID
		}
	}

	var out reconcileRunOut
	err := s.db.QueryRow(ctx, `SELECT outcome, roster_size, mirror_in_house, absent_from_roster,
		stays_closed, rooms_enumerated, rooms_expected, protected, run_id::text
		FROM iam_v2.pms_roster_reconcile($1::uuid,$2::uuid,$3::uuid,$4::bigint,$5,$6,$7)`,
		s.tenantID, s.siteID, iface, gen, operator, apply, reason).
		Scan(&out.Outcome, &out.RosterSize, &out.MirrorInHouse, &out.AbsentFromRoster,
			&out.StaysClosed, &out.RoomsEnumerated, &out.RoomsExpected, &out.Protected, &out.RunID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "reconcile_failed", err.Error())
		return nil, false
	}
	return &out, true
}

func (s *server) previewRosterReconcile(w http.ResponseWriter, r *http.Request) {
	iface, gen, ok := s.currentInterfaceAndGeneration(w, r)
	if !ok {
		return
	}
	out, ok := s.runReconcile(w, r, iface, gen, false, bodyReason(r))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) listReconciliationRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT mode, outcome, roster_size, mirror_in_house, absent_from_roster,
		stays_closed, rooms_enumerated, rooms_expected, protected_by_newer_events,
		resync_generation, run_at, run_by, COALESCE(reason,'')
		FROM iam_v2.pms_roster_reconciliation_runs
		WHERE tenant_id=$1 AND site_id=$2 ORDER BY run_at DESC LIMIT 100`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "runs_unreadable", err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var mode, outcome, runBy, reason string
		var roster, mirror, absent, closed, rooms, expected, protected int
		var gen int64
		var at any
		if err := rows.Scan(&mode, &outcome, &roster, &mirror, &absent, &closed, &rooms, &expected,
			&protected, &gen, &at, &runBy, &reason); err != nil {
			jsonErr(w, http.StatusInternalServerError, "runs_unreadable", err.Error())
			return
		}
		out = append(out, map[string]any{
			"mode": mode, "outcome": outcome, "roster_size": roster, "mirror_in_house": mirror,
			"absent_from_roster": absent, "stays_closed": closed, "rooms_enumerated": rooms,
			"rooms_expected": expected, "protected_by_newer_events": protected,
			"resync_generation": gen, "run_at": at, "run_by": runBy, "reason": reason,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (s *server) updateReconciliationSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var in struct {
		RosterTrustMin     *int   `json:"roster_trust_min"`
		InventoryTolerance *int   `json:"inventory_tolerance"`
		InventoryLookback  *int   `json:"inventory_lookback"`
		MaxClosePerRun     *int   `json:"max_close_per_run"`
		Reason             string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	sess := sessFrom(r.Context())
	operator := "unknown"
	if sess != nil {
		operator = sess.Email
	}
	// Read current values so a partial update changes only what was sent.
	var floor, tol, look, cap_ int
	var version int64
	var isDefault bool
	if err := s.db.QueryRow(ctx, `SELECT roster_trust_min, inventory_tolerance, inventory_lookback,
		max_close_per_run, config_version, is_default
		FROM iam_v2.pms_reconciliation_settings_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID).Scan(&floor, &tol, &look, &cap_, &version, &isDefault); err != nil {
		jsonErr(w, http.StatusInternalServerError, "settings_unreadable", err.Error())
		return
	}
	if in.RosterTrustMin != nil {
		floor = *in.RosterTrustMin
	}
	if in.MaxClosePerRun != nil {
		cap_ = *in.MaxClosePerRun
	}
	if in.InventoryTolerance != nil {
		tol = *in.InventoryTolerance
	}
	if in.InventoryLookback != nil {
		look = *in.InventoryLookback
	}
	var newVersion int64
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.pms_reconciliation_settings_set($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8)`,
		s.tenantID, s.siteID, floor, cap_, operator, in.Reason, tol, look).Scan(&newVersion); err != nil {
		jsonErr(w, http.StatusBadRequest, "settings_rejected", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config_version": newVersion})
}

func bodyReason(r *http.Request) string {
	var in struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	return strings.TrimSpace(in.Reason)
}

// updateConnectionSettings changes the reconnect and blocker-reporting bounds. Every field is optional so a
// partial change touches nothing else, and the definer function records who and why -- the connector reads
// these and can never write them.
func (s *server) updateConnectionSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var in struct {
		BackoffMinMs         *int   `json:"backoff_min_ms"`
		BackoffMaxMs         *int   `json:"backoff_max_ms"`
		StableResetSeconds   *int   `json:"stable_reset_seconds"`
		LinkDownAlertSeconds *int   `json:"link_down_alert_seconds"`
		BlockedAfterRefusals *int   `json:"blocked_after_refusals"`
		Reason               string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	sess := sessFrom(r.Context())
	operator := "unknown"
	if sess != nil && strings.TrimSpace(sess.Email) != "" {
		operator = sess.Email
	} else if sess != nil {
		operator = sess.OperatorID
	}
	var version int64
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.pms_connection_settings_set($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9)`,
		s.tenantID, s.siteID, operator, in.Reason,
		in.BackoffMinMs, in.BackoffMaxMs, in.StableResetSeconds,
		in.LinkDownAlertSeconds, in.BlockedAfterRefusals).Scan(&version); err != nil {
		jsonErr(w, http.StatusBadRequest, "settings_rejected", err.Error())
		return
	}
	s.audit(r, "pms_connection_settings.update", "pms_connection_settings", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}

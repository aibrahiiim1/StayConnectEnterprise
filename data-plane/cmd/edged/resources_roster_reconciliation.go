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
	"context"
	"encoding/json"
	"log/slog"
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
	// NO CONNECTION-SETTINGS ROUTE HERE ANY MORE. The recovery bounds are per interface now, so they live
	// under the interface they govern: PUT /pms-interfaces/{id}/connection-settings. A site-level route
	// would have to answer "which connection?" by guessing.
	return r
}

// rosterReconciliationState answers what the screen opens with: the settings, what is standing in the way, the
// connections a run can be about, and the LAST run recorded for the chosen one. It runs nothing (audit H-13):
// a dry run writes its own row to the run ledger, so running one on every page open made a READ permission
// write, and the ledger grow, every time anybody looked. "Run dry run" is an explicit action (POST /preview,
// write permission).
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
		slog.Error("roster reconciliation: settings unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "settings_unreadable", "the reconciliation settings could not be read")
		return
	}

	ifaces, err := s.rosterInterfaces(ctx)
	if err != nil {
		slog.Error("roster reconciliation: interfaces unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the PMS connections could not be read")
		return
	}
	iface, gen, ok := s.pickRosterInterface(w, ifaces, r.URL.Query().Get("pms_interface_id"))
	if !ok {
		return
	}

	// The most recent run of either kind for this connection: pmsd's automatic APPLY, or a dry run an
	// operator asked for. Shown as what it is -- a record with a time -- never recomputed on read.
	var last *reconcileRunOut
	var lastMode string
	var lastAt *time.Time
	var lr reconcileRunOut
	var runID string
	if err := s.db.QueryRow(ctx, `SELECT id::text, mode, outcome, roster_size, mirror_in_house, absent_from_roster,
			stays_closed, rooms_enumerated, rooms_expected, protected_by_newer_events, run_at
		  FROM iam_v2.pms_roster_reconciliation_runs
		 WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid
		 ORDER BY run_at DESC LIMIT 1`, s.tenantID, s.siteID, iface).
		Scan(&runID, &lastMode, &lr.Outcome, &lr.RosterSize, &lr.MirrorInHouse, &lr.AbsentFromRoster,
			&lr.StaysClosed, &lr.RoomsEnumerated, &lr.RoomsExpected, &lr.Protected, &lastAt); err == nil {
		lr.RunID = &runID
		last = &lr
	}

	// ELIGIBLE SNAPSHOT ARTIFACTS ONLY, which is narrower than "unanswered" and deliberately so: only a
	// resync-admitted departure carrying no reservation is a roster snapshot.
	var pending int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.pms_unanswered_review_events e
		 WHERE e.tenant_id=$1 AND e.site_id=$2
		   AND e.event_type='GO' AND e.admission_kind='RESYNC'
		   AND btrim(COALESCE(e.payload->>'reservation','')) = ''`,
		s.tenantID, s.siteID).Scan(&pending)

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

	writeJSON(w, http.StatusOK, map[string]any{
		"blockers": blockers,
		"settings": map[string]any{
			"roster_trust_min": floor, "inventory_tolerance": tol,
			"inventory_lookback": look, "max_close_per_run": cap_,
			"config_version": version, "is_default": isDefault,
		},
		"generation":       gen,
		"last_run":         last,
		"last_run_mode":    lastMode,
		"last_run_at":      lastAt,
		"undisposed_cases": pending,
		"pms_interface_id": iface,
		"interfaces":       ifaces,
	})
}

// rosterInterface is one connection a reconciliation can be about.
type rosterInterface struct {
	ID                  string     `json:"id"`
	Label               string     `json:"label"`
	ConnectorKind       string     `json:"connector_kind"`
	LifecycleState      string     `json:"lifecycle_state"`
	PublishedGeneration int64      `json:"published_generation"`
	LastCompleteSyncAt  *time.Time `json:"last_complete_sync_at,omitempty"`
}

// rosterInterfaces lists every connection with a runtime, the one a run should default to first: ACTIVE before
// anything else, then the one that most recently completed a sync. It used to take whichever runtime row had
// the highest generation number (audit H-21) -- on a site with two connections that is an arbitrary choice,
// because generation numbers are per connection and say nothing about which one is in service.
func (s *server) rosterInterfaces(ctx context.Context) ([]rosterInterface, error) {
	rows, err := s.db.Query(ctx, `SELECT rt.pms_interface_id::text, COALESCE(i.display_label,''),
			COALESCE(i.connector_kind,''), COALESCE(i.lifecycle_state,''),
			rt.published_resync_generation, rt.last_complete_sync_at
		  FROM iam_v2.pms_interface_runtime rt
		  JOIN iam_v2.pms_interfaces i
		    ON i.tenant_id = rt.tenant_id AND i.site_id = rt.site_id AND i.id = rt.pms_interface_id
		 WHERE rt.tenant_id=$1 AND rt.site_id=$2
		 ORDER BY (i.lifecycle_state = 'ACTIVE') DESC, rt.last_complete_sync_at DESC NULLS LAST,
		          rt.published_resync_generation DESC, rt.pms_interface_id
		 LIMIT 50`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rosterInterface{}
	for rows.Next() {
		var ri rosterInterface
		if err := rows.Scan(&ri.ID, &ri.Label, &ri.ConnectorKind, &ri.LifecycleState,
			&ri.PublishedGeneration, &ri.LastCompleteSyncAt); err != nil {
			return nil, err
		}
		out = append(out, ri)
	}
	return out, rows.Err()
}

// pickRosterInterface resolves the requested connection (it must be one of this site's) or the default.
func (s *server) pickRosterInterface(w http.ResponseWriter, ifaces []rosterInterface, requested string) (string, int64, bool) {
	requested = strings.TrimSpace(requested)
	if requested != "" && !uuidRe.MatchString(requested) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "pms_interface_id must be a connection id")
		return "", 0, false
	}
	for _, ri := range ifaces {
		if requested == "" || ri.ID == requested {
			return ri.ID, ri.PublishedGeneration, true
		}
	}
	if requested != "" {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS connection on this property")
		return "", 0, false
	}
	jsonErr(w, http.StatusServiceUnavailable, "no_interface_runtime", "no PMS interface runtime for this site")
	return "", 0, false
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
		slog.Error("roster reconciliation dry run failed", "interface", iface, "err", err)
		jsonErr(w, http.StatusInternalServerError, "reconcile_failed", "the dry run could not be completed")
		return nil, false
	}
	return &out, true
}

// previewRosterReconcile is "Run dry run": WRITE permission, because it records a row in the run ledger.
func (s *server) previewRosterReconcile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason         string `json:"reason"`
		PMSInterfaceID string `json:"pms_interface_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	ctx, cancel := dbCtx(r)
	ifaces, err := s.rosterInterfaces(ctx)
	cancel()
	if err != nil {
		slog.Error("roster reconciliation: interfaces unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the PMS connections could not be read")
		return
	}
	iface, gen, ok := s.pickRosterInterface(w, ifaces, in.PMSInterfaceID)
	if !ok {
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "dry run requested from the console"
	}
	out, ok := s.runReconcile(w, r, iface, gen, false, reason)
	if !ok {
		return
	}
	s.audit(r, "pms_roster_reconciliation.dry_run", "pms_interfaces", iface,
		map[string]any{"generation": gen, "outcome": out.Outcome, "absent_from_roster": out.AbsentFromRoster})
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
		slog.Error("roster reconciliation: runs unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "runs_unreadable", "the run history could not be read")
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
			slog.Error("roster reconciliation: runs unreadable", "err", err)
			jsonErr(w, http.StatusInternalServerError, "runs_unreadable", "the run history could not be read")
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

// updateReconciliationSettings changes the roster-trust settings (audit H-5): password step-up, the version the
// operator was looking at, a reason, bounds re-checked by the database, the change appended to the settings
// change log by the definer, and an audit_log row with the actor.
func (s *server) updateReconciliationSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedVersion    *int64 `json:"expected_config_version"`
		RosterTrustMin     *int   `json:"roster_trust_min"`
		InventoryTolerance *int   `json:"inventory_tolerance"`
		InventoryLookback  *int   `json:"inventory_lookback"`
		MaxClosePerRun     *int   `json:"max_close_per_run"`
		Reason             string `json:"reason"`
		Password           string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if in.ExpectedVersion == nil {
		jsonErr(w, http.StatusBadRequest, "expected_version_required",
			"expected_config_version is required so a concurrent change cannot be silently overwritten")
		return
	}
	if l := len(strings.TrimSpace(in.Reason)); l < 3 || l > 500 {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a reason of 3 to 500 characters is required")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	operator := operatorLabel(r)
	if operator == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Read current values so a partial update changes only what was sent.
	var floor, tol, look, cap_ int
	var version int64
	var isDefault bool
	if err := s.db.QueryRow(ctx, `SELECT roster_trust_min, inventory_tolerance, inventory_lookback,
		max_close_per_run, config_version, is_default
		FROM iam_v2.pms_reconciliation_settings_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID).Scan(&floor, &tol, &look, &cap_, &version, &isDefault); err != nil {
		slog.Error("roster reconciliation: settings unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "settings_unreadable", "the reconciliation settings could not be read")
		return
	}
	before := map[string]any{"roster_trust_min": floor, "max_close_per_run": cap_,
		"inventory_tolerance": tol, "inventory_lookback": look}
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
		`SELECT iam_v2.pms_reconciliation_settings_set_checked($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9)`,
		s.tenantID, s.siteID, *in.ExpectedVersion, floor, cap_, tol, look, operator, strings.TrimSpace(in.Reason)).
		Scan(&newVersion); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "RECON_SETTINGS_VERSION_CONFLICT"):
			jsonErr(w, http.StatusConflict, "version_conflict", "the settings changed since you opened them; reload and try again")
		case strings.Contains(msg, "RECON_SETTINGS_BOUNDS"), strings.Contains(msg, "RECON_SETTINGS_REASON"):
			jsonErr(w, http.StatusBadRequest, "settings_rejected", boundedDBMessage(msg))
		default:
			slog.Error("roster reconciliation: settings write failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the settings could not be saved")
		}
		return
	}
	s.audit(r, "pms_roster_reconciliation.settings_changed", "pms_reconciliation_settings", s.siteID,
		map[string]any{"before": before, "after": map[string]any{"roster_trust_min": floor, "max_close_per_run": cap_,
			"inventory_tolerance": tol, "inventory_lookback": look}, "config_version": newVersion,
			"reason": strings.TrimSpace(in.Reason)})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": newVersion})
}

// operatorLabel is how a definer records who acted: the operator's e-mail, or their id when none is known.
func operatorLabel(r *http.Request) string {
	sess := sessFrom(r.Context())
	if sess == nil {
		return ""
	}
	if v := strings.TrimSpace(sess.Email); v != "" {
		return v
	}
	return strings.TrimSpace(sess.OperatorID)
}

// boundedDBMessage returns the operator-facing part of a definer's RAISE ("CODE: sentence"), never a driver
// prefix, SQL state or stack: "ERROR: RECON_SETTINGS_BOUNDS: the ... (SQLSTATE 23514)" becomes "the ...".
func boundedDBMessage(msg string) string {
	if i := strings.Index(msg, " (SQLSTATE"); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.TrimPrefix(msg, "ERROR: ")
	if i := strings.Index(msg, ": "); i > 0 && i < 48 && strings.ToUpper(msg[:i]) == msg[:i] {
		msg = msg[i+2:]
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func bodyReason(r *http.Request) string {
	var in struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	return strings.TrimSpace(in.Reason)
}

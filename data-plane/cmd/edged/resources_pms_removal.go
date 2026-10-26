package main

// Removing a PMS connection. Two different things can be meant, and only the connection's own history decides which:
//
//	DELETE  the connection was only ever configured: switched off, never connected to its PMS, and nothing refers to it. It is removed.
//	RETIRE  the connection was used (a stay, an event, a posting, a routing map, a connected session). It is switched to the terminal DECOMMISSIONED state, hidden from
//	        ordinary lists, and every stay, event, entitlement, posting and audit record it produced is kept untouched.
//
// The assessment never reassigns routing, never touches a stay or a posting, and refuses while the connection is switched on, while a serving network is routed to it, or while
// financial work for it is open. The same assessment runs again inside the transaction that acts, under a row lock, so what the administrator confirmed is what happens.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	removalDelete  = "DELETE"
	removalRetire  = "RETIRE"
	removalBlocked = "BLOCKED"
	removalRetired = "RETIRED"
)

type removalNetwork struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type removalBlocker struct {
	Code     string           `json:"code"`
	Title    string           `json:"title"`
	Detail   string           `json:"detail"`
	Networks []removalNetwork `json:"networks,omitempty"`
}

type removalFacts struct {
	Stays            int  `json:"stays"`
	InHouseStays     int  `json:"in_house_stays"`
	LiveEntitlements int  `json:"live_entitlements"`
	Events           int  `json:"events"`
	Postings         int  `json:"postings"`
	Revisions        int  `json:"revisions"`
	EverConnected    bool `json:"ever_connected"`
	EverActivated    bool `json:"ever_activated"`
}

type removalAssessment struct {
	ID            string           `json:"id"`
	Label         string           `json:"display_label"`
	ConnectorKind string           `json:"connector_kind"`
	Lifecycle     string           `json:"lifecycle_state"`
	Outcome       string           `json:"outcome"`
	Blockers      []removalBlocker `json:"blockers"`
	Facts         removalFacts     `json:"facts"`
	Consequences  []string         `json:"consequences"`
	// Networks that are not serving but still point at this connection; retiring leaves them as they are and the routing screen shows them as unavailable.
	StaleRouting []removalNetwork `json:"stale_routing,omitempty"`
}

func isUnusedRefusal(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23514" || pg.Code == "23503" || strings.HasPrefix(pg.Message, "PMS_INTERFACE_IN_USE")
	}
	return false
}

func (s *server) assessRemoval(ctx context.Context, tx pgx.Tx, id string, lock bool) (removalAssessment, error) {
	var a removalAssessment
	a.Blockers = []removalBlocker{}
	a.Consequences = []string{}
	sql := `SELECT id::text, COALESCE(display_label,''), connector_kind, lifecycle_state FROM iam_v2.pms_interfaces WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`
	if lock {
		sql += ` FOR UPDATE`
	}
	if err := tx.QueryRow(ctx, sql, s.tenantID, s.siteID, id).Scan(&a.ID, &a.Label, &a.ConnectorKind, &a.Lifecycle); err != nil {
		return a, err
	}
	if a.Lifecycle == "DECOMMISSIONED" {
		a.Outcome = removalRetired
		return a, nil
	}

	f := &a.Facts
	one := func(q string, dst ...any) error { return tx.QueryRow(ctx, q, s.tenantID, s.siteID, id).Scan(dst...) }
	if err := one(`SELECT count(*)::int, count(*) FILTER (WHERE status='IN_HOUSE')::int FROM iam_v2.stays WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid`, &f.Stays, &f.InHouseStays); err != nil {
		return a, err
	}
	if err := one(`SELECT count(*)::int FROM iam_v2.entitlements e JOIN iam_v2.stays s ON s.id=e.stay_id
		WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.pms_interface_id=$3::uuid AND e.status IN ('PENDING','ACTIVE','SUSPENDED')`, &f.LiveEntitlements); err != nil {
		return a, err
	}
	if err := one(`SELECT count(*)::int FROM iam_v2.stay_events WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid`, &f.Events); err != nil {
		return a, err
	}
	if err := one(`SELECT count(*)::int FROM iam_v2.pms_postings WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid`, &f.Postings); err != nil {
		return a, err
	}
	if err := one(`SELECT count(*)::int FROM iam_v2.pms_interface_revisions WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid`, &f.Revisions); err != nil {
		return a, err
	}
	if err := one(`SELECT EXISTS (SELECT 1 FROM iam_v2.pms_interface_runtime WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid
		AND (last_connect_attempt_at IS NOT NULL OR last_connected_at IS NOT NULL OR last_complete_sync_at IS NOT NULL))`, &f.EverConnected); err != nil {
		return a, err
	}
	var activated int
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM public.audit_log WHERE action='pms_interface.lifecycle' AND target_id=$1
		AND payload->>'to'='ACTIVE'`, id).Scan(&activated); err != nil {
		return a, err
	}
	f.EverActivated = activated > 0

	var openWork int
	if err := one(`SELECT (SELECT count(*) FROM iam_v2.posting_outbox o WHERE o.tenant_id=$1 AND o.site_id=$2 AND o.pms_interface_id=$3::uuid AND o.state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY'))
		+ (SELECT count(*) FROM iam_v2.posting_attempts t WHERE t.tenant_id=$1 AND t.site_id=$2 AND t.pms_interface_id=$3::uuid AND t.outcome IN ('SENDING','UNKNOWN'))
		+ (SELECT count(*) FROM iam_v2.posting_execution_state x WHERE x.tenant_id=$1 AND x.site_id=$2 AND x.pms_interface_id=$3::uuid AND x.awaiting_manual_review)`, &openWork); err != nil {
		return a, err
	}

	archived := s.archivedSet(ctx)
	rows, err := tx.Query(ctx, `SELECT g.id::text, g.name, g.enabled FROM iam_v2.guest_network_pms_map m JOIN public.guest_networks g ON g.id = m.guest_network_id
		WHERE m.tenant_id=$1 AND m.site_id=$2 AND m.pms_interface_id=$3::uuid ORDER BY g.name`, s.tenantID, s.siteID, id)
	if err != nil {
		return a, err
	}
	var serving []removalNetwork
	for rows.Next() {
		var n removalNetwork
		var enabled bool
		if err := rows.Scan(&n.ID, &n.Name, &enabled); err != nil {
			rows.Close()
			return a, err
		}
		switch {
		case archived[n.ID]:
			n.State = "archived"
			a.StaleRouting = append(a.StaleRouting, n)
		case !enabled:
			n.State = "off"
			a.StaleRouting = append(a.StaleRouting, n)
		default:
			n.State = "serving"
			serving = append(serving, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return a, err
	}

	if a.Lifecycle != "AUTH_DISABLED" {
		a.Blockers = append(a.Blockers, removalBlocker{Code: "SWITCH_OFF_FIRST", Title: "Switch the connection off first",
			Detail: "It is " + strings.ToLower(strings.ReplaceAll(a.Lifecycle, "_", " ")) + ". Removing a connection that guests are being resolved against would stop room sign-in without warning. Switch it off from its connection page, then remove it."})
	}
	if len(serving) > 0 {
		a.Blockers = append(a.Blockers, removalBlocker{Code: "ROUTED", Title: "Networks still use this connection for room sign-in",
			Detail: "Change each network's room sign-in under PMS routing first. Nothing is reassigned for you.", Networks: serving})
	}
	if openWork > 0 {
		a.Blockers = append(a.Blockers, removalBlocker{Code: "OPEN_FINANCIAL_WORK", Title: "Financial work for this connection is still open",
			Detail: "A posting is queued, being sent, unanswered or waiting for review. Resolve it under Financial before removing the connection."})
	}
	if len(a.Blockers) > 0 {
		a.Outcome = removalBlocked
		return a, nil
	}

	a.Outcome = removalRetire
	if _, err := tx.Exec(ctx, `SAVEPOINT removal_dry_run`); err != nil {
		return a, err
	}
	_, derr := tx.Exec(ctx, `SELECT iam_v2.delete_unused_pms_interface($1::uuid,$2::uuid,$3::uuid)`, s.tenantID, s.siteID, id)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT removal_dry_run`); err != nil {
		return a, err
	}
	switch {
	case derr == nil && !f.EverActivated:
		a.Outcome = removalDelete
	case derr == nil:
	case !isUnusedRefusal(derr):
		return a, derr
	}

	if a.Outcome == removalDelete {
		a.Consequences = append(a.Consequences,
			"The connection and its saved drafts are deleted for good. It never connected to a PMS and nothing refers to it.",
			"The deletion is recorded in the audit trail.")
	} else {
		a.Consequences = append(a.Consequences,
			"The connection is retired: it cannot be switched on again, edited or published, and it disappears from the ordinary lists.",
			"Its history stays exactly as it is: stays, events, entitlements, postings, sign-in records and the audit trail.")
		if f.Stays > 0 {
			a.Consequences = append(a.Consequences, itoaWord(f.Stays, "stay")+" from this PMS stay in the records ("+itoaWord(f.InHouseStays, "currently in house")+"). They are not changed.")
		}
		if f.LiveEntitlements > 0 {
			a.Consequences = append(a.Consequences, itoaWord(f.LiveEntitlements, "guest access")+" granted through this connection continues until it ends on its own. No new room sign-in is possible through it.")
		}
		if len(a.StaleRouting) > 0 {
			a.Consequences = append(a.Consequences, "Networks that are switched off or archived still point at it; their routing is left as it is and shows the connection as retired.")
		}
	}
	return a, nil
}

func itoaWord(n int, noun string) string {
	if noun == "currently in house" {
		return strconv.Itoa(n) + " " + noun
	}
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func (s *server) previewPMSRemoval(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := s.assessRemoval(ctx, tx, chi.URLParam(r, "id"), false)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the removal could not be assessed")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type removePMSReq struct {
	Outcome    string `json:"outcome"`
	ReasonCode string `json:"reason_code"`
	Password   string `json:"password"`
}

func (s *server) removePMSInterface(w http.ResponseWriter, r *http.Request) {
	var in removePMSReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	in.Outcome = strings.ToUpper(strings.TrimSpace(in.Outcome))
	if in.Outcome != removalDelete && in.Outcome != removalRetire {
		jsonErr(w, http.StatusBadRequest, "validation", "outcome must be DELETE or RETIRE, as the removal preview said")
		return
	}
	if strings.TrimSpace(in.ReasonCode) == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason code is required to remove a connection")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "removal failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "removal failed")
		return
	}
	a, err := s.assessRemoval(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if isLockNotAvailable(err) {
		jsonErr(w, http.StatusConflict, "conflict", "the connection is being changed by someone else right now: try again in a moment")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the removal could not be assessed")
		return
	}
	if a.Outcome != in.Outcome {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "removal_changed",
			"message":    "What removing this connection would do has changed since you looked. Review it again.",
			"assessment": a})
		return
	}
	switch in.Outcome {
	case removalDelete:
		if _, err := tx.Exec(ctx, `SELECT iam_v2.delete_unused_pms_interface($1::uuid,$2::uuid,$3::uuid)`, s.tenantID, s.siteID, id); err != nil {
			jsonErr(w, http.StatusConflict, "removal_changed", "The connection is no longer unused, so it cannot be deleted. Review it again.")
			return
		}
	case removalRetire:
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET lifecycle_state='DECOMMISSIONED' WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`, s.tenantID, s.siteID, id); err != nil {
			jsonErr(w, http.StatusConflict, "removal_refused", "The connection could not be retired: financial work for it is still open.")
			return
		}
	}
	action := "pms_interface.retired"
	if in.Outcome == removalDelete {
		action = "pms_interface.deleted"
	}
	if err := s.auditErr(r, action, "pms_interface", id, map[string]any{
		"display_label": a.Label, "connector_kind": a.ConnectorKind, "reason_code": in.ReasonCode,
		"stays": a.Facts.Stays, "revisions": a.Facts.Revisions, "previous_lifecycle_state": a.Lifecycle,
	}); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the removal was not recorded, so it was not made")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "removal failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "outcome": in.Outcome})
}

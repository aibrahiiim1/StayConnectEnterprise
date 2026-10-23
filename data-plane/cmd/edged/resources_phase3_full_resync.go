package main

// Operator's end of a command channel: this handler only writes one row. The pmsd worker that owns the PMS
// socket claims it and raises the DR through its serialized writer, because edged writing to the link would mean
// two writers on one FIAS link. The row carries the runtime generation, so a command aimed at a replaced worker
// is refused at claim. The checks here only give the operator a real answer; the claim re-checks under the CAS.

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Bounded reason codes keep free text, and so guest data, out of a durable operations field.
var fullResyncReasons = map[string]bool{
	"SUSPECTED_STALE_GUEST_LIST": true,
	"AFTER_PMS_MAINTENANCE":      true,
	"OPERATOR_VERIFICATION":      true,
	"SUPPORT_REQUEST":            true,
}

type fullResyncReq struct {
	ReasonCode string `json:"reason_code"`
	Password   string `json:"password"`
}

func (s *server) requestFullResync(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS connection")
		return
	}
	var in fullResyncReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if !fullResyncReasons[strings.TrimSpace(in.ReasonCode)] {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason code is required: a full resync replaces the entire guest list")
		return
	}
	// Step-up as for publishing a revision: a fresh roster loads the hotel's PMS and changes what every sign-in resolves against.
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	// The write goes through iam_v2.request_full_resync, not a table: svc_edged must not write
	// iam_v2.pms_interface_runtime (transport/sync status, generation, pinned revision), which pmsd uses to prove
	// it owns a socket. The function returns NULL when no row qualifies; the reason is read back below.
	var cmdID *string
	err := s.db.QueryRow(ctx,
		`SELECT iam_v2.request_full_resync($1,$2,$3::uuid,$4)::text`,
		s.tenantID, s.siteID, id, strings.TrimSpace(in.ReasonCode)).Scan(&cmdID)

	if err == nil && cmdID == nil {
		// Nothing qualified: read the row back to tell the operator which rule stopped them.
		jsonErr(w, http.StatusConflict, "resync_not_possible", s.fullResyncRefusal(ctx, id))
		return
	}
	if err != nil {
		// Logged with the cause because the operator-facing message deliberately carries none (internal detail must not reach the screen).
		slog.Error("full resync request could not be recorded", "interface", id, "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "could not record the request")
		return
	}
	// Record the actor: the command row carries the reason but not the operator.
	s.audit(r, "pms_interface.full_resync_requested", "pms_interfaces", id,
		map[string]any{"reason_code": strings.TrimSpace(in.ReasonCode), "command_id": *cmdID})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_id": *cmdID,
		"stage":      "REQUESTING_FULL_SYNC",
		"note": "The request is recorded. The PMS connector that owns this interface will send it to the " +
			"property management system; watch the Synchronization section for progress.",
	})
}

// Names the first unmet precondition. A second query on purpose: the refusal is diagnostic text and must not depend on the write succeeding.
func (s *server) fullResyncRefusal(ctx context.Context, id string) string {
	var lifecycle, transport, sync string
	var pending bool
	if err := s.db.QueryRow(ctx, `
		SELECT pi.lifecycle_state, COALESCE(rt.transport_status,'UNKNOWN'), COALESCE(rt.sync_status,'UNKNOWN'),
		       rt.resync_command_id IS NOT NULL
		  FROM iam_v2.pms_interfaces pi
		  LEFT JOIN iam_v2.pms_interface_runtime rt
		         ON rt.tenant_id=pi.tenant_id AND rt.site_id=pi.site_id AND rt.pms_interface_id=pi.id
		 WHERE pi.tenant_id=$1 AND pi.site_id=$2 AND pi.id=$3::uuid`,
		s.tenantID, s.siteID, id).Scan(&lifecycle, &transport, &sync, &pending); err != nil {
		return "this interface cannot be resynchronized right now"
	}
	switch {
	case lifecycle != "ACTIVE":
		return "the PMS interface is not active"
	case transport != "CONNECTED":
		return "the PMS is not connected: a full resync can only be requested over a live connection"
	case sync == "RESYNC_IN_PROGRESS":
		return "a full synchronization is already running"
	case pending:
		return "a full synchronization has already been requested and is waiting for the PMS connector"
	default:
		return "this interface cannot be resynchronized right now"
	}
}

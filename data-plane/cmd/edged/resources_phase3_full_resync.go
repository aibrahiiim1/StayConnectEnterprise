package main

// FULL RESYNC NOW — the operator's end of a command channel whose other end is a different process.
//
// WHAT THIS HANDLER DOES NOT DO is the important part. It does not open a socket, it does not build a FIAS
// frame, and it does not talk to pmsd. It writes one row. The worker that owns the socket claims that row and
// raises the DR through the serialized writer it already owns, which is the only thing in the system
// permitted to emit a frame.
//
// That indirection is not ceremony. edged is a web process handling operator sessions; giving it a path to
// the PMS socket would mean two writers on one link, and FIAS has no way to interleave them safely. The row
// also survives the operator closing their laptop, and it carries the runtime generation it was issued
// against, so a command aimed at a worker that has since been replaced is refused by the claim rather than
// executed against a socket the operator never saw.
//
// WHY THE PRECONDITIONS ARE CHECKED HERE AND AGAIN AT CLAIM. The checks below exist to give the operator a
// real answer — "the PMS is disconnected" rather than a request that vanishes. They are NOT the safety
// boundary: between this check and the claim the transport can drop, ownership can change, or another resync
// can start. The claim re-checks all of it under the CAS, and the claim is what actually decides.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Bounded reason codes. A free-text reason from an operator would end up in a durable record and, eventually,
// on a screen; a closed set keeps both honest and keeps guest data out of an operations field by construction.
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

// requestFullResync serves POST /edge/v1/pms-interfaces/{id}/full-resync.
func (s *server) requestFullResync(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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
	// Step-up on the same terms as publishing a revision. Requesting a fresh roster is not destructive — the
	// current mirror keeps serving until a new generation publishes — but it does put load on the hotel's PMS
	// and changes what every subsequent guest resolves against.
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	// ONE STATEMENT, so the preconditions are evaluated against the row we write rather than against a row we
	// read a moment earlier. The WHERE clause carries every rule:
	//
	//   lifecycle ACTIVE + transport CONNECTED   there is a worker with a live socket to receive this
	//   sync_status <> RESYNC_IN_PROGRESS        a resync is not already running
	//   resync_command_id IS NULL                no earlier request is still waiting to be claimed
	//
	// resync_command_generation is set from the runtime's OWN runtime_generation in the same write, so the
	// command is bound to the worker that currently owns the interface without this process having to know
	// which generation that is.
	var cmdID string
	err := s.db.QueryRow(ctx, `
		UPDATE iam_v2.pms_interface_runtime rt
		   SET resync_command_id=gen_random_uuid(),
		       resync_command_requested_at=now(),
		       resync_command_reason=$4,
		       resync_command_generation=rt.runtime_generation,
		       resync_command_claimed_at=NULL,
		       sync_stage='REQUESTING_FULL_SYNC', sync_stage_at=now(), sync_failure_code=NULL,
		       updated_at=now()
		  FROM iam_v2.pms_interfaces pi
		 WHERE pi.tenant_id=rt.tenant_id AND pi.site_id=rt.site_id AND pi.id=rt.pms_interface_id
		   AND rt.tenant_id=$1 AND rt.site_id=$2 AND rt.pms_interface_id=$3::uuid
		   AND pi.lifecycle_state='ACTIVE'
		   AND rt.transport_status='CONNECTED'
		   AND rt.sync_status <> 'RESYNC_IN_PROGRESS'
		   AND rt.resync_command_id IS NULL
		RETURNING rt.resync_command_id::text`,
		s.tenantID, s.siteID, id, strings.TrimSpace(in.ReasonCode)).Scan(&cmdID)

	if errors.Is(err, pgx.ErrNoRows) {
		// The write matched nothing. Read the row back to tell the operator WHICH rule stopped them — a bare
		// "refused" on a button that looks like it should work is how an operator concludes the product is
		// broken and starts restarting services.
		jsonErr(w, http.StatusConflict, "resync_not_possible", s.fullResyncRefusal(ctx, id))
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "could not record the request")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_id": cmdID,
		"stage":      "REQUESTING_FULL_SYNC",
		"note": "The request is recorded. The PMS connector that owns this interface will send it to the " +
			"property management system; watch the Synchronization section for progress.",
	})
}

// fullResyncRefusal names the first unmet precondition, in the order an operator would check them.
//
// Deliberately a SECOND query rather than a value returned by the first: the refusal is diagnostic text, and
// computing it inside the write would mean the write had to succeed to explain why it failed.
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

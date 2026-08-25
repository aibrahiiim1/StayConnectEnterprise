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
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
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

	// THE WRITE GOES THROUGH A FUNCTION, NOT A TABLE.
	//
	// svc_edged holds no write privilege on iam_v2.pms_interface_runtime and must not: that row carries
	// transport_status, sync_status, runtime_generation and the pinned revision — the state a pmsd worker uses
	// to prove it still owns a socket. An admin process able to write any of it could invalidate ownership or
	// fake a connection. The first deployment of this handler failed with "permission denied" and that refusal
	// was correct; iam_v2.request_full_resync (migration 0055) is the narrow capability that replaced it.
	//
	// The function returns NULL rather than raising when no row qualifies, so a refusal is an ordinary result
	// here and the specific reason is read back below.
	var cmdID *string
	err := s.db.QueryRow(ctx,
		`SELECT iam_v2.request_full_resync($1,$2,$3::uuid,$4)::text`,
		s.tenantID, s.siteID, id, strings.TrimSpace(in.ReasonCode)).Scan(&cmdID)

	if err == nil && cmdID == nil {
		// Nothing qualified. Read the row back to tell the operator WHICH rule stopped them — a bare
		// "refused" on a button that looks like it should work is how an operator concludes the product is
		// broken and starts restarting services.
		jsonErr(w, http.StatusConflict, "resync_not_possible", s.fullResyncRefusal(ctx, id))
		return
	}
	if err != nil {
		// Logged with the cause, because the operator-facing message deliberately carries none: a database
		// error rendered into an admin screen is how internal detail escapes. Without this line the first
		// deployment of this handler produced a bare 500 with nothing to diagnose it by.
		slog.Error("full resync request could not be recorded", "interface", id, "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "could not record the request")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"command_id": *cmdID,
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

package main

// THE CROSS-PMS TRANSFER OPERATOR SURFACE.
//
// Four routes, and the shape is chosen to keep a staff-confirmed action from becoming a reflex:
//
//	GET  /review-signals   the AMBIGUOUS resolutions. A SIGNAL, labelled as one. It is read-only, it names no
//	                       transfer, and nothing downstream reads it — "the guest matched on two interfaces"
//	                       is a reason for somebody to look, never evidence of where they went.
//	POST /preview          what would happen, with no locks taken and nothing written. An operator who cannot
//	                       see which rooms are involved and how many devices are about to move is confirming
//	                       a sentence rather than a decision.
//	POST /execute          the transfer: RBAC, password step-up, a bounded reason, an audit row, and the
//	                       whole thing atomic.
//	GET  /                 the lineage that has already been recorded.
//
// The operator names BOTH Stays here, which is the opposite of the guest surface — and it is safe for the
// opposite reason. The caller is an authenticated operator acting under step-up, and every named Stay is
// re-verified against live PMS state INSIDE the transaction rather than trusted. A Stay that does not exist
// is refused; one that is not in house is refused; one on the same interface is refused as the room move it
// is.

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/transfer"
)

// transferGraceValidity is how long the destination grace entitlement lasts. It is a WINDOW rather than an
// open-ended grant: a transfer moves a guest so they stay online across the change, and the destination
// property's own authentication then applies like anyone else's.
const transferGraceValidity = 4 * time.Hour

func (s *server) stayTransfersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listStayTransfers)
	r.Get("/review-signals", s.listTransferReviewSignals)
	r.Post("/preview", s.previewStayTransfer)
	r.Post("/execute", s.executeStayTransfer)
	return r
}

// ---- the review signal -----------------------------------------------------

// reviewSignal deliberately carries no stay id. The rows are GROUPED by outcome and network, so no single
// stay characterises a group — and naming one would invite an operator to read it as "the guest who moved",
// which is precisely the inference this endpoint exists not to support.
type reviewSignal struct {
	ResolvedAt  string `json:"resolved_at"`
	OutcomeCode string `json:"outcome_code"`
	NetworkID   string `json:"guest_network_id"`
	Count       int    `json:"occurrences"`
}

// listTransferReviewSignals reports AMBIGUOUS multi-PMS resolutions.
//
// This is the only "detection" Phase 5 ships, and calling it detection would already be overstating it. An
// AMBIGUOUS outcome means the guest's evidence matched on more than one interface — which happens when a
// property genuinely has two PMS systems and the same name appears in both, and says nothing whatsoever
// about a guest having MOVED. It is surfaced so an operator can go and look. Nothing reads it to decide
// anything, and the transfer package does not import this file's data at all.
func (s *server) listTransferReviewSignals(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT to_char(max(ar.resolved_at) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       ar.outcome_code, ar.guest_network_id::text, count(*)::int
		  FROM iam_v2.auth_resolutions ar
		 WHERE ar.tenant_id=$1 AND ar.site_id=$2
		   AND ar.outcome_code LIKE 'AMBIGUOUS%'
		   AND ar.resolved_at > now() - interval '7 days'
		 GROUP BY ar.outcome_code, ar.guest_network_id
		 ORDER BY count(*) DESC
		 LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := []reviewSignal{}
	for rows.Next() {
		var v reviewSignal
		if err := rows.Scan(&v.ResolvedAt, &v.OutcomeCode, &v.NetworkID, &v.Count); err != nil {
			jsonErr(w, http.StatusInternalServerError, "scan_failed", err.Error())
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"signals": out,
		// Said in the payload because the screen must say it too, and because a future caller reading this
		// endpoint deserves to be told before they build on it.
		"notice": "These are ambiguous authentication outcomes, not transfers. They indicate that a guest's " +
			"details matched on more than one PMS interface and that somebody should look. They are never " +
			"evidence that a guest moved, and no transfer may be based on them.",
	})
}

// ---- lineage ---------------------------------------------------------------

type transferRow struct {
	ID              string `json:"id"`
	FromStay        string `json:"from_stay_id"`
	FromReservation string `json:"from_external_reservation_id"`
	FromRoom        string `json:"from_room"`
	ToStay          string `json:"to_stay_id"`
	ToReservation   string `json:"to_external_reservation_id"`
	ToRoom          string `json:"to_room"`
	Actor           string `json:"actor"`
	CreatedAt       string `json:"created_at"`
}

func (s *server) listStayTransfers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT et.id::text, et.from_stay_id::text, f.external_reservation_id, COALESCE(f.normalized_room_number,''),
		       et.to_stay_id::text, t.external_reservation_id, COALESCE(t.normalized_room_number,''),
		       et.actor::text, to_char(et.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')
		  FROM iam_v2.entitlement_transfers et
		  JOIN iam_v2.stays f ON f.tenant_id=et.tenant_id AND f.site_id=et.site_id AND f.id=et.from_stay_id
		  JOIN iam_v2.stays t ON t.tenant_id=et.tenant_id AND t.site_id=et.site_id AND t.id=et.to_stay_id
		 WHERE et.tenant_id=$1 AND et.site_id=$2
		 ORDER BY et.created_at DESC
		 LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := []transferRow{}
	for rows.Next() {
		var v transferRow
		if err := rows.Scan(&v.ID, &v.FromStay, &v.FromReservation, &v.FromRoom,
			&v.ToStay, &v.ToReservation, &v.ToRoom, &v.Actor, &v.CreatedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "scan_failed", err.Error())
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"transfers": out})
}

// ---- preview + execute -----------------------------------------------------

type transferPreviewReq struct {
	FromStayID string `json:"from_stay_id"`
	ToStayID   string `json:"to_stay_id"`
}

func (s *server) previewStayTransfer(w http.ResponseWriter, r *http.Request) {
	var in transferPreviewReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if in.FromStayID == "" || in.ToStayID == "" {
		jsonErr(w, http.StatusBadRequest, "invalid", "both stays are required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	p, err := transfer.New(s.db).PreviewTransfer(ctx, s.tenantID, s.siteID, in.FromStayID, in.ToStayID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "preview_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type transferExecuteReq struct {
	FromStayID string `json:"from_stay_id"`
	ToStayID   string `json:"to_stay_id"`
	// Password is the step-up. There is no actor field: the operator comes from the SESSION.
	Password string `json:"password"`
	Reason   string `json:"reason"`
}

func (s *server) executeStayTransfer(w http.ResponseWriter, r *http.Request) {
	var in transferExecuteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if in.FromStayID == "" || in.ToStayID == "" {
		jsonErr(w, http.StatusBadRequest, "invalid", "both stays are required")
		return
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason (4-500 characters) is required")
		return
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	out, err := transfer.New(s.db).Execute(ctx, transfer.Request{
		Tenant: s.tenantID, Site: s.siteID,
		FromStay: in.FromStayID, ToStay: in.ToStayID,
		Operator: actor, Reason: in.Reason, GraceValidFor: transferGraceValidity,
	})
	if err != nil {
		transferOpErr(w, err)
		return
	}
	s.audit(r, "stay.cross_pms_transfer", "stay", in.FromStayID, map[string]any{
		"reason": in.Reason, "to_stay_id": in.ToStayID, "transfer_id": out.TransferID,
		"devices_rebound": out.DevicesRebound, "sessions_rebound": out.SessionsRebound,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"transfer_id": out.TransferID, "to_entitlement_id": out.ToEntitlement,
		"devices_rebound": out.DevicesRebound, "sessions_rebound": out.SessionsRebound,
		"window_ends_at": out.WindowEnds.UTC().Format(time.RFC3339),
	})
}

// transferOpErr maps the package's typed refusals onto HTTP.
//
// Every one of these is a CONFLICT rather than a server fault: the operator asked for something the current
// state does not permit, and the message says which, because an operator who cannot tell "that guest has
// already moved" from "the other property has not checked them in yet" cannot do anything about either.
func transferOpErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, transfer.ErrNotAuthorized):
		jsonErr(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, transfer.ErrSameInterface):
		jsonErr(w, http.StatusConflict, "room_move_not_transfer", err.Error())
	case errors.Is(err, transfer.ErrDestinationMissing):
		jsonErr(w, http.StatusConflict, "destination_missing", err.Error())
	case errors.Is(err, transfer.ErrDestinationNotEligible):
		jsonErr(w, http.StatusConflict, "destination_not_in_house", err.Error())
	case errors.Is(err, transfer.ErrDestinationOccupied):
		jsonErr(w, http.StatusConflict, "destination_occupied", err.Error())
	case errors.Is(err, transfer.ErrSourceNotTransferable):
		jsonErr(w, http.StatusConflict, "source_not_transferable", err.Error())
	case errors.Is(err, transfer.ErrAlreadyTransferred):
		jsonErr(w, http.StatusConflict, "already_transferred", err.Error())
	case errors.Is(err, transfer.ErrNoGracePackage):
		jsonErr(w, http.StatusConflict, "no_landing_package", err.Error())
	default:
		jsonErr(w, http.StatusInternalServerError, "transfer_failed", err.Error())
	}
}

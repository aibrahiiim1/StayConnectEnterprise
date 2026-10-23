package main

// Routes: review-signals (read-only), preview (no locks, nothing written), execute (RBAC, password step-up,
// bounded reason, audit row, atomic) and the recorded lineage. review-signals are AMBIGUOUS resolutions, a reason
// to look, never evidence of where a guest went. The operator names both Stays; each is re-verified against live
// PMS state inside the transaction (missing, not in house, or same-interface is refused).

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/transfer"
)

// A window, not an open-ended grant; the destination property's own authentication then applies.
const transferGraceValidity = 4 * time.Hour

func (s *server) stayTransfersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listStayTransfers)
	r.Get("/review-signals", s.listTransferReviewSignals)
	r.Post("/preview", s.previewStayTransfer)
	r.Post("/execute", s.executeStayTransfer)
	return r
}

// Deliberately carries no stay id: rows are grouped by outcome and network, and naming one would invite reading it as "the guest who moved".
type reviewSignal struct {
	ResolvedAt  string `json:"resolved_at"`
	OutcomeCode string `json:"outcome_code"`
	NetworkID   string `json:"guest_network_id"`
	Count       int    `json:"occurrences"`
}

// AMBIGUOUS means the guest matched on more than one interface; it says nothing about a guest having moved.
// Nothing reads it to decide anything.
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
		slog.Error("stay transfer: review signals unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the review signals could not be read")
		return
	}
	defer rows.Close()
	out := []reviewSignal{}
	for rows.Next() {
		var v reviewSignal
		if err := rows.Scan(&v.ResolvedAt, &v.OutcomeCode, &v.NetworkID, &v.Count); err != nil {
			jsonErr(w, http.StatusInternalServerError, "scan_failed", "the review signals could not be read")
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"signals": out,
		"notice": "These are ambiguous authentication outcomes, not transfers. They indicate that a guest's " +
			"details matched on more than one PMS interface and that somebody should look. They are never " +
			"evidence that a guest moved, and no transfer may be based on them.",
	})
}

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
	identity := canSeeStayIdentity(r)
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
		slog.Error("stay transfer: lineage unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the transfers could not be read")
		return
	}
	defer rows.Close()
	out := []transferRow{}
	for rows.Next() {
		var v transferRow
		if err := rows.Scan(&v.ID, &v.FromStay, &v.FromReservation, &v.FromRoom,
			&v.ToStay, &v.ToReservation, &v.ToRoom, &v.Actor, &v.CreatedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "scan_failed", "the transfers could not be read")
			return
		}
		if !identity {
			v.FromReservation, v.ToReservation = "", "" // audit H-7: a viewer sees rooms, not reservation numbers
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"transfers": out})
}

type transferPreviewReq struct {
	FromStayID      string `json:"from_stay_id"`
	ToStayID        string `json:"to_stay_id"`
	FromInterfaceID string `json:"from_pms_interface_id"`
	ToInterfaceID   string `json:"to_pms_interface_id"`
}

var errStayRefNotFound = errors.New("no stay on this property has that reference")
var errStayRefAmbiguous = errors.New("that reference names more than one stay; choose the PMS connection")

func (s *server) resolveStayRef(ctx context.Context, ref, iface string) (string, error) {
	ref = strings.TrimSpace(ref)
	iface = strings.TrimSpace(iface)
	if ref == "" {
		return "", errStayRefNotFound
	}
	if iface != "" && !uuidRe.MatchString(iface) {
		return "", errStayRefNotFound
	}
	var ifaceArg any
	if iface != "" {
		ifaceArg = iface
	}
	if uuidRe.MatchString(ref) {
		// Passed unchanged: the transfer rules re-verify inside the transaction and report a missing destination as a conflict.
		return ref, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM iam_v2.stays
		WHERE tenant_id=$1 AND site_id=$2 AND btrim(external_reservation_id)=$3
		  AND ($4::uuid IS NULL OR pms_interface_id=$4::uuid)
		ORDER BY (status='IN_HOUSE') DESC
		LIMIT 3`, s.tenantID, s.siteID, ref, ifaceArg)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	var inHouse int
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", errStayRefNotFound
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	// More than one stay carries the number (two connections, or a reused one): prefer the single IN_HOUSE one, else ambiguous.
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.stays
		WHERE tenant_id=$1 AND site_id=$2 AND btrim(external_reservation_id)=$3 AND status='IN_HOUSE'
		  AND ($4::uuid IS NULL OR pms_interface_id=$4::uuid)`, s.tenantID, s.siteID, ref, ifaceArg).Scan(&inHouse)
	if inHouse == 1 {
		return ids[0], nil
	}
	return "", errStayRefAmbiguous
}

func (s *server) resolveTransferPair(ctx context.Context, w http.ResponseWriter, from, fromIface, to, toIface string) (string, string, bool) {
	fromID, err := s.resolveStayRef(ctx, from, fromIface)
	if err != nil {
		stayRefErr(w, "from", err)
		return "", "", false
	}
	toID, err := s.resolveStayRef(ctx, to, toIface)
	if err != nil {
		stayRefErr(w, "to", err)
		return "", "", false
	}
	return fromID, toID, true
}

func stayRefErr(w http.ResponseWriter, side string, err error) {
	switch {
	case errors.Is(err, errStayRefNotFound):
		jsonErr(w, http.StatusNotFound, side+"_stay_not_found", "the "+side+" stay: "+err.Error())
	case errors.Is(err, errStayRefAmbiguous):
		jsonErr(w, http.StatusConflict, side+"_stay_ambiguous", "the "+side+" stay: "+err.Error())
	default:
		slog.Error("stay transfer: reference lookup failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the stays could not be looked up")
	}
}

func (s *server) previewStayTransfer(w http.ResponseWriter, r *http.Request) {
	var in transferPreviewReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if strings.TrimSpace(in.FromStayID) == "" || strings.TrimSpace(in.ToStayID) == "" {
		jsonErr(w, http.StatusBadRequest, "invalid", "both stays are required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	fromID, toID, ok := s.resolveTransferPair(ctx, w, in.FromStayID, in.FromInterfaceID, in.ToStayID, in.ToInterfaceID)
	if !ok {
		return
	}
	p, err := transfer.New(s.db).PreviewTransfer(ctx, s.tenantID, s.siteID, fromID, toID)
	if err != nil {
		transferOpErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type transferExecuteReq struct {
	FromStayID      string `json:"from_stay_id"`
	ToStayID        string `json:"to_stay_id"`
	FromInterfaceID string `json:"from_pms_interface_id"`
	ToInterfaceID   string `json:"to_pms_interface_id"`
	// Password is the step-up. There is no actor field: the operator comes from the session.
	Password string `json:"password"`
	Reason   string `json:"reason"`
}

func (s *server) executeStayTransfer(w http.ResponseWriter, r *http.Request) {
	var in transferExecuteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if strings.TrimSpace(in.FromStayID) == "" || strings.TrimSpace(in.ToStayID) == "" {
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
	fromID, toID, ok := s.resolveTransferPair(ctx, w, in.FromStayID, in.FromInterfaceID, in.ToStayID, in.ToInterfaceID)
	if !ok {
		return
	}
	in.FromStayID, in.ToStayID = fromID, toID
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

// Every refusal is a conflict, not a server fault, and the message says which so the operator can act on it.
func transferOpErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, transfer.ErrNotAuthorized):
		jsonErr(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, transfer.ErrSameInterface):
		jsonErr(w, http.StatusConflict, "room_move_not_transfer", err.Error())
	case errors.Is(err, transfer.ErrDestinationMissing):
		jsonErr(w, http.StatusConflict, "destination_missing", err.Error())
	case errors.Is(err, transfer.ErrSourceNotInHouse):
		jsonErr(w, http.StatusConflict, "source_checked_out", err.Error())
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
		// Logged with the cause; the operator is not shown a database message.
		slog.Error("stay transfer failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "transfer_failed", "the transfer could not be completed")
	}
}

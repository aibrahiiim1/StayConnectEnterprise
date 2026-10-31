// Package transfer is the staff-confirmed, atomic Cross-PMS Transfer of a guest's live access to a Stay on
// another PMS interface. It is not a room move (same interface), not a supersession (same subject), is never
// inferred (it must not read auth_resolutions; an AMBIGUOUS match is only a review signal), and never creates
// a Stay (the destination must already exist from verified PMS state). It moves no money.
package transfer

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CapEntitlementTransfer is the controlled-operation family that owns writes to entitlement_transfers and stay_links.
const CapEntitlementTransfer = "entitlement_transfer"

var (
	ErrSourceNotTransferable = errors.New("transfer: the source stay has no transferable live access")
	// ErrDestinationMissing keeps the operation from inventing a Stay: the operator must wait for it to arrive.
	ErrDestinationMissing = errors.New("transfer: the destination stay does not exist yet from verified PMS state")
	// ErrSourceNotInHouse: a checked-out guest holds checkout grace, a courtesy of the property they left that
	// must not extend across properties.
	ErrSourceNotInHouse       = errors.New("transfer: the source stay has already checked out; grace access is not transferable")
	ErrDestinationNotEligible = errors.New("transfer: the destination stay is not currently in house")
	// ErrSameInterface: both Stays on one interface is a room move.
	ErrSameInterface = errors.New("transfer: both stays are on the same PMS interface; this is a room move, not a transfer")
	// ErrDestinationOccupied: landing on live access would be a cross-subject supersession.
	ErrDestinationOccupied = errors.New("transfer: the destination stay already holds live access")
	// ErrAlreadyTransferred makes a repeat attempt return an answer instead of a constraint violation.
	ErrAlreadyTransferred = errors.New("transfer: this access has already been transferred")
	// ErrNoGracePackage fails closed: existing access is never terminated without somewhere to land the guest.
	ErrNoGracePackage = errors.New("transfer: no eligible no-posting package at the destination")
	ErrNotAuthorized  = errors.New("transfer: an operator and a bounded reason are required")
)

// errPreviewUnavailable is the blocker shown when a preview check could not be read.
const errPreviewUnavailable = "transfer: the transfer could not be checked right now; try again"

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Request names both Stays explicitly; that is safe because the caller is a step-up authenticated operator
// and each Stay is re-verified against live PMS state inside the transaction.
type Request struct {
	Tenant, Site  string
	FromStay      string
	ToStay        string
	Operator      string
	Reason        string
	GraceValidFor time.Duration
}

type Result struct {
	TransferID      string
	FromEntitlement string
	ToEntitlement   string
	ToPackage       string
	DevicesRebound  int
	SessionsRebound int
	WindowEnds      time.Time
}

// Preview is shown to the operator before they confirm, since the source access ends irreversibly.
type Preview struct {
	FromStay        string `json:"from_stay_id"`
	FromReservation string `json:"from_external_reservation_id"`
	FromRoom        string `json:"from_room"`
	FromInterface   string `json:"from_pms_interface_id"`
	ToStay          string `json:"to_stay_id"`
	ToReservation   string `json:"to_external_reservation_id"`
	ToRoom          string `json:"to_room"`
	ToInterface     string `json:"to_pms_interface_id"`
	LiveDevices     int    `json:"live_devices"`
	LiveSessions    int    `json:"live_sessions"`
	// Blocker is why the transfer would be refused, or empty; shown before the operator types a reason.
	Blocker string `json:"blocker,omitempty"`
}

// PreviewTransfer evaluates the transfer without performing it. It takes no locks, so Execute re-checks
// everything under lock.
func (s *Store) PreviewTransfer(ctx context.Context, tenant, site, fromStay, toStay string) (Preview, error) {
	var p Preview
	p.FromStay, p.ToStay = fromStay, toStay
	err := s.pool.QueryRow(ctx, `
		SELECT f.external_reservation_id, COALESCE(f.normalized_room_number,''), f.pms_interface_id::text,
		       t.external_reservation_id, COALESCE(t.normalized_room_number,''), t.pms_interface_id::text
		  FROM iam_v2.stays f, iam_v2.stays t
		 WHERE f.tenant_id=$1 AND f.site_id=$2 AND f.id=$3
		   AND t.tenant_id=$1 AND t.site_id=$2 AND t.id=$4`,
		tenant, site, fromStay, toStay).
		Scan(&p.FromReservation, &p.FromRoom, &p.FromInterface, &p.ToReservation, &p.ToRoom, &p.ToInterface)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// One message for a missing source or destination, so another property's reservations are not enumerable.
			p.Blocker = ErrDestinationMissing.Error()
			return p, nil
		}
		return p, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM iam_v2.entitlement_devices ed
		         JOIN iam_v2.entitlements e ON e.id=ed.entitlement_id
		        WHERE e.stay_id=$1 AND e.status IN ('PENDING','ACTIVE','SUSPENDED') AND ed.status='AUTHORIZED'),
		       (SELECT count(*) FROM iam_v2.sessions se
		         JOIN iam_v2.entitlements e ON e.id=se.entitlement_id
		        WHERE e.stay_id=$1 AND se.state='active')`, fromStay).
		Scan(&p.LiveDevices, &p.LiveSessions); err != nil {
		return p, err
	}
	p.Blocker = s.blocker(ctx, tenant, site, fromStay, toStay, p.FromInterface, p.ToInterface)
	return p, nil
}

// blocker returns the first refusal reason, structural impossibilities before transient ones.
func (s *Store) blocker(ctx context.Context, tenant, site, fromStay, toStay, fromIface, toIface string) string {
	if fromIface == toIface {
		return ErrSameInterface.Error()
	}
	var fromStatus, toStatus string
	if err := s.pool.QueryRow(ctx, `SELECT f.status, t.status FROM iam_v2.stays f, iam_v2.stays t
		WHERE f.tenant_id=$1 AND f.site_id=$2 AND f.id=$3
		  AND t.tenant_id=$1 AND t.site_id=$2 AND t.id=$4`,
		tenant, site, fromStay, toStay).Scan(&fromStatus, &toStatus); err != nil {
		return ErrDestinationMissing.Error()
	}
	if fromStatus != "IN_HOUSE" {
		return ErrSourceNotInHouse.Error()
	}
	if toStatus != "IN_HOUSE" {
		return ErrDestinationNotEligible.Error()
	}
	var liveSource, liveDest int
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM iam_v2.entitlements
		         WHERE tenant_id=$1 AND site_id=$2 AND stay_id=$3 AND status IN ('PENDING','ACTIVE','SUSPENDED')),
		       (SELECT count(*) FROM iam_v2.entitlements
		         WHERE tenant_id=$1 AND site_id=$2 AND stay_id=$4 AND status IN ('PENDING','ACTIVE','SUSPENDED'))`,
		tenant, site, fromStay, toStay).Scan(&liveSource, &liveDest); err != nil {
		// Audit P6 L11: a fixed message for the operator; the database's text goes to the log only.
		slog.Warn("transfer preview: live access could not be counted", "err", err)
		return errPreviewUnavailable
	}
	if liveSource == 0 {
		return ErrSourceNotTransferable.Error()
	}
	if liveDest > 0 {
		return ErrDestinationOccupied.Error()
	}
	return ""
}

// Execute performs the transfer by calling iam_v2.cross_pms_transfer (migration 0201): one SECURITY DEFINER function that runs the whole
// operation in one transaction, with the lock order (both Stays in id order, then the source Entitlement) and every precondition the
// preview checked re-checked under the locks. The Hotel Admin login holds no table privilege for any of the writes; executing this one
// function is all it may do. The function's refusals arrive as 'XFER_<CODE>: ...' and map back to the errors below.
func (s *Store) Execute(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Operator) == "" || len(strings.TrimSpace(req.Reason)) < 4 {
		return Result{}, ErrNotAuthorized
	}
	if req.GraceValidFor <= 0 || req.GraceValidFor > 7*24*time.Hour {
		return Result{}, ErrNotAuthorized
	}
	var out Result
	err := s.pool.QueryRow(ctx, `SELECT transfer_id::text, from_entitlement::text, to_entitlement::text, to_package::text,
		devices_rebound, sessions_rebound, window_ends
		  FROM iam_v2.cross_pms_transfer($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7::int)`,
		req.Tenant, req.Site, req.FromStay, req.ToStay, req.Operator, req.Reason, int(req.GraceValidFor.Seconds())).
		Scan(&out.TransferID, &out.FromEntitlement, &out.ToEntitlement, &out.ToPackage,
			&out.DevicesRebound, &out.SessionsRebound, &out.WindowEnds)
	if err != nil {
		return Result{}, mapTransferError(err)
	}
	return out, nil
}

// mapTransferError turns the function's coded refusals back into the package's errors. Anything else is returned as it came.
func mapTransferError(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	for code, mapped := range map[string]error{
		"XFER_NOT_AUTHORIZED":           ErrNotAuthorized,
		"XFER_DESTINATION_MISSING":      ErrDestinationMissing,
		"XFER_SAME_INTERFACE":           ErrSameInterface,
		"XFER_SOURCE_NOT_IN_HOUSE":      ErrSourceNotInHouse,
		"XFER_DESTINATION_NOT_ELIGIBLE": ErrDestinationNotEligible,
		"XFER_SOURCE_NOT_TRANSFERABLE":  ErrSourceNotTransferable,
		"XFER_ALREADY_TRANSFERRED":      ErrAlreadyTransferred,
		"XFER_DESTINATION_OCCUPIED":     ErrDestinationOccupied,
		"XFER_NO_LANDING_PACKAGE":       ErrNoGracePackage,
	} {
		if strings.HasPrefix(pe.Message, code+":") {
			return mapped
		}
	}
	return err
}

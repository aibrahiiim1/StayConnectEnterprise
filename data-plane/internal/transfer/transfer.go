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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
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

// Execute performs the transfer in one transaction. Lock order is L1 Stay before L3 Entitlement, and both
// Stays are locked in id order first: caller order would deadlock two opposite-direction transfers between
// the same pair. Everything the preview checked is re-checked under the locks.
func (s *Store) Execute(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Operator) == "" || len(strings.TrimSpace(req.Reason)) < 4 {
		return Result{}, ErrNotAuthorized
	}
	if req.GraceValidFor <= 0 || req.GraceValidFor > 7*24*time.Hour {
		return Result{}, ErrNotAuthorized
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	first, second := req.FromStay, req.ToStay
	if first > second {
		first, second = second, first
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT id FROM iam_v2.stays WHERE tenant_id=$1 AND site_id=$2 AND id IN ($3,$4)
		 ORDER BY id FOR UPDATE) locked`, req.Tenant, req.Site, first, second).Scan(&n); err != nil {
		return Result{}, err
	}
	if n != 2 {
		return Result{}, ErrDestinationMissing
	}

	var fromIface, toIface, fromStatus, toStatus string
	if err := tx.QueryRow(ctx, `SELECT f.pms_interface_id::text, t.pms_interface_id::text, f.status, t.status
		  FROM iam_v2.stays f, iam_v2.stays t
		 WHERE f.tenant_id=$1 AND f.site_id=$2 AND f.id=$3
		   AND t.tenant_id=$1 AND t.site_id=$2 AND t.id=$4`,
		req.Tenant, req.Site, req.FromStay, req.ToStay).
		Scan(&fromIface, &toIface, &fromStatus, &toStatus); err != nil {
		return Result{}, ErrDestinationMissing
	}
	if fromIface == toIface {
		return Result{}, ErrSameInterface
	}
	// Under the lock: a checkout that committed first leaves grace, which must not cross properties; and the
	// destination may have checked out since the preview.
	if fromStatus != "IN_HOUSE" {
		return Result{}, ErrSourceNotInHouse
	}
	if toStatus != "IN_HOUSE" {
		return Result{}, ErrDestinationNotEligible
	}

	var fromEnt string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.entitlements
		 WHERE tenant_id=$1 AND site_id=$2 AND stay_id=$3 AND status IN ('PENDING','ACTIVE','SUSPENDED')
		 FOR UPDATE`, req.Tenant, req.Site, req.FromStay).Scan(&fromEnt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrSourceNotTransferable
		}
		return Result{}, err
	}
	var already int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_transfers
		WHERE from_entitlement_id=$1`, fromEnt).Scan(&already); err != nil {
		return Result{}, err
	}
	if already > 0 {
		return Result{}, ErrAlreadyTransferred
	}
	var destLive int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements
		 WHERE tenant_id=$1 AND site_id=$2 AND stay_id=$3 AND status IN ('PENDING','ACTIVE','SUSPENDED')`,
		req.Tenant, req.Site, req.ToStay).Scan(&destLive); err != nil {
		return Result{}, err
	}
	if destLive > 0 {
		return Result{}, ErrDestinationOccupied
	}

	// The landing package: zero-price, no-posting, chosen inside the transaction.
	var pkgRev, planRev string
	if err := tx.QueryRow(ctx, `
		SELECT ipr.id::text, ipr.service_plan_revision_id::text
		  FROM iam_v2.internet_package_revisions ipr
		  JOIN iam_v2.internet_packages ip
		    ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		 WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ip.active
		   AND ip.current_revision_id = ipr.id
		   AND ipr.price_minor = 0
		   AND ipr.settlement_methods = ARRAY['NOT_REQUIRED']::text[]
		   AND ipr.package_type IN ('CHECKOUT_GRACE','FREE_STAY','GENERAL')
		 ORDER BY CASE ipr.package_type WHEN 'CHECKOUT_GRACE' THEN 0 WHEN 'FREE_STAY' THEN 1 ELSE 2 END,
		          ip.code, ipr.id -- audit P6 L11: a deterministic choice among equals
		 LIMIT 1`, req.Tenant, req.Site).Scan(&pkgRev, &planRev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrNoGracePackage
		}
		return Result{}, err
	}

	// The database requires TERMINATED(TRANSFERRED) before it records the lineage row.
	if _, err := tx.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now(),'TRANSFERRED')`, fromEnt); err != nil {
		return Result{}, err
	}

	if err := writerguard.Open(ctx, tx, "commerce_intent"); err != nil {
		return Result{}, err
	}
	var purchase string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, stay_id, trigger, amount_minor, state)
		VALUES ($1,$2,$3,$4,'CROSS_PMS_TRANSFER',0,'GRANTED') RETURNING id::text`,
		req.Tenant, req.Site, pkgRev, req.ToStay).Scan(&purchase); err != nil {
		return Result{}, err
	}
	var toEnt string
	var windowEnds time.Time
	// supersedes_entitlement_id must stay unset: a transfer crosses subjects, a supersession does not, and
	// the guard refuses both claims on one row.
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, stay_id, purchase_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, window_ends_at, status)
		VALUES ($1,$2,$3,$4,'{}'::jsonb,$5,$6,'VALIDITY_WINDOW','VALIDITY_WINDOW',
		        now() + make_interval(secs => $7),'PENDING')
		RETURNING id::text, window_ends_at`,
		req.Tenant, req.Site, req.ToStay, purchase, planRev, pkgRev,
		req.GraceValidFor.Seconds()).Scan(&toEnt, &windowEnds); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),NULL)`, toEnt); err != nil {
		return Result{}, err
	}

	// Devices and live sessions rebind in place (no logout, no nft churn: enforcement is keyed on the
	// session). entitlement_device_authorizations is an append-only interval history that accounting
	// attribution reads, so the source's open intervals must close at the same instant the destination's open.
	movedAt := time.Now().UTC()
	devices, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id, site_id, entitlement_id, device_id, status, grandfathered, first_authorized, last_authorized)
		SELECT $1,$2,$3, ed.device_id, 'AUTHORIZED', ed.grandfathered, ed.first_authorized, $5
		  FROM iam_v2.entitlement_devices ed
		 WHERE ed.entitlement_id=$4 AND ed.status='AUTHORIZED'`,
		req.Tenant, req.Site, toEnt, fromEnt, movedAt)
	if err != nil {
		return Result{}, err
	}
	if err := writerguard.Open(ctx, tx, "device_auth"); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id, site_id, entitlement_id, device_id, seq, authorized_at)
		SELECT $1,$2,$3, ed.device_id, 1, $4
		  FROM iam_v2.entitlement_devices ed WHERE ed.entitlement_id=$3`,
		req.Tenant, req.Site, toEnt, movedAt); err != nil {
		return Result{}, err
	}
	// GREATEST keeps an interval from ending before it began.
	if _, err := tx.Exec(ctx, `WITH closed AS (
		UPDATE iam_v2.entitlement_device_authorizations a
		   SET deauthorized_at = GREATEST($2::timestamptz, a.authorized_at)
		 WHERE a.entitlement_id=$1 AND a.deauthorized_at IS NULL
		 RETURNING a.entitlement_id, a.device_id)
		UPDATE iam_v2.entitlement_devices ed
		   SET status='DISCONNECTED', disconnected_reason='CROSS_PMS_TRANSFER'
		  FROM closed WHERE ed.entitlement_id=closed.entitlement_id AND ed.device_id=closed.device_id`,
		fromEnt, movedAt); err != nil {
		return Result{}, err
	}
	sessions, err := tx.Exec(ctx, `UPDATE iam_v2.sessions
		SET entitlement_id=$1 WHERE entitlement_id=$2 AND state='active'`, toEnt, fromEnt)
	if err != nil {
		return Result{}, err
	}

	// The database refuses both lineage rows unless everything above holds.
	if err := writerguard.OpenPhase5(ctx, tx, CapEntitlementTransfer); err != nil {
		return Result{}, err
	}
	var transferID string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlement_transfers
		(tenant_id, site_id, from_entitlement_id, to_entitlement_id, from_stay_id, to_stay_id, actor)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`,
		req.Tenant, req.Site, fromEnt, toEnt, req.FromStay, req.ToStay, req.Operator).Scan(&transferID); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_links
		(tenant_id, site_id, from_stay, to_stay, reason)
		VALUES ($1,$2,$3,$4,'CROSS_PMS_TRANSFER') ON CONFLICT DO NOTHING`,
		req.Tenant, req.Site, req.FromStay, req.ToStay); err != nil {
		return Result{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return Result{
		TransferID: transferID, FromEntitlement: fromEnt, ToEntitlement: toEnt, ToPackage: pkgRev,
		DevicesRebound: int(devices.RowsAffected()), SessionsRebound: int(sessions.RowsAffected()),
		WindowEnds: windowEnds,
	}, nil
}

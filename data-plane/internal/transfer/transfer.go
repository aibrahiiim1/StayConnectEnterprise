// Package transfer is the Cross-PMS Transfer: a staff-confirmed, atomic, typed movement of a guest's live
// access from a Stay on one PMS interface to a Stay on another.
//
// WHAT THIS IS NOT, and the distinctions are the design:
//
//	NOT A ROOM MOVE.       A room move is the same Stay changing rooms on the SAME interface, and Phase 3
//	                       already handles it by preserving the entitlement outright. Two Stays on one
//	                       interface satisfy "two different Stays", which is why the database now requires two
//	                       different INTERFACES before it will record a transfer at all.
//	NOT A SUPERSESSION.    Supersession is same-subject: one entitlement replacing another for the SAME Stay.
//	                       A transfer crosses subjects, which the Phase-1A engine rejects outright for
//	                       supersession — that rejection is precisely why transfers need their own typed
//	                       relationship rather than a pointer.
//	NOT INFERRED.          A multi-PMS AMBIGUOUS resolution is a REVIEW SIGNAL and nothing else. No code path
//	                       in this package reads auth_resolutions, and none may: "the guest matched on two
//	                       interfaces" is evidence that somebody should look, never evidence of where they
//	                       went.
//	NOT CREATIVE.          The destination Stay must ALREADY EXIST from verified PMS state. This package
//	                       never creates a Stay, never invents a reservation and never accepts one from a
//	                       caller who claims it is about to exist.
//
// It moves no money: the destination entitlement is a zero-price no-posting grace grant, and the destination
// Stay's posting permission is untouched by this operation.
package transfer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// CapEntitlementTransfer is the Phase-5 controlled-operation family that owns writes to
// entitlement_transfers and stay_links.
const CapEntitlementTransfer = "entitlement_transfer"

var (
	// ErrSourceNotTransferable — the source Stay has no live entitlement to move, or the Stay itself is not
	// in a state a guest can be moved out of.
	ErrSourceNotTransferable = errors.New("transfer: the source stay has no transferable live access")
	// ErrDestinationMissing — the destination Stay does not exist in this scope from verified PMS state.
	// This is the refusal that keeps the operation from inventing a Stay: an operator who knows the guest has
	// checked in on the other property must wait for that Stay to arrive.
	ErrDestinationMissing = errors.New("transfer: the destination stay does not exist yet from verified PMS state")
	// ErrSourceNotInHouse — the source Stay has already checked out. What such a guest holds is CHECKOUT
	// GRACE, a courtesy window granted by the property they left; moving it to another property would extend
	// that courtesy across a boundary nobody authorized. The destination authenticates them normally instead.
	ErrSourceNotInHouse = errors.New("transfer: the source stay has already checked out; grace access is not transferable")
	// ErrDestinationNotEligible — the destination Stay exists but is not IN_HOUSE, so there is nobody there
	// to give access to.
	ErrDestinationNotEligible = errors.New("transfer: the destination stay is not currently in house")
	// ErrSameInterface — both Stays are on one PMS interface. That is a room move, and Phase 3 owns it.
	ErrSameInterface = errors.New("transfer: both stays are on the same PMS interface; this is a room move, not a transfer")
	// ErrDestinationOccupied — the destination Stay already holds live access. Transferring onto it would
	// have to supersede that access, and supersession across subjects is exactly what a transfer is not.
	ErrDestinationOccupied = errors.New("transfer: the destination stay already holds live access")
	// ErrAlreadyTransferred — this source entitlement has already moved. The operation is idempotent by
	// refusal: a second attempt gets an answer it can act on rather than a constraint violation.
	ErrAlreadyTransferred = errors.New("transfer: this access has already been transferred")
	// ErrNoGracePackage — the destination site has no usable no-posting grace package to land the guest on.
	// Fail closed: a transfer that cannot land the guest anywhere must not terminate their existing access.
	ErrNoGracePackage = errors.New("transfer: no eligible no-posting package at the destination")
	// ErrNotAuthorized — the operation was called without a real operator or a bounded reason.
	ErrNotAuthorized = errors.New("transfer: an operator and a bounded reason are required")
)

// Store performs transfers.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Request is a staff-confirmed transfer. BOTH Stays are named explicitly by the operator: this is the one
// place in Phase 5 where a caller supplies subjects, and it is safe precisely because the caller is an
// authenticated operator acting under step-up, and because every named Stay is re-verified against live PMS
// state inside the transaction rather than trusted.
type Request struct {
	Tenant, Site  string
	FromStay      string
	ToStay        string
	Operator      string
	Reason        string
	GraceValidFor time.Duration
}

// Result is what a completed transfer produced.
type Result struct {
	TransferID      string
	FromEntitlement string
	ToEntitlement   string
	ToPackage       string
	DevicesRebound  int
	SessionsRebound int
	WindowEnds      time.Time
}

// Preview is what an operator is shown BEFORE they confirm. It is deliberately a separate call: a transfer
// is irreversible in the sense that the source access ends, and an operator who cannot see what they are
// about to move — and onto what — is confirming a sentence rather than a decision.
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
	// Blocker is the reason this transfer would be refused, or empty when it would be accepted. It is shown
	// to the operator BEFORE they type a reason and a password, so a refusal costs one screen rather than a
	// completed confirmation dialog.
	Blocker string `json:"blocker,omitempty"`
}

// PreviewTransfer evaluates the transfer WITHOUT performing it. It runs the same checks the execution runs,
// so an operator who sees "no blocker" and then confirms does not discover a new refusal — but it takes no
// locks and promises nothing: the state can change between preview and confirmation, which is why the
// execution re-checks everything under lock rather than trusting this.
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
			// One message for "the source is not here" and "the destination is not here": an operator surface
			// is not a place to enumerate another property's reservations either.
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

// blocker returns the first reason this transfer would be refused, in the order an operator would want to
// hear them: the structural impossibilities before the transient ones.
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
		return err.Error()
	}
	if liveSource == 0 {
		return ErrSourceNotTransferable.Error()
	}
	if liveDest > 0 {
		return ErrDestinationOccupied.Error()
	}
	return ""
}

// Execute performs the transfer in ONE transaction.
//
// Lock order is the approved global one — L1 Stay before L3 Entitlement — and BOTH Stays are locked, in a
// deterministic order (by id), before anything is read or written. Locking them in caller order would
// deadlock two operators transferring in opposite directions between the same pair, which is exactly the
// pair a busy property produces.
//
// Everything the preview checked is re-checked here under those locks. The preview is a courtesy; this is
// the authority.
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

	// (L1) Both Stays, locked in id order. This is the whole deadlock story: a fixed order means two
	// concurrent transfers between the same pair serialize instead of each holding what the other needs.
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
	// Re-checked UNDER THE LOCK, and this is the check the checkout race found missing: a checkout that
	// commits first leaves the guest holding GRACE, and grace is a courtesy the departed property granted.
	// Transferring it would carry that courtesy to a property that never agreed to it, and would do so
	// invisibly because no money and no authentication is involved either way.
	if fromStatus != "IN_HOUSE" {
		return Result{}, ErrSourceNotInHouse
	}
	if toStatus != "IN_HOUSE" {
		// Re-checked under the lock: the destination may have checked out between preview and confirmation,
		// and moving a guest onto a departed Stay would be worse than refusing.
		return Result{}, ErrDestinationNotEligible
	}

	// (L3) The source's live entitlement. FOR UPDATE so a concurrent grant or checkout cannot slip past.
	var fromEnt string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.entitlements
		 WHERE tenant_id=$1 AND site_id=$2 AND stay_id=$3 AND status IN ('PENDING','ACTIVE','SUSPENDED')
		 FOR UPDATE`, req.Tenant, req.Site, req.FromStay).Scan(&fromEnt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrSourceNotTransferable
		}
		return Result{}, err
	}
	// Already moved? The from/to uniqueness would refuse it anyway; this turns a constraint violation into an
	// answer the operator can act on.
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

	// The landing package: a zero-price, no-posting grant. Chosen inside the transaction so a revision
	// published a moment ago cannot change underneath the grant.
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
		 ORDER BY CASE ipr.package_type WHEN 'CHECKOUT_GRACE' THEN 0 WHEN 'FREE_STAY' THEN 1 ELSE 2 END
		 LIMIT 1`, req.Tenant, req.Site).Scan(&pkgRev, &planRev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// FAIL CLOSED. Terminating the source before knowing the guest can land somewhere would take
			// access away and give nothing back.
			return Result{}, ErrNoGracePackage
		}
		return Result{}, err
	}

	// The source ends as TERMINATED(TRANSFERRED) — the state the database requires before it will record the
	// lineage row at all.
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
	// NOTE the absent column: supersedes_entitlement_id is NOT set. A transfer crosses subjects and a
	// supersession does not; asserting both would be two contradictory claims about the same row, and the
	// Phase-5 guard refuses it.
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

	// SEAMLESS REBIND. The devices and their live sessions move to the new entitlement in place: no logout,
	// no re-authentication, and no nft churn, because the enforcement state is keyed on the session rather
	// than on which entitlement row it points at.
	//
	// The AUTHORIZATION INTERVAL is the part that is easy to get wrong, and the F9-i race is what found it.
	// entitlement_device_authorizations is an append-only INTERVAL model, not a flag: an interval left open
	// against a TERMINATED entitlement says the device is still authorized under an authority that no longer
	// exists. It is not merely untidy — every later question that reads the interval history (which
	// entitlement was this device under at time T, what does the boundary see, which sessions are
	// attributable to what) gets a wrong answer from it, and the accounting attribution is built on exactly
	// that. So the source's open intervals are CLOSED at the same instant the destination's are opened, in
	// this transaction, the same way checkout closes them at its boundary.
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
	// Open the destination's intervals for exactly those devices.
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
	// ...and close the source's, marking the current view with a bounded machine reason. GREATEST keeps an
	// interval from ending before it began if the clock or an earlier write disagrees.
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

	// THE TYPED LINEAGE. Both rows, under the Phase-5 capability, and both refused by the database unless
	// everything above actually holds.
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

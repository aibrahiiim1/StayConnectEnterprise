package poststay

// Reconnection to After Check-out Access. There is nothing to grant here and no package to choose: the Stay already holds
// its ONE After Check-out entitlement, granted once at the verified checkout from the site's one policy (internal/checkout).
// A guest who proved room + reservation from a device that was authorised on the Stay before check-out JOINS that
// entitlement: same window, same speed, same shared allowance and whatever was already used. Nothing a device does here can
// create a window, an allowance or a device place (the database refuses a new device on the entitlement as well:
// iam_v2.after_checkout_device_guard, migration 0193).
//
// The separate Post-Stay benefit (a zero-price POST_STAY package converted into a new entitlement) is retired; the database
// refuses a new purchase with that trigger (iam_v2.post_stay_conversion_retired).
//
// One transaction in lock order L1 Stay -> L2 context -> L3 entitlement, so a guest never ends up with a consumed context and
// no access.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
)

// ConvertRequest's subject comes from the consumed auth context, never the caller; a caller naming the
// profile could reconnect someone else's.
type ConvertRequest struct {
	Tenant, Site string
	// Context is the one-time POST_STAY_PIN auth context; consuming it re-verifies the episode, so a
	// reinstatement in between invalidates the reconnection.
	Context   string
	Presenter authctx.Presenter
}

type Converted struct {
	Profile     string
	Stay        string
	Entitlement string
	WindowEnds  time.Time
	// Joined is always true now: the device joined the stay's existing entitlement. (Kept so a caller that reads it keeps
	// compiling; there is no other outcome.)
	Joined bool
}

func (s *Store) Convert(ctx context.Context, req ConvertRequest, ac *authctx.Store) (Converted, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Converted{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := s.ConvertTx(ctx, tx, req, ac)
	if err != nil {
		return Converted{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Converted{}, err
	}
	return out, nil
}

// ConvertTx runs inside the caller's transaction so the device's session opens in the same commit; a reconnection without a
// session would strand the guest behind a consumed one-time context.
func (s *Store) ConvertTx(ctx context.Context, tx pgx.Tx, req ConvertRequest, ac *authctx.Store) (Converted, error) {
	// Consuming locks the origin Stay and re-asserts the episode, serializing the rest against Checkout and Reinstatement.
	consumed, err := ac.ConsumeTx(ctx, tx, req.Context, req.Presenter)
	if err != nil {
		return Converted{}, err
	}
	if consumed.Method != "POST_STAY_PIN" || consumed.PostStayProfile == "" {
		return Converted{}, authctx.ErrContextInvalid
	}

	var stay string
	if err := tx.QueryRow(ctx, `SELECT psp.origin_stay_id::text
		FROM iam_v2.post_stay_profiles psp
		WHERE psp.tenant_id=$1 AND psp.site_id=$2 AND psp.id=$3`,
		req.Tenant, req.Site, consumed.PostStayProfile).Scan(&stay); err != nil {
		return Converted{}, err
	}

	// The stay's live After Check-out entitlement (or, for a stay checked out before the unification, the live entitlement an
	// earlier post-stay conversion granted: it is honoured until it ends, never extended). The stay is already locked
	// (ConsumeTx), so no checkout or other reconnection decides this concurrently for the stay; the entitlement row is locked
	// for the device list below.
	var ent string
	var windowEnds *time.Time
	var live bool
	err = tx.QueryRow(ctx, `SELECT e.id::text, e.window_ends_at,
		       (e.status='ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now()))
		  FROM iam_v2.entitlements e
		  LEFT JOIN iam_v2.purchases pu ON pu.tenant_id=e.tenant_id AND pu.site_id=e.site_id AND pu.id=e.purchase_id
		 WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.stay_id=$3 AND e.status IN ('PENDING','ACTIVE','SUSPENDED')
		   AND (e.end_mode='GRACE_AFTER_CHECKOUT' OR pu.trigger='POST_STAY_CONVERSION')
		 FOR UPDATE OF e`, req.Tenant, req.Site, stay).Scan(&ent, &windowEnds, &live)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No live After Check-out entitlement: the window ended, the quota ran out, the site grants none, or the stay had no
			// eligible entitlement at checkout. There is nothing to join and nothing is granted instead.
			return Converted{}, ErrEntitlementNotJoinable
		}
		return Converted{}, err
	}
	if !live {
		return Converted{}, ErrEntitlementNotJoinable
	}

	// LINEAGE, enforced where the join happens and not only where the candidate was found: this device must already hold an
	// open authorisation on this entitlement, i.e. the checkout carried it over (it was authorised on the Stay before check-out,
	// online or not) and nobody removed it since. A device without one is refused; correct credentials do not change that.
	var held bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM iam_v2.entitlement_devices d
		  JOIN iam_v2.entitlement_device_authorizations a
		    ON a.entitlement_id=d.entitlement_id AND a.device_id=d.device_id AND a.deauthorized_at IS NULL
		 WHERE d.tenant_id=$1 AND d.site_id=$2 AND d.entitlement_id=$3 AND d.device_id=$4 AND d.status='AUTHORIZED')`,
		req.Tenant, req.Site, ent, req.Presenter.Device).Scan(&held); err != nil {
		return Converted{}, err
	}
	if !held {
		return Converted{}, ErrNoLineage
	}

	out := Converted{Profile: consumed.PostStayProfile, Stay: stay, Entitlement: ent, Joined: true}
	if windowEnds != nil {
		out.WindowEnds = *windowEnds
	}
	s.auditTx(ctx, tx, req.Tenant, AuditActionEntitlementJoined, stay, ent)
	return out, nil
}

// auditTx writes a join row in the reconnection's own transaction, so the record and the access commit or roll back
// together. It holds ids only (the stay and the entitlement are the appliance's own), no guest data. The insert is a
// savepoint: a missing audit right costs the row, never the guest's access.
func (s *Store) auditTx(ctx context.Context, tx pgx.Tx, tenant, action, stay, entitlement string) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return
	}
	if _, err := sp.Exec(ctx, `INSERT INTO public.audit_log
		(tenant_id, actor_type, actor_id, action, target_type, target_id, payload)
		VALUES ($1::uuid, 'system', NULL, $2, 'post_stay_signin', $3, $4)`,
		tenant, action, entitlement, map[string]any{"stay_id": stay, "entitlement_id": entitlement}); err != nil {
		_ = sp.Rollback(ctx)
		return
	}
	_ = sp.Commit(ctx)
}

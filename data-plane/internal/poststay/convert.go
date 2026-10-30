package poststay

// Zero-price post-stay conversion. Only INCLUDED package revisions are granted; a priced revision or any
// settlement method other than NOT_REQUIRED is refused (ErrSettlementRequired) rather than given away free.
// No PMS or payment traffic: the Stay moves CHECKED_OUT -> POST_STAY_ACTIVE, which posting_only_in_house
// keeps unable to post. It is one transaction in lock order L1 Stay -> L2 context -> L3 entitlement, so a
// guest never ends up with a consumed context and no access.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// ConvertRequest's subject comes from the consumed auth context, never the caller; a caller naming the
// profile could convert someone else's.
type ConvertRequest struct {
	Tenant, Site string
	// Context is the one-time POST_STAY_PIN auth context; consuming it re-verifies the episode, so a
	// reinstatement in between invalidates the conversion.
	Context         string
	Presenter       authctx.Presenter
	PackageRevision string
}

type Converted struct {
	Profile     string
	Stay        string
	Purchase    string
	Entitlement string
	WindowEnds  time.Time
	// Joined is true when the device joined the stay's existing live Entitlement (continuity); false when this call
	// granted the one Post-Stay benefit (a new Entitlement). Nothing else distinguishes the two outcomes.
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

// ConvertTx runs inside the caller's transaction so the device's session opens in the same commit; a
// conversion without a session would strand the guest behind a consumed one-time context. An empty
// PackageRevision is refused before anything is consumed.
func (s *Store) ConvertTx(ctx context.Context, tx pgx.Tx, req ConvertRequest, ac *authctx.Store) (Converted, error) {
	if req.PackageRevision == "" {
		return Converted{}, ErrPackageNotGrantable
	}
	// Consuming locks the origin Stay and re-asserts the episode, serializing the rest against Checkout and Reinstatement.
	consumed, err := ac.ConsumeTx(ctx, tx, req.Context, req.Presenter)
	if err != nil {
		return Converted{}, err
	}
	if consumed.Method != "POST_STAY_PIN" || consumed.PostStayProfile == "" {
		return Converted{}, authctx.ErrContextInvalid
	}

	// Must be a current, free POST_STAY revision in this scope, read inside the transaction so it cannot change under the grant.
	var priceMinor int64
	var settlement []string
	var planRevision string
	var durationPolicy []byte
	err = tx.QueryRow(ctx, `SELECT ipr.price_minor, ipr.settlement_methods, ipr.service_plan_revision_id::text,
		       COALESCE(ipr.duration_policy, '{}'::jsonb)
		  FROM iam_v2.internet_package_revisions ipr
		  JOIN iam_v2.internet_packages ip
		    ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		 WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ipr.id=$3
		   AND ipr.package_type='POST_STAY' AND ip.active
		   AND ip.current_revision_id = ipr.id`,
		req.Tenant, req.Site, req.PackageRevision).Scan(&priceMinor, &settlement, &planRevision, &durationPolicy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Converted{}, ErrPackageNotGrantable
		}
		return Converted{}, err
	}
	if priceMinor != 0 || !onlyNotRequired(settlement) {
		// Never grant a priced package free, and never charge for it here.
		return Converted{}, ErrSettlementRequired
	}

	var stay string
	var lifecycleVersion int
	if err := tx.QueryRow(ctx, `SELECT psp.origin_stay_id::text, psp.origin_lifecycle_version
		FROM iam_v2.post_stay_profiles psp
		WHERE psp.tenant_id=$1 AND psp.site_id=$2 AND psp.id=$3`,
		req.Tenant, req.Site, consumed.PostStayProfile).Scan(&stay, &lifecycleVersion); err != nil {
		return Converted{}, err
	}

	// CONTINUITY FIRST (AD-36). A Stay has one live Entitlement (ent_live_stay) and a device does not get one of its own.
	// If the stay has a live Entitlement -- the post-stay one a first device converted into, or the stay's own while it
	// still runs -- this device JOINS it: same window, same plan, same allowance and whatever it has already spent, with
	// the plan's device limit enforced under the entitlement lock. A returning device and a further device are both this
	// case. No purchase, no Entitlement and no window are created, so changing device cannot reset anything.
	// The stay is already locked (ConsumeTx), so no one else is deciding this concurrently.
	var liveEnt, liveStatus string
	var liveWindowEnds *time.Time
	var liveJoinable bool
	err = tx.QueryRow(ctx, `SELECT e.id::text, e.status, e.window_ends_at,
		       (e.status='ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now()))
		  FROM iam_v2.entitlements e
		 WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.stay_id=$3 AND e.status IN ('PENDING','ACTIVE','SUSPENDED')
		 FOR UPDATE`, req.Tenant, req.Site, stay).Scan(&liveEnt, &liveStatus, &liveWindowEnds, &liveJoinable)
	switch {
	case err == nil:
		if !liveJoinable {
			// Neither joinable nor replaceable: a second live Entitlement is forbidden, and replacing a suspended or
			// just-expired one would hand out a fresh allowance. The sweeper ends it; nothing is granted meanwhile.
			return Converted{}, ErrEntitlementNotJoinable
		}
		if _, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`,
			liveEnt, req.Presenter.Device); err != nil {
			if strings.Contains(err.Error(), "MAX_DEVICES_REACHED") {
				return Converted{}, ErrDeviceLimit
			}
			return Converted{}, err
		}
		out := Converted{Profile: consumed.PostStayProfile, Stay: stay, Entitlement: liveEnt, Joined: true}
		if liveWindowEnds != nil {
			out.WindowEnds = *liveWindowEnds
		}
		s.auditTx(ctx, tx, req.Tenant, AuditActionEntitlementJoined, stay, liveEnt)
		return out, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Converted{}, err
	}

	// THE POST-STAY BENEFIT. The stay has no live Entitlement (it ended at check-out). The zero-price POST_STAY package
	// grants a new one -- ONCE for the stay (one_conversion_per_episode in the database, this check for a clear answer).
	// This is a designed benefit of the package, granted by the stay and the package and not by the device; every device
	// after the first joins it above. If it has been granted and has ended or run out, it is over: a device must not be
	// able to ask for a fresh allowance, so ErrAlreadyConverted, not a second grant.
	var existing int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements e
		JOIN iam_v2.purchases pu ON pu.tenant_id=e.tenant_id AND pu.site_id=e.site_id AND pu.id=e.purchase_id
		WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.stay_id=$3 AND pu.trigger='POST_STAY_CONVERSION'`,
		req.Tenant, req.Site, stay).Scan(&existing); err != nil {
		return Converted{}, err
	}
	if existing > 0 {
		return Converted{}, ErrAlreadyConverted
	}

	if err := writerguard.Open(ctx, tx, "commerce_intent"); err != nil {
		return Converted{}, err
	}
	var purchase string
	// amount_minor is a literal 0, not read from the revision, so a mis-priced revision cannot carry an amount.
	// auth_context_id is the acquisition chain session activation follows to find the entitlement's device;
	// without it that device could not be readmitted after its first session.
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, stay_id, auth_context_id, trigger, amount_minor, state)
		VALUES ($1,$2,$3,$4,$5::uuid,'POST_STAY_CONVERSION',0,'GRANTED')
		RETURNING id::text`,
		req.Tenant, req.Site, req.PackageRevision, stay, req.Context).Scan(&purchase); err != nil {
		return Converted{}, err
	}

	window := windowFromPolicy(durationPolicy)
	var entitlement string
	var windowEnds time.Time
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, stay_id, purchase_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, window_ends_at, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW',
		        now() + make_interval(secs => $8), 'PENDING')
		RETURNING id::text, window_ends_at`,
		req.Tenant, req.Site, stay, purchase, durationPolicy, planRevision, req.PackageRevision,
		window.Seconds()).Scan(&entitlement, &windowEnds); err != nil {
		return Converted{}, err
	}
	// Use the approved transition, which writes the history the coherence trigger requires.
	if _, err := tx.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),NULL)`, entitlement); err != nil {
		return Converted{}, err
	}
	// A session needs an AUTHORIZED device binding; the plan's device limit is enforced here under the entitlement lock.
	if _, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`,
		entitlement, req.Presenter.Device); err != nil {
		return Converted{}, err
	}

	// Convert the Stay last, while its lock is held.
	if err := writerguard.Open(ctx, tx, "stay"); err != nil {
		return Converted{}, err
	}
	ct, err := tx.Exec(ctx, `UPDATE iam_v2.stays
		SET status='POST_STAY_ACTIVE'
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3
		  AND status='CHECKED_OUT' AND lifecycle_version=$4`,
		req.Tenant, req.Site, stay, lifecycleVersion)
	if err != nil {
		return Converted{}, err
	}
	if ct.RowsAffected() == 0 {
		// The Stay left the episode since consumption, or was never checked out: fail closed.
		return Converted{}, ErrNotAuthenticable
	}
	s.auditTx(ctx, tx, req.Tenant, AuditActionBenefitGranted, stay, entitlement)
	return Converted{Profile: consumed.PostStayProfile, Stay: stay, Purchase: purchase,
		Entitlement: entitlement, WindowEnds: windowEnds}, nil
}

// auditTx writes a grant/join row in the conversion's own transaction, so the record and the access commit or roll back
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

func onlyNotRequired(methods []string) bool {
	if len(methods) == 0 {
		return false
	}
	for _, m := range methods {
		if m != "NOT_REQUIRED" {
			return false
		}
	}
	return true
}

type Offer struct {
	PackageRevisionID string `json:"package_revision_id"`
	Code              string `json:"code"`
	DownKbps          int    `json:"down_kbps"`
	UpKbps            int    `json:"up_kbps"`
}

// GrantableOffers uses the same predicate as ConvertTx, so every offer shown is one the conversion accepts.
func (s *Store) GrantableOffers(ctx context.Context, tenant, site string) ([]Offer, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ipr.id::text, ip.code, COALESCE(spr.down_kbps,0), COALESCE(spr.up_kbps,0)
		  FROM iam_v2.internet_package_revisions ipr
		  JOIN iam_v2.internet_packages ip
		    ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = ipr.service_plan_revision_id
		 WHERE ipr.tenant_id=$1 AND ipr.site_id=$2
		   AND ipr.package_type='POST_STAY' AND ip.active
		   AND ip.current_revision_id = ipr.id
		   AND ipr.price_minor = 0
		   AND cardinality(ipr.settlement_methods) > 0
		   AND 'NOT_REQUIRED' = ALL(ipr.settlement_methods)
		 ORDER BY ip.code, ipr.id`, tenant, site)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Offer
	for rows.Next() {
		var o Offer
		if err := rows.Scan(&o.PackageRevisionID, &o.Code, &o.DownKbps, &o.UpKbps); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// windowFromPolicy falls back to a short window on an absent or malformed policy: free access must fail short, never long.
func windowFromPolicy(raw []byte) time.Duration {
	const fallback = 60 * time.Minute
	var p struct {
		Seconds int `json:"duration_seconds"`
		Minutes int `json:"duration_minutes"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return fallback
	}
	switch {
	case p.Seconds > 0 && p.Seconds <= 30*24*3600:
		return time.Duration(p.Seconds) * time.Second
	case p.Minutes > 0 && p.Minutes <= 30*24*60:
		return time.Duration(p.Minutes) * time.Minute
	default:
		return fallback
	}
}

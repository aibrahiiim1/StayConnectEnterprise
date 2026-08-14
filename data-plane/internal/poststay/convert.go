package poststay

// THE ZERO-PRICE POST-STAY CONVERSION.
//
// This is the only place in Phase 5 that grants access, and the whole of its financial behaviour is a
// refusal. Post-Stay v1 grants INCLUDED package revisions only: a revision carrying a price, or requiring any
// settlement method other than NOT_REQUIRED, is refused with ErrSettlementRequired. It is refused rather than
// granted-for-free deliberately — silently giving away a paid package would be a financial event nobody
// authorized, and it would be invisible precisely because no money moved.
//
// Nothing here allocates a P#, writes an outbox row, contacts a PMS or touches a payment provider. The Stay
// moves CHECKED_OUT -> POST_STAY_ACTIVE, which the database's own posting_only_in_house CHECK makes
// permanently unable to post.
//
// It is ONE transaction, in the approved lock order (L1 Stay -> L2 context -> L3 entitlement): the auth
// context is consumed, the Purchase and Entitlement are created, and the Stay is converted, or none of it
// happens. A guest never ends up with a consumed context and no access.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// ConvertRequest is a post-stay purchase. The subject comes from the CONSUMED auth context, never from the
// caller: a caller that could name the profile could convert someone else's.
type ConvertRequest struct {
	Tenant, Site string
	// Context is the one-time POST_STAY_PIN auth context id, and Presenter is the device/network it was
	// pinned to. Consuming it re-verifies the episode, so a reinstatement between authentication and purchase
	// invalidates the whole conversion.
	Context   string
	Presenter authctx.Presenter
	// PackageRevision is the POST_STAY package revision to grant. It must be zero-price.
	PackageRevision string
}

// Converted is the result of a successful conversion.
type Converted struct {
	Profile     string
	Stay        string
	Purchase    string
	Entitlement string
	WindowEnds  time.Time
}

// Convert consumes the post-stay auth context and grants the zero-price POST_STAY package in one transaction.
func (s *Store) Convert(ctx context.Context, req ConvertRequest, ac *authctx.Store) (Converted, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Converted{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// (L1/L2) Consume the context. This locks the origin Stay and re-asserts the episode before flipping the
	// context to consumed, so everything below is serialized against Checkout and Reinstatement.
	consumed, err := ac.ConsumeTx(ctx, tx, req.Context, req.Presenter)
	if err != nil {
		return Converted{}, err
	}
	if consumed.Method != "POST_STAY_PIN" || consumed.PostStayProfile == "" {
		// A context of another method is not a post-stay authorization, however valid it is in its own right.
		return Converted{}, authctx.ErrContextInvalid
	}

	// The package must be a currently-visible POST_STAY revision in this scope, and it must be free. Both are
	// read INSIDE the transaction so a revision published a moment ago cannot change underneath the grant.
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
		// The refusal that keeps Phase 5 out of the financial system. Granting it free instead would be a
		// giveaway nobody authorized; charging for it would be financial traffic this phase must not perform.
		return Converted{}, ErrSettlementRequired
	}

	// Resolve the origin Stay from the PROFILE. The caller never names it.
	var stay string
	var lifecycleVersion int
	if err := tx.QueryRow(ctx, `SELECT psp.origin_stay_id::text, psp.origin_lifecycle_version
		FROM iam_v2.post_stay_profiles psp
		WHERE psp.tenant_id=$1 AND psp.site_id=$2 AND psp.id=$3`,
		req.Tenant, req.Site, consumed.PostStayProfile).Scan(&stay, &lifecycleVersion); err != nil {
		return Converted{}, err
	}

	// One conversion per episode. The partial unique index on entitlements already permits only one live
	// entitlement per Stay, but this refusal is explicit so a second attempt gets an answer it can act on
	// rather than a constraint violation.
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
	// amount_minor is written as a literal 0 rather than from the revision: this path grants free access and
	// nothing else, so the row cannot carry an amount even if a revision were mis-priced.
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, stay_id, trigger, amount_minor, state)
		VALUES ($1,$2,$3,$4,'POST_STAY_CONVERSION',0,'GRANTED')
		RETURNING id::text`,
		req.Tenant, req.Site, req.PackageRevision, stay).Scan(&purchase); err != nil {
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
	// The status is moved through the APPROVED operation, which writes the history the coherence trigger
	// requires. Writing 'ACTIVE' directly would leave a status no transition backs.
	if _, err := tx.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),NULL)`, entitlement); err != nil {
		return Converted{}, err
	}

	// The Stay conversion, last, while its lock is still held. posting_allowed is already false after
	// checkout and the posting_only_in_house CHECK keeps it that way for POST_STAY_ACTIVE.
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
		// The Stay left the episode between consumption and here, or was never checked out. Fail closed.
		return Converted{}, ErrNotAuthenticable
	}

	if err := tx.Commit(ctx); err != nil {
		return Converted{}, err
	}
	return Converted{Profile: consumed.PostStayProfile, Stay: stay, Purchase: purchase,
		Entitlement: entitlement, WindowEnds: windowEnds}, nil
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

// windowFromPolicy reads the revision's duration policy. An absent or malformed policy yields a SHORT
// conservative window rather than a long one: the failure direction for "how long does this free access last"
// must be too little, never too much.
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

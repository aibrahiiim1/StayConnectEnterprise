// Package staygrant is the Phase-3 STAY-scoped access grant: it turns a verified PMS Auth Context into a
// durable Entitlement in ONE physical transaction — Auth Context consumption, offer Quote, Purchase,
// Entitlement + its initial lifecycle history, and the device authorization interval all commit or all roll
// back together. There is no partially-granted state: a guest never ends up with a consumed context and no
// access, or a Purchase with no Entitlement, or an Entitlement whose history does not back its status.
//
// SCOPE (deliberate, fail-closed): this phase grants INCLUDED (zero-price) package revisions only. Any
// revision carrying a price, or requiring a settlement method other than NOT_REQUIRED, is refused with
// ErrSettlementRequired — paid access and financial posting are out of scope and must never be silently
// approximated by granting the package for free.
package staygrant

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

var (
	// ErrPackageNotGrantable — the package revision is not a grantable guest package in this scope (wrong
	// tenant/site, a system/grace package, or not currently visible).
	ErrPackageNotGrantable = errors.New("staygrant: package revision is not grantable in this scope")
	// ErrSettlementRequired — the package is not INCLUDED (non-zero price or a settlement method beyond
	// NOT_REQUIRED). Paid access is out of scope for this phase; the grant fails closed.
	ErrSettlementRequired = errors.New("staygrant: package requires settlement (paid access is out of scope)")
	// ErrAlreadyEntitled — the Stay already holds a live (PENDING/ACTIVE/SUSPENDED) entitlement. The Stay
	// lifecycle allows exactly one, so a second grant is refused rather than racing the unique index.
	ErrAlreadyEntitled = errors.New("staygrant: stay already holds a live entitlement")
	// ErrDeviceLimit — the entitlement's plan does not allow another concurrent device.
	ErrDeviceLimit = errors.New("staygrant: device limit reached")
)

// Store performs atomic stay grants.
type Store struct {
	pool *pgxpool.Pool
	ctxs *authctx.Store
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool, ctxs: authctx.NewStore(pool)} }

// Request is what a guest-side grant needs. Everything that decides ACCESS is server-derived: the caller
// supplies only the one-time Auth Context id, the presenter identity it must match, and which package was
// selected. Price, policy, plan and duration are read from the pinned revision — never from the request.
type Request struct {
	AuthContextID string
	Presenter     authctx.Presenter
	PackageRevID  string
	// QuoteTTLSeconds bounds the offer quote's life. The quote is created and consumed inside this same
	// transaction, so the TTL exists for the durable record, not as a window the caller can widen.
	QuoteTTLSeconds int
}

// Result is the durable outcome of a successful grant.
type Result struct {
	QuoteID       string
	PurchaseID    string
	EntitlementID string
	DeviceAuthID  string
	Stay          string
	Interface     string
	ActivatedAt   time.Time
}

// Grant executes the whole chain in ONE transaction.
func (s *Store) Grant(ctx context.Context, tenant, site string, r Request) (Result, error) {
	var res Result
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err = s.GrantTx(ctx, tx, tenant, site, r)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return res, nil
}

// GrantTx runs the grant inside the caller's transaction (no nested transaction), so it can be composed with
// whatever else must commit atomically with it.
func (s *Store) GrantTx(ctx context.Context, tx pgx.Tx, tenant, site string, r Request) (Result, error) {
	var res Result
	// The Quote and the Purchase are both capability-scoped; the grant declares that operation once for
	// the whole chain it is about to write.
	if err := writerguard.Open(ctx, tx, writerguard.CapCommerceIntent); err != nil {
		return res, err
	}
	if r.QuoteTTLSeconds <= 0 {
		r.QuoteTTLSeconds = 300
	}
	// (1) consume the Auth Context EXACTLY once against the full server pin set. This takes the L1 Stay lock
	// first and yields the server-pinned Stay/Interface/Revision — the caller never names the Stay.
	consumed, err := s.ctxs.ConsumeTx(ctx, tx, r.AuthContextID, r.Presenter)
	if err != nil {
		return res, err
	}
	res.Stay, res.Interface = consumed.Stay, consumed.Interface

	// (2) QUOTE from the pinned revision. Everything priced or policy-bearing is read here, under the same
	// snapshot, so the Purchase and Entitlement cannot disagree with what was offered.
	var svcRev string
	var priceMinor int64
	var settlement []string
	var pkgType string
	var duration []byte
	err = tx.QueryRow(ctx, `SELECT ipr.service_plan_revision_id::text, ipr.price_minor, ipr.settlement_methods,
			ipr.package_type, ipr.duration_policy
		FROM iam_v2.internet_package_revisions ipr
		JOIN iam_v2.internet_packages ip ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ipr.id=$3
		  AND ip.current_revision_id = ipr.id          -- only the CURRENT revision of a package is grantable
		  AND ip.is_system IS NOT TRUE                 -- system/grace catalogs are never guest-purchasable
		  AND (ipr.visible_from IS NULL OR ipr.visible_from <= now())
		  AND (ipr.visible_until IS NULL OR ipr.visible_until > now())`,
		tenant, site, r.PackageRevID).Scan(&svcRev, &priceMinor, &settlement, &pkgType, &duration)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, ErrPackageNotGrantable
		}
		return res, err
	}
	if pkgType == "CHECKOUT_GRACE" {
		return res, ErrPackageNotGrantable
	}
	// INCLUDED-only, fail closed: a priced package or any settlement method beyond NOT_REQUIRED is refused.
	if priceMinor != 0 || len(settlement) != 1 || settlement[0] != "NOT_REQUIRED" {
		return res, ErrSettlementRequired
	}

	// (3) exactly one live entitlement per Stay — check explicitly so a second grant gets a typed error
	// rather than a raw unique-index violation (the index remains the real guard).
	var live int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements
		WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, res.Stay).Scan(&live); err != nil {
		return res, err
	}
	if live > 0 {
		return res, ErrAlreadyEntitled
	}

	// (4) durable QUOTE pinned to the consumed context + revision, with a server-built grant snapshot.
	// NOTE: the id parameters are passed separately in their uuid and text forms — reusing one placeholder as
	// both a uuid column value and a ::text jsonb member makes PostgreSQL deduce two types for it (42P08).
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.offer_quotes
		(tenant_id, site_id, auth_context_id, package_revision_id, pms_interface_id, price_minor,
		 grant_snapshot, expires_at, consumed_at)
		VALUES ($1,$2,$3,$4,$5,0,
		  jsonb_build_object('package_revision_id',$6::text,'service_plan_revision_id',$7::text,
		                     'package_type',$8::text,'duration_policy',$9::jsonb,'settlement','NOT_REQUIRED'),
		  now() + make_interval(secs => $10), now())
		RETURNING id::text`,
		tenant, site, r.AuthContextID, r.PackageRevID, res.Interface,
		r.PackageRevID, svcRev, pkgType, duration, r.QuoteTTLSeconds).Scan(&res.QuoteID); err != nil {
		return res, err
	}

	// (5) PURCHASE referencing that quote (the schema requires a GUEST_SELECTION purchase to carry one).
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, offer_quote_id, auth_context_id, pms_interface_id, stay_id,
		 authentication_interface_revision_id, trigger, amount_minor, state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'GUEST_SELECTION',0,'GRANTED')
		RETURNING id::text`,
		tenant, site, r.PackageRevID, res.QuoteID, r.AuthContextID, res.Interface, res.Stay, consumed.Revision).Scan(&res.PurchaseID); err != nil {
		return res, err
	}

	// (6) ENTITLEMENT + its INITIAL history, in this same transaction. The deferred coherence constraint
	// forbids an Entitlement committing without history, and apply_entitlement_transition is the only writer
	// that may set its status — so the real grant path produces exactly the history a Checkout later reads.
	endMode, windowSecs := grantShape(duration)
	var window *time.Time
	activatedAt := time.Now().UTC().Truncate(time.Microsecond)
	if windowSecs > 0 {
		w := activatedAt.Add(time.Duration(windowSecs) * time.Second)
		window = &w
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, stay_id, pms_interface_id, purchase_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, status, window_ends_at)
		SELECT $1,$2,$3,$4,$5,
		       jsonb_build_object('package_revision_id',$8::text,'service_plan_revision_id',$9::text,
		                          'granted_from','PMS_AUTH_CONTEXT'),
		       $7,$6, spr.time_accounting_mode, $10, 'ACTIVE', $11
		FROM iam_v2.service_plan_revisions spr WHERE spr.id=$7
		RETURNING id::text`,
		tenant, site, res.Stay, res.Interface, res.PurchaseID, r.PackageRevID, svcRev,
		r.PackageRevID, svcRev, endMode, window).Scan(&res.EntitlementID); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',$2,'GRANT')`,
		res.EntitlementID, activatedAt); err != nil {
		return res, err
	}
	res.ActivatedAt = activatedAt

	// (7) authorize the presenting device through the controlled operation, so the CURRENT view and the
	// append-only interval history are written together and the plan's device limit is enforced atomically.
	if err := tx.QueryRow(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,$3)::text`,
		res.EntitlementID, r.Presenter.Device, activatedAt).Scan(&res.DeviceAuthID); err != nil {
		return res, err
	}
	return res, nil
}

// grantShape derives the Entitlement's end mode + validity window from the revision's duration policy. An
// unrecognized policy yields MANUAL_END with no window rather than an invented duration.
func grantShape(durationPolicy []byte) (string, int64) {
	var p struct {
		EndMode      string `json:"end_mode"`
		WindowSecs   int64  `json:"window_seconds"`
		DurationSecs int64  `json:"duration_seconds"`
	}
	if err := json.Unmarshal(durationPolicy, &p); err != nil {
		return "MANUAL_END", 0
	}
	secs := p.WindowSecs
	if secs == 0 {
		secs = p.DurationSecs
	}
	switch p.EndMode {
	case "VALIDITY_WINDOW", "AT_CHECKOUT", "MANUAL_END", "GRACE_AFTER_CHECKOUT":
		return p.EndMode, secs
	}
	if secs > 0 {
		return "VALIDITY_WINDOW", secs
	}
	return "MANUAL_END", 0
}

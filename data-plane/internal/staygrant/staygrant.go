// Package staygrant turns a verified PMS Auth Context into an Entitlement in one transaction. Only
// INCLUDED (zero-price) revisions are granted; anything priced fails closed with ErrSettlementRequired.
package staygrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

var (
	// ErrPackageNotGrantable: wrong tenant/site, a system/grace package, or not currently visible.
	ErrPackageNotGrantable = errors.New("staygrant: package revision is not grantable in this scope")
	// ErrSettlementRequired: the package is priced or not acquirable for free.
	ErrSettlementRequired = errors.New("staygrant: package requires settlement (paid access is out of scope)")
	// ErrAlreadyEntitled: the Stay already holds a live entitlement; refused rather than racing the unique index.
	ErrAlreadyEntitled = errors.New("staygrant: stay already holds a live entitlement")
	ErrDeviceLimit     = errors.New("staygrant: device limit reached")
	// ErrStayLengthUnknown: a PER_STAY_NIGHT allowance cannot be computed without known arrival and departure,
	// and any guess is permanent once frozen onto the entitlement.
	ErrStayLengthUnknown = errors.New("staygrant: stay length is not authoritatively known")
)

// Store performs atomic stay grants.
type Store struct {
	aggregateOnlineTime bool
	pool                *pgxpool.Pool
	ctxs                *authctx.Store
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool, ctxs: authctx.NewStore(pool)} }

// WithAggregateOnlineTime sets whether NEW entitlements in AGGREGATE_ONLINE_TIME mode may be created
// (default off: the grant is refused). Accrual is data-driven and not gated by this flag; existing
// entitlements keep being accounted for, or a finite budget would become unlimited.
func (s *Store) WithAggregateOnlineTime(on bool) *Store {
	s.aggregateOnlineTime = on
	return s
}

// Request carries only the Auth Context id, its presenter and the selected package; price, policy, plan and
// duration are read from the pinned revision, never from the request.
type Request struct {
	AuthContextID string
	Presenter     authctx.Presenter
	PackageRevID  string
	// QuoteTTLSeconds is for the durable record only; the quote is created and consumed in this transaction.
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
	// Joined: the Stay already held an ACTIVE entitlement and this device took a slot; nothing else written.
	Joined bool
}

// Grant executes the whole chain in one transaction.
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

// GrantTx runs the grant inside the caller's transaction.
func (s *Store) GrantTx(ctx context.Context, tx pgx.Tx, tenant, site string, r Request) (Result, error) {
	var res Result
	if err := writerguard.Open(ctx, tx, writerguard.CapCommerceIntent); err != nil {
		return res, err
	}
	if r.QuoteTTLSeconds <= 0 {
		r.QuoteTTLSeconds = 300
	}
	// Consume the Auth Context once; this takes the L1 Stay lock first and yields the server-pinned
	// Stay/Interface/Revision (the caller never names the Stay).
	consumed, err := s.ctxs.ConsumeTx(ctx, tx, r.AuthContextID, r.Presenter)
	if err != nil {
		return res, err
	}
	res.Stay, res.Interface = consumed.Stay, consumed.Interface

	// Everything priced or policy-bearing is read once here so the Purchase and Entitlement match the offer.
	var svcRev string
	var priceMinor int64
	var settlement []string
	var pkgType string
	var duration []byte
	var allocPolicy []byte
	err = tx.QueryRow(ctx, `SELECT ipr.service_plan_revision_id::text, ipr.price_minor, ipr.settlement_methods,
			ipr.package_type, ipr.duration_policy, ipr.data_allocation_policy
		FROM iam_v2.internet_package_revisions ipr
		JOIN iam_v2.internet_packages ip ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ipr.id=$3
		  AND ip.current_revision_id = ipr.id          -- only the CURRENT revision of a package is grantable
		  AND ip.is_system IS NOT TRUE                 -- system/grace catalogs are never guest-purchasable
		  AND (ipr.visible_from IS NULL OR ipr.visible_from <= now())
		  AND (ipr.visible_until IS NULL OR ipr.visible_until > now())`,
		tenant, site, r.PackageRevID).Scan(&svcRev, &priceMinor, &settlement, &pkgType, &duration, &allocPolicy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, ErrPackageNotGrantable
		}
		return res, err
	}
	if pkgType == "CHECKOUT_GRACE" {
		return res, ErrPackageNotGrantable
	}
	// A free package may also accept vouchers (PREPAID); that does not make it paid.
	if priceMinor != 0 || !slices.Contains(settlement, "NOT_REQUIRED") {
		return res, ErrSettlementRequired
	}

	// One live entitlement per Stay; checked explicitly for a typed error (the unique index is the real guard).
	var live int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements
		WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, res.Stay).Scan(&live); err != nil {
		return res, err
	}
	if live > 0 {
		// Another device of a Stay with access joins its entitlement; authorize_entitlement_device enforces
		// the device limit (MAX_DEVICES_REACHED). A live entitlement that is not ACTIVE still refuses.
		var eid string
		err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.entitlements
			WHERE stay_id=$1 AND status='ACTIVE' AND (window_ends_at IS NULL OR window_ends_at > now())
			FOR UPDATE`, res.Stay).Scan(&eid)
		if errors.Is(err, pgx.ErrNoRows) {
			return res, ErrAlreadyEntitled
		}
		if err != nil {
			return res, err
		}
		res.EntitlementID, res.Joined = eid, true
		if err := tx.QueryRow(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())::text`,
			eid, r.Presenter.Device).Scan(&res.DeviceAuthID); err != nil {
			return res, err
		}
		return res, nil
	}

	// The id parameters are passed separately as uuid and text: one placeholder used as both makes
	// PostgreSQL deduce two types (42P08).
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

	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id, site_id, package_revision_id, offer_quote_id, auth_context_id, pms_interface_id, stay_id,
		 authentication_interface_revision_id, trigger, amount_minor, state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'GUEST_SELECTION',0,'GRANTED')
		RETURNING id::text`,
		tenant, site, r.PackageRevID, res.QuoteID, r.AuthContextID, res.Interface, res.Stay, consumed.Revision).Scan(&res.PurchaseID); err != nil {
		// purchase_once_per_stay: a race or stale offer; refuse as not grantable.
		if strings.Contains(err.Error(), "purchase_once_per_stay") {
			return res, ErrPackageNotGrantable
		}
		return res, err
	}

	// The deferred coherence constraint forbids an Entitlement without history, and
	// apply_entitlement_transition is the only writer of its status.
	endMode, windowSecs := grantShape(duration)
	var window *time.Time
	// The activation instant comes from the database clock, the same clock the eligibility checks in this transaction
	// use (now()), so the window cannot drift from them if the database ever runs elsewhere.
	var activatedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&activatedAt); err != nil {
		return res, err
	}
	activatedAt = activatedAt.UTC().Truncate(time.Microsecond)
	if windowSecs > 0 {
		w := activatedAt.Add(time.Duration(windowSecs) * time.Second)
		window = &w
	}
	// Fail closed on a time mode this deployment has not enabled (existing entitlements are still accounted in acctd).
	{
		var mode string
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(time_accounting_mode,'VALIDITY_WINDOW') FROM iam_v2.service_plan_revisions WHERE id=$1`,
			svcRev).Scan(&mode); err != nil {
			return res, err
		}
		// Shared rule, so the acquisition paths cannot disagree.
		if why := iamv2.TimeModeAcquirable(mode, s.aggregateOnlineTime); why != "" {
			return res, fmt.Errorf("refusing to grant plan revision %s: %s", svcRev, why)
		}
	}
	// A PER_STAY_NIGHT allowance is resolved once here under the L1 Stay lock and frozen onto the
	// entitlement, so later PMS changes to the stay cannot alter it. A FIXED package resolves to nil.
	dataQuota, qErr := s.effectiveDataQuota(ctx, tx, tenant, site, res.Stay, allocPolicy)
	if qErr != nil {
		return res, qErr
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, stay_id, pms_interface_id, purchase_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, status, window_ends_at, data_quota_bytes)
		SELECT $1,$2,$3,$4,$5,
		       jsonb_build_object('package_revision_id',$8::text,'service_plan_revision_id',$9::text,
		                          'granted_from','PMS_AUTH_CONTEXT'),
		       $7,$6, spr.time_accounting_mode, $10, 'ACTIVE', $11, $12
		FROM iam_v2.service_plan_revisions spr WHERE spr.id=$7
		RETURNING id::text`,
		tenant, site, res.Stay, res.Interface, res.PurchaseID, r.PackageRevID, svcRev,
		r.PackageRevID, svcRev, endMode, window, dataQuota).Scan(&res.EntitlementID); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',$2,'GRANT')`,
		res.EntitlementID, activatedAt); err != nil {
		return res, err
	}
	res.ActivatedAt = activatedAt

	// The controlled operation writes the current view and interval history together and enforces the device limit.
	if err := tx.QueryRow(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,$3)::text`,
		res.EntitlementID, r.Presenter.Device, activatedAt).Scan(&res.DeviceAuthID); err != nil {
		return res, err
	}
	return res, nil
}

// grantShape derives the end mode and validity window; an unrecognized policy yields MANUAL_END, no window.
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

// effectiveDataQuota resolves the byte allowance to freeze onto the entitlement, or nil to leave it reading
// the pinned plan revision (FIXED packages). A PER_STAY_NIGHT package with no authoritative arrival or
// departure is refused: there is no defensible guess. The dates are read under the L1 Stay lock.
func (s *Store) effectiveDataQuota(ctx context.Context, tx pgx.Tx, tenant, site, stayID string,
	rawPolicy []byte) (*int64, error) {
	if len(rawPolicy) == 0 {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(rawPolicy, &raw); err != nil {
		return nil, fmt.Errorf("%w: unreadable data allocation policy", ErrPackageNotGrantable)
	}
	policy, err := iamv2.ParseDataAllocationPolicy(raw)
	if err != nil {
		// Refuse rather than fall back to the plan quota: granting terms other than published is worse.
		return nil, fmt.Errorf("%w: %v", ErrPackageNotGrantable, err)
	}
	if policy.Mode != iamv2.AllocPerStayNight {
		return nil, nil
	}

	var arrival, departure *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT s.arrival, s.departure FROM iam_v2.stays s
		  WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.id=$3`,
		tenant, site, stayID).Scan(&arrival, &departure); err != nil {
		return nil, err
	}
	bytes, snapshot, err := iamv2.EffectiveDataQuotaBytes(policy,
		iamv2.EligibilitySubject{Stay: &iamv2.StayEvidence{Arrival: arrival, Departure: departure}})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStayLengthUnknown, err)
	}
	if !snapshot {
		return nil, nil
	}
	return &bytes, nil
}

// GrantShape exposes the end-mode rule so a paid stay purchase freezes the same shape as a free grant.
func GrantShape(durationPolicy []byte) (string, int64) { return grantShape(durationPolicy) }

// EffectiveDataQuota exposes the frozen PER_STAY_NIGHT allowance computation for the same reason.
func (s *Store) EffectiveDataQuota(ctx context.Context, tx pgx.Tx, tenant, site, stayID string, allocPolicy []byte) (*int64, error) {
	return s.effectiveDataQuota(ctx, tx, tenant, site, stayID, allocPolicy)
}

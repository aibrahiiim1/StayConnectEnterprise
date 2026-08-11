package iamv2

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// unmarshalNumberAware decodes JSON preserving numeric literals as json.Number so grant validation can
// reject non-integer (float) values (e.g. 5.5) rather than silently coercing them.
func unmarshalNumberAware(b []byte) map[string]any {
	m := map[string]any{}
	if len(b) == 0 {
		return m
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&m)
	return m
}

// PgCommerceRepository is the production/scratch Phase-2 commerce repository over iam_v2. It is
// constructed only when the Phase-2 master flag is ON; while dark the engine holds a nil repository.
type PgCommerceRepository struct{ db *pgxpool.Pool }

// NewPgCommerceRepository builds the repository over a pool.
func NewPgCommerceRepository(db *pgxpool.Pool) *PgCommerceRepository {
	return &PgCommerceRepository{db: db}
}

// WithTx runs fn in a single serializable-enough READ COMMITTED transaction with row locks; commit on
// success, rollback on any error (zero partial rows).
func (r *PgCommerceRepository) WithTx(ctx context.Context, fn func(CommerceTx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Phase-2 Commerce writes across three capability-scoped Phase-3 families: it consumes an Auth Context,
	// writes the Quote/Purchase pair, and terminates the superseded Entitlement through the controlled
	// operation. The scopes are declared on the transaction because that is where they are read.
	for _, cap := range []string{
		writerguard.CapAuthContext, writerguard.CapCommerceIntent, writerguard.CapDeviceAuth,
	} {
		if err := writerguard.Open(ctx, tx, cap); err != nil {
			return err
		}
	}
	if err := fn(&pgCommerceTx{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type pgCommerceTx struct{ tx pgx.Tx }

// subjectCols maps a subject to the iam_v2 (voucher_id, guest_account_id, guest_principal_id) triple
// used by auth_contexts / entitlements (exactly one non-null).
func subjectCols(s CommerceSubject) (voucher, account, principal *string) {
	switch s.Kind {
	case SubjectVoucher:
		v := s.VoucherID
		return &v, nil, nil
	case SubjectAccount:
		a := s.AccountID
		return nil, &a, nil
	case SubjectPrincipal:
		p := s.PrincipalID
		return nil, nil, &p
	}
	return nil, nil, nil
}

func (t *pgCommerceTx) LoadAuthContext(ctx context.Context, tenantID, siteID, id string) (AuthContextRow, error) {
	return t.loadAuthContext(ctx, tenantID, siteID, id, false)
}
func (t *pgCommerceTx) LockAuthContextForUpdate(ctx context.Context, tenantID, siteID, id string) (AuthContextRow, error) {
	return t.loadAuthContext(ctx, tenantID, siteID, id, true)
}

func (t *pgCommerceTx) loadAuthContext(ctx context.Context, tenantID, siteID, id string, lock bool) (AuthContextRow, error) {
	q := `SELECT id::text, tenant_id::text, site_id::text, method,
	             guest_account_id::text, voucher_id::text, guest_principal_id::text, stay_id::text,
	             device_id::text, guest_network_id::text, expires_at, consumed_at
	        FROM iam_v2.auth_contexts WHERE tenant_id=$1 AND site_id=$2 AND id=$3`
	if lock {
		q += " FOR UPDATE"
	}
	var row AuthContextRow
	var acct, vouch, princ, stay *string
	var consumed *time.Time
	var method string
	err := t.tx.QueryRow(ctx, q, tenantID, siteID, id).Scan(&row.ID, &row.TenantID, &row.SiteID, &method,
		&acct, &vouch, &princ, &stay, &row.DeviceID, &row.GuestNetworkID, &row.ExpiresAt, &consumed)
	if err == pgx.ErrNoRows {
		return AuthContextRow{}, &Error{Code: ErrACNotFound, Msg: "auth_context"}
	}
	if err != nil {
		return AuthContextRow{}, err
	}
	row.Method = Method(method)
	row.Consumed = consumed != nil
	if stay != nil {
		row.StayID = *stay
	}
	switch {
	case vouch != nil:
		row.Subject = CommerceSubject{Kind: SubjectVoucher, VoucherID: *vouch, Method: row.Method}
	case acct != nil:
		row.Subject = CommerceSubject{Kind: SubjectAccount, AccountID: *acct, Method: row.Method}
	case princ != nil:
		row.Subject = CommerceSubject{Kind: SubjectPrincipal, PrincipalID: *princ, Method: row.Method}
	}
	return row, nil
}

func (t *pgCommerceTx) ResolveActivePackageRevision(ctx context.Context, tenantID, siteID, packageID string) (PackageRevisionRow, error) {
	var row PackageRevisionRow
	var display, duration []byte
	var settlement []string
	var cexp *int
	err := t.tx.QueryRow(ctx,
		`SELECT r.id::text, r.package_id::text, r.service_plan_revision_id::text, r.package_type,
		        r.price_minor, r.currency, r.currency_exponent, r.settlement_methods,
		        r.visible_from, r.visible_until, p.active, (p.current_revision_id = r.id) AS is_current,
		        r.display, r.duration_policy
		   FROM iam_v2.internet_packages p
		   JOIN iam_v2.internet_package_revisions r ON r.id = p.current_revision_id
		  WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.id=$3`,
		tenantID, siteID, packageID).Scan(&row.ID, &row.PackageID, &row.PlanRevisionID, &row.PackageType,
		&row.PriceMinor, &row.Currency, &cexp, &settlement, &row.VisibleFrom, &row.VisibleUntil,
		&row.PackageActive, &row.IsCurrent, &display, &duration)
	if err == pgx.ErrNoRows {
		return PackageRevisionRow{}, &Error{Code: ErrInvalidInput, Msg: "package_not_found"}
	}
	if err != nil {
		return PackageRevisionRow{}, err
	}
	if cexp != nil {
		row.CurrencyExponent = *cexp
	}
	row.SettlementMethods = settlement
	if len(display) > 0 {
		_ = json.Unmarshal(display, &row.Display)
	}
	if len(duration) > 0 {
		row.DurationPolicy = unmarshalNumberAware(duration)
	}
	return row, nil
}

// ListActivePackageRevisions returns the current revision of every ACTIVE package for the tenant/site.
// The guest listing path filters these down to the subject-eligible, free, in-window ones in the domain
// layer; this query performs no eligibility disclosure of its own.
func (t *pgCommerceTx) ListActivePackageRevisions(ctx context.Context, tenantID, siteID string) ([]PackageRevisionRow, error) {
	rows, err := t.tx.Query(ctx,
		`SELECT r.id::text, r.package_id::text, r.service_plan_revision_id::text, r.package_type,
		        r.price_minor, r.currency, r.currency_exponent, r.settlement_methods,
		        r.visible_from, r.visible_until, p.active, (p.current_revision_id = r.id) AS is_current,
		        r.display, r.duration_policy
		   FROM iam_v2.internet_packages p
		   JOIN iam_v2.internet_package_revisions r ON r.id = p.current_revision_id
		  WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.active
		  ORDER BY p.code`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageRevisionRow
	for rows.Next() {
		var row PackageRevisionRow
		var display, duration []byte
		var settlement []string
		var cexp *int
		if err := rows.Scan(&row.ID, &row.PackageID, &row.PlanRevisionID, &row.PackageType,
			&row.PriceMinor, &row.Currency, &cexp, &settlement, &row.VisibleFrom, &row.VisibleUntil,
			&row.PackageActive, &row.IsCurrent, &display, &duration); err != nil {
			return nil, err
		}
		if cexp != nil {
			row.CurrencyExponent = *cexp
		}
		row.SettlementMethods = settlement
		if len(display) > 0 {
			_ = json.Unmarshal(display, &row.Display)
		}
		if len(duration) > 0 {
			row.DurationPolicy = unmarshalNumberAware(duration)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (t *pgCommerceTx) LoadPlanRevision(ctx context.Context, tenantID, siteID, id string) (PlanRevisionRow, error) {
	var row PlanRevisionRow
	var down, up *int
	var tq, dq *int64
	err := t.tx.QueryRow(ctx,
		`SELECT id::text, down_kbps, up_kbps, max_concurrent_devices, time_quota_seconds, data_quota_bytes, time_accounting_mode
		   FROM iam_v2.service_plan_revisions WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		tenantID, siteID, id).Scan(&row.ID, &down, &up, &row.MaxConcurrentDevices, &tq, &dq, &row.TimeAccountingMode)
	if err == pgx.ErrNoRows {
		return PlanRevisionRow{}, &Error{Code: ErrInvalidInput, Msg: "plan_revision_not_found"}
	}
	if err != nil {
		return PlanRevisionRow{}, err
	}
	if down != nil {
		row.DownKbps = *down
	}
	if up != nil {
		row.UpKbps = *up
	}
	if tq != nil {
		row.TimeQuotaSeconds = *tq
	}
	if dq != nil {
		row.DataQuotaBytes = *dq
	}
	return row, nil
}

func (t *pgCommerceTx) LoadEligibilityRules(ctx context.Context, packageRevisionID string) ([]EligibilityRule, error) {
	rows, err := t.tx.Query(ctx,
		`SELECT rule_type, rule_value FROM iam_v2.package_eligibility_rules WHERE package_revision_id=$1 ORDER BY id`, packageRevisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EligibilityRule
	for rows.Next() {
		var rt string
		var rv []byte
		if err := rows.Scan(&rt, &rv); err != nil {
			return nil, err
		}
		m := map[string]any{}
		_ = json.Unmarshal(rv, &m)
		out = append(out, EligibilityRule{Type: rt, Value: m})
	}
	return out, rows.Err()
}

func (t *pgCommerceTx) LoadGrantTiers(ctx context.Context, packageRevisionID string) ([]GrantTier, error) {
	rows, err := t.tx.Query(ctx,
		`SELECT tier_order, grant_value FROM iam_v2.package_grant_tiers WHERE package_revision_id=$1 ORDER BY tier_order`, packageRevisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GrantTier
	for rows.Next() {
		var ord int
		var gv []byte
		if err := rows.Scan(&ord, &gv); err != nil {
			return nil, err
		}
		out = append(out, GrantTier{Order: ord, Value: unmarshalNumberAware(gv)})
	}
	return out, rows.Err()
}

func (t *pgCommerceTx) HasPriorPurchase(ctx context.Context, tenantID, siteID, packageRevisionID string, subj CommerceSubject) (bool, error) {
	v, a, p := subjectCols(subj)
	var exists bool
	err := t.tx.QueryRow(ctx,
		`SELECT EXISTS(
		    SELECT 1 FROM iam_v2.entitlements e
		     WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.package_revision_id=$3
		       AND ( ($4::uuid IS NOT NULL AND e.voucher_id=$4::uuid)
		          OR ($5::uuid IS NOT NULL AND e.guest_account_id=$5::uuid)
		          OR ($6::uuid IS NOT NULL AND e.guest_principal_id=$6::uuid) ))`,
		tenantID, siteID, packageRevisionID, v, a, p).Scan(&exists)
	return exists, err
}

func (t *pgCommerceTx) InsertOfferQuote(ctx context.Context, q OfferQuoteSpec) (string, error) {
	var id string
	// pms_interface_id / settlement_mapping_id / tax_* are left NULL (free quote); grant_snapshot is the
	// canonical typed snapshot.
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.offer_quotes
		   (tenant_id, site_id, auth_context_id, package_revision_id, price_minor, currency, currency_exponent, grant_snapshot, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`,
		q.TenantID, q.SiteID, q.AuthContextID, q.PackageRevisionID, q.PriceMinor, q.Currency, q.CurrencyExponent, q.GrantSnapshot.Canonical(), q.ExpiresAt).Scan(&id)
	return id, err
}

func (t *pgCommerceTx) LockOfferQuoteForUpdate(ctx context.Context, tenantID, siteID, id string) (OfferQuoteRow, error) {
	var row OfferQuoteRow
	var snap []byte
	var consumed *time.Time
	var cexp *int
	err := t.tx.QueryRow(ctx,
		`SELECT id::text, tenant_id::text, site_id::text, auth_context_id::text, package_revision_id::text,
		        price_minor, currency, currency_exponent, pms_interface_id::text, settlement_mapping_id::text,
		        tax_code, tax_rate_bp, tax_amount_minor, grant_snapshot, expires_at, consumed_at
		   FROM iam_v2.offer_quotes WHERE tenant_id=$1 AND site_id=$2 AND id=$3 FOR UPDATE`,
		tenantID, siteID, id).Scan(&row.ID, &row.TenantID, &row.SiteID, &row.AuthContextID, &row.PackageRevisionID,
		&row.PriceMinor, &row.Currency, &cexp, &row.PMSInterfaceID, &row.SettlementMappingID,
		&row.TaxCode, &row.TaxRateBP, &row.TaxAmountMinor, &snap, &row.ExpiresAt, &consumed)
	if err == pgx.ErrNoRows {
		return OfferQuoteRow{}, &Error{Code: ErrACNotFound, Msg: "quote_not_found"}
	}
	if err != nil {
		return OfferQuoteRow{}, err
	}
	if cexp != nil {
		row.CurrencyExponent = *cexp
	}
	row.Consumed = consumed != nil
	gs, perr := ParseGrantSnapshot(snap)
	if perr != nil {
		return OfferQuoteRow{}, perr
	}
	row.GrantSnapshot = gs
	return row, nil
}

// AcquireSubjectLock takes a transaction-scoped advisory lock keyed by a deterministic hash of
// (tenant, site, subject-kind, subject-id) so all confirms for the same subject serialize in one order.
func (t *pgCommerceTx) AcquireSubjectLock(ctx context.Context, tenantID, siteID string, subj CommerceSubject) error {
	id, _ := subj.subjectID()
	h := fnv.New64a()
	h.Write([]byte("phase2.subject\x00" + tenantID + "\x00" + siteID + "\x00" + string(subj.Kind) + "\x00" + id))
	key := int64(binary.BigEndian.Uint64(h.Sum(nil)))
	_, err := t.tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key)
	return err
}

func (t *pgCommerceTx) ConsumeOfferQuote(ctx context.Context, quoteID string, now time.Time) (bool, error) {
	var id string
	err := t.tx.QueryRow(ctx,
		`UPDATE iam_v2.offer_quotes SET consumed_at=$2 WHERE id=$1 AND consumed_at IS NULL AND expires_at>$2 RETURNING id::text`,
		quoteID, now).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (t *pgCommerceTx) ConsumeAuthContextByID(ctx context.Context, authContextID string, now time.Time) (bool, error) {
	var id string
	err := t.tx.QueryRow(ctx,
		`UPDATE iam_v2.auth_contexts SET consumed_at=$2 WHERE id=$1 AND consumed_at IS NULL AND expires_at>$2 RETURNING id::text`,
		authContextID, now).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (t *pgCommerceTx) InsertPurchase(ctx context.Context, p PurchaseSpec) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.purchases
		   (tenant_id, site_id, package_revision_id, offer_quote_id, auth_context_id, trigger, amount_minor, currency, currency_exponent, state)
		 VALUES ($1,$2,$3,$4,$5,'GUEST_SELECTION',$6,$7,$8,'PENDING') RETURNING id::text`,
		p.TenantID, p.SiteID, p.PackageRevisionID, p.OfferQuoteID, p.AuthContextID, p.AmountMinor, p.Currency, p.CurrencyExponent).Scan(&id)
	return id, err
}

func (t *pgCommerceTx) InsertSettlement(ctx context.Context, tenantID, siteID, purchaseID string) error {
	_, err := t.tx.Exec(ctx,
		`INSERT INTO iam_v2.settlements (tenant_id, site_id, purchase_id, method, status)
		 VALUES ($1,$2,$3,'NOT_REQUIRED','NOT_REQUIRED')`, tenantID, siteID, purchaseID)
	return err
}

// TerminateLiveEntitlementForSubject ends whatever access a subject currently holds, so a new grant can
// supersede it.
//
// The termination is performed by the controlled writer, NOT by an UPDATE here. Two reasons, and the second
// is the one that actually bites:
//
//	The Entitlement status column is controlled-writer-only. This path used to set it directly, which works
//	only for as long as the service's database role happens to also own the controlled operations. Under the
//	dedicated minimum-privilege owners Gate-P introduces, the guard would refuse it — and it would refuse it
//	at the moment a guest was buying access, not at deploy time.
//
//	A raw UPDATE moves the status and writes NO history. An Entitlement that is TERMINATED with nothing in
//	its transition chain saying when or why is unanswerable afterwards: "was this guest's access ended by an
//	operator, by checkout, or by another purchase?" has no recorded answer. Routing through the controlled
//	operation appends the transition as a matter of course.
//
// The row is selected FOR UPDATE first so the entitlement cannot be terminated by a concurrent path between
// the read and the transition — the same lock the controlled operation takes, taken in the same order.
func (t *pgCommerceTx) TerminateLiveEntitlementForSubject(ctx context.Context, tenantID, siteID string, subj CommerceSubject) (string, error) {
	v, a, p := subjectCols(subj)
	var id string
	err := t.tx.QueryRow(ctx,
		`SELECT id::text FROM iam_v2.entitlements
		  WHERE tenant_id=$1 AND site_id=$2 AND status IN ('PENDING','ACTIVE','SUSPENDED')
		    AND ( ($3::uuid IS NOT NULL AND voucher_id=$3::uuid)
		       OR ($4::uuid IS NOT NULL AND guest_account_id=$4::uuid)
		       OR ($5::uuid IS NOT NULL AND guest_principal_id=$5::uuid) )
		  ORDER BY activated_at DESC NULLS LAST, id
		  LIMIT 1
		  FOR UPDATE`,
		tenantID, siteID, v, a, p).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", nil // no live entitlement to supersede
	}
	if err != nil {
		return "", err
	}
	if _, err := t.tx.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1::uuid, 'TERMINATED', now(), 'SUPERSEDED')`, id); err != nil {
		return "", err
	}
	return id, nil
}

func (t *pgCommerceTx) InsertEntitlement(ctx context.Context, e EntitlementSpec) (string, error) {
	v, a, p := subjectCols(e.Subject)
	snap := e.PolicySnapshot.Canonical()
	var supersedes *string
	if e.SupersedesID != "" {
		supersedes = &e.SupersedesID
	}
	endMode := e.EndMode
	if endMode == "" {
		endMode = "MANUAL_END"
	}
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.entitlements
		   (tenant_id, site_id, voucher_id, guest_account_id, guest_principal_id, purchase_id,
		    policy_snapshot, service_plan_revision_id, package_revision_id, time_accounting_mode,
		    end_mode, window_ends_at, status, supersedes_entitlement_id, activated_at)
		 VALUES ($1,$2,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,$9,$10,$11,$12,'ACTIVE',$13::uuid, now())
		 RETURNING id::text`,
		e.TenantID, e.SiteID, v, a, p, e.PurchaseID, snap, e.ServicePlanRevID, e.PackageRevID, e.TimeAccountingMode, endMode, e.WindowEndsAt, supersedes).Scan(&id)
	return id, err
}

func (t *pgCommerceTx) MarkPurchaseGranted(ctx context.Context, purchaseID string) error {
	_, err := t.tx.Exec(ctx, `UPDATE iam_v2.purchases SET state='GRANTED' WHERE id=$1`, purchaseID)
	return err
}

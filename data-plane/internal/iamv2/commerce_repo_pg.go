package iamv2

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"strings"
	"time"

	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// unmarshalNumberAware keeps numbers as json.Number so grant validation can reject floats (e.g. 5.5) instead
// of coercing them.
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

type PgCommerceRepository struct{ db *pgxpool.Pool }

func NewPgCommerceRepository(db *pgxpool.Pool) *PgCommerceRepository {
	return &PgCommerceRepository{db: db}
}

func (r *PgCommerceRepository) WithTx(ctx context.Context, fn func(CommerceTx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Writes span three capability-scoped families (auth context, quote/purchase, entitlement termination);
	// scopes are declared on the transaction because that is where they are read.
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
	             device_id::text, guest_network_id::text, expires_at, consumed_at, anonymous_subject_id::text,
	             client_group_id::text
	        FROM iam_v2.auth_contexts WHERE tenant_id=$1 AND site_id=$2 AND id=$3`
	if lock {
		q += " FOR UPDATE"
	}
	var row AuthContextRow
	var acct, vouch, princ, stay, anon, group *string
	var consumed *time.Time
	var method string
	err := t.tx.QueryRow(ctx, q, tenantID, siteID, id).Scan(&row.ID, &row.TenantID, &row.SiteID, &method,
		&acct, &vouch, &princ, &stay, &row.DeviceID, &row.GuestNetworkID, &row.ExpiresAt, &consumed, &anon, &group)
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
	if group != nil {
		row.ClientGroupID = *group
	}
	switch {
	case vouch != nil:
		row.Subject = CommerceSubject{Kind: SubjectVoucher, VoucherID: *vouch, Method: row.Method}
	case acct != nil:
		row.Subject = CommerceSubject{Kind: SubjectAccount, AccountID: *acct, Method: row.Method}
	case princ != nil:
		row.Subject = CommerceSubject{Kind: SubjectPrincipal, PrincipalID: *princ, Method: row.Method}
	case anon != nil:
		row.Subject = CommerceSubject{Kind: SubjectAnonymous, AnonymousID: *anon, Method: row.Method}
	}
	row.Subject.DeviceID = row.DeviceID
	return row, nil
}

func (t *pgCommerceTx) ResolveActivePackageRevision(ctx context.Context, tenantID, siteID, packageID string) (PackageRevisionRow, error) {
	var row PackageRevisionRow
	var display, duration []byte
	var settlement []string
	var cexp *int
	var cur *string
	err := t.tx.QueryRow(ctx,
		`SELECT r.id::text, r.package_id::text, r.service_plan_revision_id::text, r.package_type,
		        r.price_minor, r.currency, r.currency_exponent, r.settlement_methods,
		        r.visible_from, r.visible_until, p.active, (p.current_revision_id = r.id) AS is_current,
		        r.display, r.duration_policy, COALESCE(r.data_allocation_policy->>'mode','FIXED')
		   FROM iam_v2.internet_packages p
		   JOIN iam_v2.internet_package_revisions r ON r.id = p.current_revision_id
		  WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.id=$3`,
		tenantID, siteID, packageID).Scan(&row.ID, &row.PackageID, &row.PlanRevisionID, &row.PackageType,
		&row.PriceMinor, &cur, &cexp, &settlement, &row.VisibleFrom, &row.VisibleUntil,
		&row.PackageActive, &row.IsCurrent, &display, &duration, &row.DataAllocationMode)
	if err == pgx.ErrNoRows {
		return PackageRevisionRow{}, &Error{Code: ErrInvalidInput, Msg: "package_not_found"}
	}
	if err != nil {
		return PackageRevisionRow{}, err
	}
	if cur != nil {
		row.Currency = *cur
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

// No eligibility disclosure here; the domain layer filters these down to the subject-eligible, free, in-window
// ones.
func (t *pgCommerceTx) ListActivePackageRevisions(ctx context.Context, tenantID, siteID string) ([]PackageRevisionRow, error) {
	rows, err := t.tx.Query(ctx,
		`SELECT r.id::text, r.package_id::text, r.service_plan_revision_id::text, r.package_type,
		        r.price_minor, r.currency, r.currency_exponent, r.settlement_methods,
		        r.visible_from, r.visible_until, p.active, (p.current_revision_id = r.id) AS is_current,
		        r.display, r.duration_policy, COALESCE(r.data_allocation_policy->>'mode','FIXED')
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
		// Currency is nullable: the system emergency-grace package carries none, and scanning it into a string
		// would fail the whole listing. NULL reads as "" and the currency gates exclude it.
		var cur *string
		if err := rows.Scan(&row.ID, &row.PackageID, &row.PlanRevisionID, &row.PackageType,
			&row.PriceMinor, &cur, &cexp, &settlement, &row.VisibleFrom, &row.VisibleUntil,
			&row.PackageActive, &row.IsCurrent, &display, &duration, &row.DataAllocationMode); err != nil {
			return nil, err
		}
		if cur != nil {
			row.Currency = *cur
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

// Returns ("", nil) for a voucher that does not belong to this owner, so an unresolvable card offers no
// package rather than all of them.
func (t *pgCommerceTx) VoucherPinnedPackageRevision(ctx context.Context, tenantID, siteID, voucherID string) (string, error) {
	var rev string
	err := t.tx.QueryRow(ctx, `
	    SELECT package_revision_id::text FROM iam_v2.vouchers
	     WHERE tenant_id = $1 AND site_id = $2 AND id = $3`,
		tenantID, siteID, voucherID).Scan(&rev)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return rev, nil
}

// SELECT on iam_v2.guest_network_lineage only; the replacement table behind it holds operator, reason and
// addressing no guest path may read.
func (t *pgCommerceTx) GuestNetworkAncestors(ctx context.Context, tenantID, siteID, guestNetworkID string) ([]string, error) {
	if strings.TrimSpace(guestNetworkID) == "" {
		return nil, nil
	}
	rows, err := t.tx.Query(ctx, `
	    SELECT ancestor_id::text FROM iam_v2.guest_network_lineage
	     WHERE tenant_id = $1 AND site_id = $2 AND network_id = $3 ORDER BY depth`,
		tenantID, siteID, guestNetworkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
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

func (t *pgCommerceTx) HasPriorPurchase(ctx context.Context, tenantID, siteID string, q PriorPurchaseQuery) (bool, error) {
	// Keyed on the package, not the revision: a republish must not hand every Client a fresh free allowance.
	// An anonymous subject is new every time a device has no live access, so its history is the device's own
	// (purchase -> auth context -> device). An identified Client's own history counts, plus the device's when
	// the policy says so. The window runs from activated_at; a pending grant counts as now.
	var since *time.Time
	if q.Policy.WithinHours > 0 {
		s := time.Now().Add(-time.Duration(q.Policy.WithinHours) * time.Hour)
		since = &s
	}
	bySubject := `SELECT 1 FROM iam_v2.entitlements e
	                JOIN iam_v2.internet_package_revisions r ON r.id = e.package_revision_id
	               WHERE e.tenant_id=$1 AND e.site_id=$2 AND r.package_id=$3::uuid
	                 AND ($8::timestamptz IS NULL OR COALESCE(e.activated_at, now()) >= $8::timestamptz)
	                 AND ( ($4::uuid IS NOT NULL AND e.voucher_id=$4::uuid)
	                    OR ($5::uuid IS NOT NULL AND e.guest_account_id=$5::uuid)
	                    OR ($6::uuid IS NOT NULL AND e.guest_principal_id=$6::uuid) )`
	byDevice := `SELECT 1 FROM iam_v2.entitlements e
	               JOIN iam_v2.internet_package_revisions r ON r.id = e.package_revision_id
	               JOIN iam_v2.purchases pu     ON pu.id = e.purchase_id
	               JOIN iam_v2.auth_contexts ac ON ac.id = pu.auth_context_id
	              WHERE e.tenant_id=$1 AND e.site_id=$2 AND r.package_id=$3::uuid
	                AND ($8::timestamptz IS NULL OR COALESCE(e.activated_at, now()) >= $8::timestamptz)
	                AND $7::uuid IS NOT NULL AND ac.device_id = $7::uuid`
	v, a, p := subjectCols(q.Subject)
	var dev *string
	useDevice := q.Subject.Kind == SubjectAnonymous || q.Policy.AlsoByDevice
	if useDevice {
		dev = nullable(q.Subject.DeviceID)
	}
	// One statement with every parameter referenced: PostgreSQL refuses a statement with an unreferenced
	// parameter of undeterminable type (42P18). A NULL device never matches.
	sql := `SELECT EXISTS(` + bySubject + `) OR EXISTS(` + byDevice + `)`
	var exists bool
	err := t.tx.QueryRow(ctx, sql, tenantID, siteID, q.PackageID, v, a, p, dev, since).Scan(&exists)
	return exists, err
}

// EndedByStaff asks iam_v2.package_ended_by_staff (0202), the same rule the entitlement guard enforces, so an offer
// is never shown that the grant then refuses.
func (t *pgCommerceTx) EndedByStaff(ctx context.Context, tenantID, siteID, packageRevisionID string, subj CommerceSubject) (bool, error) {
	v, a, p := subjectCols(subj)
	var ended bool
	err := t.tx.QueryRow(ctx, `SELECT iam_v2.package_ended_by_staff($1::uuid,$2::uuid,$3::uuid,NULL::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid)`,
		tenantID, siteID, packageRevisionID, a, v, p, nullable(subj.DeviceID)).Scan(&ended)
	return ended, err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Resolves a revision by id, current or not: a voucher is honoured against the immutable revision it was
// printed for.
func (t *pgCommerceTx) LoadPackageRevisionByID(ctx context.Context, tenantID, siteID, revID string) (PackageRevisionRow, error) {
	var row PackageRevisionRow
	var display, duration []byte
	var settlement []string
	var cexp *int
	var cur *string
	err := t.tx.QueryRow(ctx,
		`SELECT r.id::text, r.package_id::text, r.service_plan_revision_id::text, r.package_type,
		        r.price_minor, r.currency, r.currency_exponent, r.settlement_methods,
		        r.visible_from, r.visible_until, p.active, (p.current_revision_id = r.id) AS is_current,
		        r.display, r.duration_policy, COALESCE(r.data_allocation_policy->>'mode','FIXED')
		   FROM iam_v2.internet_package_revisions r
		   JOIN iam_v2.internet_packages p ON p.tenant_id = r.tenant_id AND p.site_id = r.site_id AND p.id = r.package_id
		  WHERE r.tenant_id=$1 AND r.site_id=$2 AND r.id=$3`,
		tenantID, siteID, revID).Scan(&row.ID, &row.PackageID, &row.PlanRevisionID, &row.PackageType,
		&row.PriceMinor, &cur, &cexp, &settlement, &row.VisibleFrom, &row.VisibleUntil,
		&row.PackageActive, &row.IsCurrent, &display, &duration, &row.DataAllocationMode)
	if err == pgx.ErrNoRows {
		return PackageRevisionRow{}, &Error{Code: ErrInvalidInput, Msg: "package_revision_not_found"}
	}
	if err != nil {
		return PackageRevisionRow{}, err
	}
	if cur != nil {
		row.Currency = *cur
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

func (t *pgCommerceTx) InsertPurchaseAs(ctx context.Context, p PurchaseSpec, trigger, state string) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.purchases
		   (tenant_id, site_id, package_revision_id, offer_quote_id, auth_context_id, trigger, amount_minor, currency, currency_exponent, state)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text`,
		p.TenantID, p.SiteID, p.PackageRevisionID, p.OfferQuoteID, p.AuthContextID, trigger, p.AmountMinor, p.Currency,
		p.CurrencyExponent, state).Scan(&id)
	return id, err
}

// The database birth rule (migration 0095) refuses incoherent pairs, including a PREPAID settlement that is
// not a voucher's.
func (t *pgCommerceTx) InsertSettlementAs(ctx context.Context, tenantID, siteID, purchaseID, method, status string) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.settlements (tenant_id, site_id, purchase_id, method, status)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id::text`, tenantID, siteID, purchaseID, method, status).Scan(&id)
	return id, err
}

func (t *pgCommerceTx) GrantVoucherEntitlement(ctx context.Context, tenantID, siteID, purchaseID string) (string, string, error) {
	var eid string
	var superseded *string
	var already bool
	err := t.tx.QueryRow(ctx,
		`SELECT entitlement_id::text, already_granted, superseded::text
		   FROM iam_v2.p4_grant_voucher_entitlement($1,$2,$3)`, tenantID, siteID, purchaseID).Scan(&eid, &already, &superseded)
	if err != nil {
		return "", "", err
	}
	if superseded != nil {
		return eid, *superseded, nil
	}
	return eid, "", nil
}

func (t *pgCommerceTx) InsertOfferQuote(ctx context.Context, q OfferQuoteSpec) (string, error) {
	var id string
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

// Transaction-scoped advisory lock keyed by a deterministic hash of (tenant, site, subject-kind, subject-id),
// so confirms for one subject serialize in one order.
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

// Termination goes through the controlled writer, not a raw UPDATE: the status column is controlled-writer-
// only, and a raw UPDATE records no transition history. The row is locked FOR UPDATE first, in the same order
// the controlled operation uses.
func (t *pgCommerceTx) TerminateLiveEntitlementForSubject(ctx context.Context, tenantID, siteID string, subj CommerceSubject) (string, error) {
	v, a, p := subjectCols(subj)
	// The runtime role holds no UPDATE, so the SELECT ... FOR UPDATE and the termination both live inside the
	// controlled function. A NULL result means the subject held nothing.
	var id *string
	err := t.tx.QueryRow(ctx,
		`SELECT iam_v2.p4_terminate_live_entitlement_for_subject($1,$2,$3::uuid,$4::uuid,$5::uuid)::text`,
		tenantID, siteID, v, a, p).Scan(&id)
	if err != nil {
		return "", err
	}
	if id == nil {
		return "", nil
	}
	return *id, nil
}

func (t *pgCommerceTx) InsertEntitlement(ctx context.Context, e EntitlementSpec) (string, error) {
	v, a, p := subjectCols(e.Subject)
	snap := e.PolicySnapshot.Canonical()
	var supersedes *string
	if e.SupersedesID != "" {
		supersedes = &e.SupersedesID
	}
	// p4_insert_entitlement creates the row and its opening ACTIVE transition together, so the runtime role
	// needs no INSERT or UPDATE on entitlements.
	var id string
	err := t.tx.QueryRow(ctx,
		`SELECT iam_v2.p4_insert_entitlement($1,$2,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,$9,$10,$11,$12,$13::uuid)::text`,
		e.TenantID, e.SiteID, v, a, p, e.PurchaseID, snap, e.ServicePlanRevID, e.PackageRevID,
		e.TimeAccountingMode, e.EndMode, e.WindowEndsAt, supersedes).Scan(&id)
	return id, err
}

// No parameter for subject, package revision or policy snapshot: the operation re-resolves them from the
// purchase's pinned quote and auth context, so a caller cannot substitute evidence.
func (t *pgCommerceTx) GrantQuotedEntitlement(ctx context.Context, tenantID, siteID, purchaseID string) (string, string, error) {
	var eid string
	var already bool
	var superseded *string
	err := t.tx.QueryRow(ctx,
		`SELECT entitlement_id::text, already_granted, superseded::text
		   FROM iam_v2.p4_grant_quoted_entitlement($1::uuid,$2::uuid,$3::uuid)`,
		tenantID, siteID, purchaseID).Scan(&eid, &already, &superseded)
	if err != nil {
		return "", "", err
	}
	// Single use is enforced inside p4_entitlement_grant_kernel, not here: svc_scd has no UPDATE on
	// iam_v2.vouchers, and the kernel (owned by iam_v2_owner) already holds the per-subject advisory lock. A
	// grant against a non-UNUSED voucher raises VOUCHER_NOT_REDEEMABLE; an idempotent retry returns
	// already_granted before the burn.
	_ = already

	if superseded == nil {
		return eid, "", nil
	}
	return eid, *superseded, nil
}

func (t *pgCommerceTx) MarkPurchaseGranted(ctx context.Context, purchaseID string) error {
	// GRANTED is reached through the controlled function, which refuses any origin state the machine does not
	// permit (a raw UPDATE could move CANCELLED or FAILED to GRANTED).
	_, err := t.tx.Exec(ctx, `SELECT iam_v2.p4_mark_purchase_granted($1)`, purchaseID)
	return err
}

// Every value comes from rows the purchase already references; no parameter lets a caller substitute package,
// price or subject.
func (t *pgCommerceTx) LoadSettledPurchaseGrant(ctx context.Context, tenantID, siteID, purchaseID string) (SettledPurchaseGrant, error) {
	var g SettledPurchaseGrant
	var snap []byte
	var voucher, account, principal, existing *string
	var method string
	err := t.tx.QueryRow(ctx, `
SELECT p.id::text, p.state, p.package_revision_id::text,
       se.status, se.method,
       q.grant_snapshot,
       ac.method,
       ac.voucher_id::text, ac.guest_account_id::text, ac.guest_principal_id::text,
       (SELECT e.id::text FROM iam_v2.entitlements e WHERE e.purchase_id = p.id LIMIT 1)
  FROM iam_v2.purchases p
  JOIN iam_v2.settlements se ON se.tenant_id = p.tenant_id AND se.site_id = p.site_id AND se.purchase_id = p.id
  JOIN iam_v2.offer_quotes q ON q.tenant_id = p.tenant_id AND q.site_id = p.site_id AND q.id = p.offer_quote_id
  JOIN iam_v2.auth_contexts ac ON ac.tenant_id = p.tenant_id AND ac.site_id = p.site_id AND ac.id = q.auth_context_id
 WHERE p.tenant_id = $1 AND p.site_id = $2 AND p.id = $3`,
		tenantID, siteID, purchaseID).
		Scan(&g.PurchaseID, &g.PurchaseState, &g.PackageRevisionID, &g.SettlementStatus, &g.SettlementMethod,
			&snap, &method, &voucher, &account, &principal, &existing)
	if err == pgx.ErrNoRows {
		return SettledPurchaseGrant{}, &Error{Code: ErrSubjectResolve, Msg: "no such settled purchase in this tenant/site"}
	}
	if err != nil {
		return SettledPurchaseGrant{}, err
	}
	parsed, perr := ParseGrantSnapshot(snap)
	if perr != nil {
		return SettledPurchaseGrant{}, &Error{Code: ErrRepo, Msg: "grant snapshot is unreadable"}
	}
	g.GrantSnapshot = parsed
	g.Subject = CommerceSubject{Method: Method(method)}
	switch {
	case voucher != nil:
		g.Subject.Kind, g.Subject.VoucherID = SubjectVoucher, *voucher
	case account != nil:
		g.Subject.Kind, g.Subject.AccountID = SubjectAccount, *account
	case principal != nil:
		g.Subject.Kind, g.Subject.PrincipalID = SubjectPrincipal, *principal
	default:
		return SettledPurchaseGrant{}, &Error{Code: ErrSubjectResolve, Msg: "purchase has no grantable subject"}
	}
	if existing != nil {
		g.ExistingEntitlementID = *existing
	}
	return g, nil
}

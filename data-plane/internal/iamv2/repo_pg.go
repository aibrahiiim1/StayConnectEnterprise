package iamv2

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// PgRepository is a SCRATCH/TEST iam_v2 Repository. It is provided only when a scratch/enabled run
// supplies it; production never constructs or invokes it (flags OFF => Authenticator short-circuits
// before any repository call).
type PgRepository struct{ db *pgxpool.Pool }

// NewPgRepository builds a scratch Repository over the given pool (a disposable iam_v2 database).
func NewPgRepository(db *pgxpool.Pool) *PgRepository { return &PgRepository{db: db} }

// WithTx runs fn in a single transaction.
func (r *PgRepository) WithTx(ctx context.Context, fn func(Tx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// This repository's transactions issue and consume Auth Contexts.
	if err := writerguard.Open(ctx, tx, writerguard.CapAuthContext); err != nil {
		return err
	}
	if err := fn(&pgTx{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type pgTx struct{ tx pgx.Tx }

func (t *pgTx) ResolveVoucherByHMAC(ctx context.Context, tenantID, siteID string, codeHMAC []byte, now time.Time) (string, bool, error) {
	var id, state string
	var vf, vu *time.Time
	err := t.tx.QueryRow(ctx,
		`SELECT id::text, state, redemption_valid_from, redemption_valid_until
		   FROM iam_v2.vouchers
		  WHERE tenant_id=$1 AND site_id=$2 AND code_hmac=$3`,
		tenantID, siteID, codeHMAC).Scan(&id, &state, &vf, &vu)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, voucherRedeemable(state, vf, vu, now), nil
}

// voucherRedeemable implements the Phase 1B credential-validity rule for a voucher (pure; unit-tested).
// Canonical states: UNUSED | REDEEMED | REVOKED | REDEMPTION_EXPIRED. Credential-valid only when the
// state is exactly UNUSED and now is inside [redemption_valid_from, redemption_valid_until) — the
// upper bound is EXCLUSIVE. Phase 1B does NOT redeem or grant (no state mutation).
func voucherRedeemable(state string, vf, vu *time.Time, now time.Time) bool {
	if state != "UNUSED" {
		return false
	}
	if vf != nil && now.Before(*vf) {
		return false
	}
	if vu != nil && !now.Before(*vu) {
		return false
	}
	return true
}

func (t *pgTx) LookupAccount(ctx context.Context, tenantID, siteID, username string) (id, passwordHash string, enabled bool, vf, vu, locked *time.Time, err error) {
	e := t.tx.QueryRow(ctx,
		`SELECT id::text, password_hash, enabled, valid_from, valid_until, locked_until
		   FROM iam_v2.guest_access_accounts
		  WHERE tenant_id=$1 AND site_id=$2 AND lower(username)=lower($3)`,
		tenantID, siteID, username).Scan(&id, &passwordHash, &enabled, &vf, &vu, &locked)
	if e == pgx.ErrNoRows {
		return "", "", false, nil, nil, nil, nil
	}
	return id, passwordHash, enabled, vf, vu, locked, e
}

// resolveExistingPrincipal returns the principal for an existing identity, or "" if none.
func (t *pgTx) resolveExistingPrincipal(ctx context.Context, tenantID, factorType, issuer, valueNorm string) (string, error) {
	var pid string
	err := t.tx.QueryRow(ctx,
		`SELECT guest_principal_id::text FROM iam_v2.guest_principal_identities
		  WHERE tenant_id=$1 AND factor_type=$2 AND coalesce(factor_issuer,'')=$3 AND factor_value_norm=$4`,
		tenantID, factorType, issuer, valueNorm).Scan(&pid)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return pid, err
}

// ResolvePrincipalByIdentity finds or creates the principal for a verified factor identity. It is
// concurrency-idempotent: the new principal is created inside a SAVEPOINT, the identity is inserted
// ON CONFLICT DO NOTHING, and if a concurrent caller won the unique identity, the SAVEPOINT is rolled
// back (discarding this caller's orphan principal) and the winning principal is returned. All
// concurrent callers converge on ONE principal with no orphan and no unique-violation surfaced.
func (t *pgTx) ResolvePrincipalByIdentity(ctx context.Context, tenantID, factorType, issuer, valueNorm string, now time.Time) (string, error) {
	if pid, err := t.resolveExistingPrincipal(ctx, tenantID, factorType, issuer, valueNorm); err != nil || pid != "" {
		return pid, err
	}
	sp, err := t.tx.Begin(ctx) // nested tx == SAVEPOINT
	if err != nil {
		return "", err
	}
	var newPID string
	if err := sp.QueryRow(ctx,
		`INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`,
		tenantID).Scan(&newPID); err != nil {
		_ = sp.Rollback(ctx)
		return "", err
	}
	var wonPID string
	err = sp.QueryRow(ctx,
		`INSERT INTO iam_v2.guest_principal_identities
		     (tenant_id, guest_principal_id, factor_type, factor_issuer, factor_value_norm, verified_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (tenant_id, factor_type, factor_issuer, factor_value_norm) DO NOTHING
		 RETURNING guest_principal_id::text`,
		tenantID, newPID, factorType, issuer, valueNorm, now).Scan(&wonPID)
	if err == nil {
		// we won: keep the new principal + identity
		if cerr := sp.Commit(ctx); cerr != nil {
			return "", cerr
		}
		return wonPID, nil
	}
	if err != pgx.ErrNoRows {
		_ = sp.Rollback(ctx)
		return "", err
	}
	// a concurrent caller won the unique identity: discard our orphan principal and resolve theirs
	if rerr := sp.Rollback(ctx); rerr != nil {
		return "", rerr
	}
	return t.resolveExistingPrincipal(ctx, tenantID, factorType, issuer, valueNorm)
}

func (t *pgTx) UpsertDevice(ctx context.Context, tenantID, siteID, applianceID, mac, guestNetworkID, ip string, now time.Time) (string, error) {
	var id string
	if err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac, first_seen, last_seen, last_ip)
		 VALUES ($1,$2,nullif($3,'')::uuid,$4::macaddr,$6,$6,nullif($5,'')::inet)
		 ON CONFLICT (tenant_id, site_id, appliance_id, mac)
		 DO UPDATE SET last_seen=$6, last_ip=nullif($5,'')::inet
		 RETURNING id::text`,
		tenantID, siteID, applianceID, mac, ip, now).Scan(&id); err != nil {
		return "", err
	}
	if guestNetworkID != "" {
		// A device-appearance failure must roll back the whole device/auth-context transaction — never
		// return DecisionAllow with a partial device or auth_context.
		if _, err := t.tx.Exec(ctx,
			`INSERT INTO iam_v2.device_network_appearances (tenant_id, site_id, device_id, guest_network_id, first_seen, last_seen)
			 VALUES ($1,$2,$3,$4,$5,$5)
			 ON CONFLICT (device_id, guest_network_id) DO UPDATE SET last_seen=$5`,
			tenantID, siteID, id, guestNetworkID, now); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (t *pgTx) CreateAuthContext(ctx context.Context, s AuthContextSpec) (string, error) {
	var voucherID, accountID, principalID *string
	switch s.Method {
	case MethodVoucher:
		voucherID = &s.Subject.VoucherID
	case MethodAccount:
		accountID = &s.Subject.GuestAccountID
	case MethodOTP, MethodSocial:
		principalID = &s.Subject.PrincipalID
	}
	var dev, gn *string
	if s.DeviceID != "" {
		dev = &s.DeviceID
	}
	if s.GuestNetworkID != "" {
		gn = &s.GuestNetworkID
	}
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.auth_contexts
		     (tenant_id, site_id, method, voucher_id, guest_account_id, guest_principal_id,
		      device_id, guest_network_id, expires_at)
		 VALUES ($1,$2,$3,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8::uuid,$9)
		 RETURNING id::text`,
		s.TenantID, s.SiteID, string(s.Method), voucherID, accountID, principalID,
		dev, gn, s.Now.Add(s.TTL)).Scan(&id)
	return id, err
}

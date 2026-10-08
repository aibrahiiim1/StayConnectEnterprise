package iamv2

import (
	"context"
	"encoding/json"
	"strings"
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
	var live bool
	err := t.tx.QueryRow(ctx,
		`SELECT v.id::text, v.state, v.redemption_valid_from, v.redemption_valid_until,
		        EXISTS (SELECT 1 FROM iam_v2.entitlements e
		                 WHERE e.tenant_id = v.tenant_id AND e.site_id = v.site_id AND e.voucher_id = v.id
		                   AND e.status = 'ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now()))
		   FROM iam_v2.vouchers v
		  WHERE v.tenant_id=$1 AND v.site_id=$2 AND v.code_hmac=$3`,
		tenantID, siteID, codeHMAC).Scan(&id, &state, &vf, &vu, &live)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, voucherRedeemable(state, vf, vu, now) || voucherRejoinable(state, live), nil
}

// voucherRejoinable: a REDEEMED card whose own entitlement is still ACTIVE may sign a device in again -- to
// rejoin THAT entitlement, within its device limit, never to purchase. Single use holds where it matters:
// the grant kernel refuses any second redemption (VOUCHER_NOT_REDEEMABLE), so the card still yields exactly
// one entitlement and no fresh quota. Without this a guest who disconnected, or whose phone changed its
// address, could never get back online on access they still hold. Contract section 6.3: same-device
// reconnect replaces its own session; other devices take the entitlement's remaining slots.
func voucherRejoinable(state string, liveEntitlement bool) bool {
	return state == "REDEEMED" && liveEntitlement
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

// ResolvePrincipalByFactors finds or creates the Client for a verified primary factor and links the secondary
// factors the same proof established (identity.go). The order is the contract's: the primary factor, then each
// secondary, then a new Client. Concurrency-idempotent exactly as before: the new principal is created inside a
// SAVEPOINT, the primary identity is inserted ON CONFLICT DO NOTHING, and a lost race discards the orphan and
// returns the winner. A secondary factor that already belongs to ANOTHER Client is never moved and the two are
// never merged: it is reported as a conflict and the Client continues with the factor they proved.
func (t *pgTx) ResolvePrincipalByFactors(ctx context.Context, tenantID string, primary FactorClaim, secondary []FactorClaim, now time.Time) (PrincipalResolution, error) {
	primary.Value = normFactor(primary)
	if primary.Type == "" || primary.Value == "" {
		return PrincipalResolution{}, nil
	}
	// Serialise resolution per primary factor: two first sign-ins of the same new Client on two devices
	// otherwise race between "find" and "create" and the savepoint below has to arbitrate.
	if _, err := t.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('principal_resolve'), hashtext($1))`,
		tenantID+"|"+primary.Type+"|"+primary.Issuer+"|"+primary.Value); err != nil {
		return PrincipalResolution{}, err
	}
	var rs PrincipalResolution
	// 1. The factor just proven.
	pid, err := t.resolveExistingPrincipal(ctx, tenantID, primary.Type, primary.Issuer, primary.Value)
	if err != nil {
		return PrincipalResolution{}, err
	}
	// 2. The secondary factors the same proof established (and the lookup-only legacy rows).
	if pid == "" {
		for _, sec := range secondary {
			if pid, err = t.resolveExistingPrincipal(ctx, tenantID, sec.Type, sec.Issuer, normFactor(sec)); err != nil {
				return PrincipalResolution{}, err
			}
			if pid != "" {
				break
			}
		}
	}
	// 3. Otherwise a new Client.
	if pid == "" {
		sp, err := t.tx.Begin(ctx) // nested tx == SAVEPOINT
		if err != nil {
			return PrincipalResolution{}, err
		}
		var newPID string
		if err := sp.QueryRow(ctx,
			`INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`,
			tenantID).Scan(&newPID); err != nil {
			_ = sp.Rollback(ctx)
			return PrincipalResolution{}, err
		}
		var wonPID string
		err = sp.QueryRow(ctx,
			`INSERT INTO iam_v2.guest_principal_identities
			     (tenant_id, guest_principal_id, factor_type, factor_issuer, factor_value_norm, verified_at, attrs)
			 VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)
			 ON CONFLICT (tenant_id, factor_type, factor_issuer, factor_value_norm) DO NOTHING
			 RETURNING guest_principal_id::text`,
			tenantID, newPID, primary.Type, primary.Issuer, primary.Value, now, attrsJSON(primary.Attrs)).Scan(&wonPID)
		switch {
		case err == nil:
			if cerr := sp.Commit(ctx); cerr != nil {
				return PrincipalResolution{}, cerr
			}
			pid, rs.Created = wonPID, true
		case err == pgx.ErrNoRows:
			if rerr := sp.Rollback(ctx); rerr != nil {
				return PrincipalResolution{}, rerr
			}
			if pid, err = t.resolveExistingPrincipal(ctx, tenantID, primary.Type, primary.Issuer, primary.Value); err != nil {
				return PrincipalResolution{}, err
			}
		default:
			_ = sp.Rollback(ctx)
			return PrincipalResolution{}, err
		}
	}
	if pid == "" {
		return PrincipalResolution{}, nil
	}
	rs.PrincipalID = pid
	// 4. Attach what is missing: the primary (when the Client was found through a secondary) and every
	// insertable secondary. ON CONFLICT DO NOTHING; a factor held by another Client is a recorded conflict.
	attach := func(f FactorClaim) error {
		v := normFactor(f)
		if f.LookupOnly || f.Type == "" || v == "" {
			return nil
		}
		var owner string
		err := t.tx.QueryRow(ctx,
			`INSERT INTO iam_v2.guest_principal_identities
			     (tenant_id, guest_principal_id, factor_type, factor_issuer, factor_value_norm, verified_at, attrs)
			 VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)
			 ON CONFLICT (tenant_id, factor_type, factor_issuer, factor_value_norm) DO NOTHING
			 RETURNING guest_principal_id::text`,
			tenantID, pid, f.Type, f.Issuer, v, now, attrsJSON(f.Attrs)).Scan(&owner)
		if err == nil {
			rs.Linked = append(rs.Linked, f.Key())
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		// Exists. Ours: refresh the verified claims (tid/hd can be learned later). Another Client's: conflict.
		if owner, err = t.resolveExistingPrincipal(ctx, tenantID, f.Type, f.Issuer, v); err != nil {
			return err
		}
		if owner == pid {
			if len(f.Attrs) > 0 {
				_, err = t.tx.Exec(ctx,
					`UPDATE iam_v2.guest_principal_identities SET attrs = COALESCE(attrs,'{}'::jsonb) || $5::jsonb
					  WHERE tenant_id=$1 AND factor_type=$2 AND factor_issuer=$3 AND factor_value_norm=$4`,
					tenantID, f.Type, f.Issuer, v, attrsJSON(f.Attrs))
				return err
			}
			return nil
		}
		rs.Conflicts = append(rs.Conflicts, f.Key())
		return nil
	}
	if err := attach(primary); err != nil {
		return PrincipalResolution{}, err
	}
	for _, sec := range secondary {
		if err := attach(sec); err != nil {
			return PrincipalResolution{}, err
		}
	}
	return rs, nil
}

// normFactor applies the one normalisation the unique index relies on: a social subject is case-sensitive
// (it is the provider's own identifier), everything else is lower-cased.
func normFactor(f FactorClaim) string {
	v := strings.TrimSpace(f.Value)
	if f.Type != "SOCIAL_SUBJECT" {
		v = strings.ToLower(v)
	}
	return v
}

func attrsJSON(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// LoadPrincipalFactors returns every verified factor of a Client with its verified claims.
func (t *pgTx) LoadPrincipalFactors(ctx context.Context, tenantID, principalID string) ([]FactorClaim, error) {
	rows, err := t.tx.Query(ctx,
		`SELECT factor_type, factor_issuer, factor_value_norm, COALESCE(attrs,'{}'::jsonb)::text
		   FROM iam_v2.guest_principal_identities
		  WHERE tenant_id=$1 AND guest_principal_id=$2::uuid
		  ORDER BY verified_at, id`, tenantID, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FactorClaim
	for rows.Next() {
		var f FactorClaim
		var raw string
		if err := rows.Scan(&f.Type, &f.Issuer, &f.Value, &raw); err != nil {
			return nil, err
		}
		f.Attrs = map[string]string{}
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			for k, v := range m {
				if s, ok := v.(string); ok {
					f.Attrs[k] = s
				}
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// LoadClientGroups returns a site's groups with their rules, enabled and disabled alike (the evaluator skips
// the disabled ones; the Admin Console shows both).
func (t *pgTx) LoadClientGroups(ctx context.Context, tenantID, siteID string) ([]ClientGroup, error) {
	return loadClientGroups(ctx, t.tx, tenantID, siteID)
}

type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadClientGroups is shared with the commerce repository.
func loadClientGroups(ctx context.Context, q rowQuerier, tenantID, siteID string) ([]ClientGroup, error) {
	rows, err := q.Query(ctx,
		`SELECT g.id::text, g.name, g.priority, g.enabled, r.id::text, r.rule_type, COALESCE(r.rule_value,'{}'::jsonb)::text
		   FROM iam_v2.client_groups g
		   LEFT JOIN iam_v2.client_group_rules r ON r.tenant_id=g.tenant_id AND r.site_id=g.site_id AND r.group_id=g.id
		  WHERE g.tenant_id=$1 AND g.site_id=$2
		  ORDER BY g.priority, lower(g.name), g.id, r.created_at, r.id`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientGroup
	idx := map[string]int{}
	for rows.Next() {
		var g ClientGroup
		var rid, rtype, rval *string
		if err := rows.Scan(&g.ID, &g.Name, &g.Priority, &g.Enabled, &rid, &rtype, &rval); err != nil {
			return nil, err
		}
		i, seen := idx[g.ID]
		if !seen {
			out = append(out, g)
			i = len(out) - 1
			idx[g.ID] = i
		}
		if rid != nil && rtype != nil {
			var v map[string]any
			if rval != nil {
				_ = json.Unmarshal([]byte(*rval), &v)
			}
			out[i].Rules = append(out[i].Rules, ClientGroupRule{ID: *rid, Type: *rtype, Value: v})
		}
	}
	return out, rows.Err()
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
	var groupID, evidence *string
	if s.ClientGroupID != "" {
		g := s.ClientGroupID
		groupID = &g
		if len(s.ClientGroupEvidence) > 0 {
			if b, err := json.Marshal(s.ClientGroupEvidence); err == nil {
				e := string(b)
				evidence = &e
			}
		}
	}
	var id string
	err := t.tx.QueryRow(ctx,
		`INSERT INTO iam_v2.auth_contexts
		     (tenant_id, site_id, method, voucher_id, guest_account_id, guest_principal_id,
		      device_id, guest_network_id, expires_at, client_group_id, client_group_evidence)
		 VALUES ($1,$2,$3,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8::uuid,$9,$10::uuid,$11::jsonb)
		 RETURNING id::text`,
		s.TenantID, s.SiteID, string(s.Method), voucherID, accountID, principalID,
		dev, gn, s.Now.Add(s.TTL), groupID, evidence).Scan(&id)
	return id, err
}

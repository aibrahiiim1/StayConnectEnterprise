package auth

// Second-step locks per (operator, client address) with an account-wide backstop (AD-6, migration 0070).
//
// A wrong code is counted for the operator and the address it came from; signin_lock_failures of them lock the
// second step for that address only, so someone who merely knows a password cannot lock the real user out from
// elsewhere. operator_mfa.failed_codes keeps counting across every address and locks the whole account only at
// MFAAccountBackstopFactor times the same limit.

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MFAAccountBackstopFactor is how many times the per-address limit the account-wide counter allows.
const MFAAccountBackstopFactor = 4

type clientAddrKey struct{}

// WithClientAddr tells the second-step checks which client address the attempt came from.
func WithClientAddr(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, clientAddrKey{}, strings.TrimSpace(addr))
}

func clientAddr(ctx context.Context) string {
	a, _ := ctx.Value(clientAddrKey{}).(string)
	if a == "" {
		return "-"
	}
	return a
}

func (s *MFA) addrKey(ctx context.Context, operatorID string) string {
	return s.throttleMAC("mfa-addr:v1\x00" + strings.ToLower(operatorID) + "\x00" + clientAddr(ctx))
}

// addrLockedUntil reports a running lock for this address, if any.
func (s *MFA) addrLockedUntil(ctx context.Context, tx pgx.Tx, operatorID string) (*time.Time, error) {
	var until *time.Time
	err := tx.QueryRow(ctx, `SELECT max(locked_until) FROM operator_mfa_address_locks
	                          WHERE operator_id = $1::uuid AND key_hash = $2 AND locked_until > now()`,
		operatorID, s.addrKey(ctx, operatorID)).Scan(&until)
	return until, err
}

// addrFail counts one wrong code for this address and returns when it started a lock.
func (s *MFA) addrFail(ctx context.Context, tx pgx.Tx, operatorID string) (*time.Time, error) {
	key := s.addrKey(ctx, operatorID)
	var failures, limit, mins int
	if err := tx.QueryRow(ctx, `
		INSERT INTO operator_mfa_address_locks AS t (operator_id, key_hash, failures, last_failure_at)
		VALUES ($1::uuid, $2, 1, now())
		ON CONFLICT (operator_id, key_hash) DO UPDATE SET
		  failures = CASE WHEN t.locked_until IS NOT NULL AND t.locked_until <= now() THEN 1 ELSE t.failures + 1 END,
		  last_failure_at = now()
		RETURNING t.failures`, operatorID, key).Scan(&failures); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(signin_lock_failures), 5), COALESCE(max(signin_lock_minutes), 15)
	                              FROM central_security_settings WHERE id`).Scan(&limit, &mins); err != nil {
		return nil, err
	}
	var newly *time.Time
	if failures >= limit {
		if err := tx.QueryRow(ctx, `UPDATE operator_mfa_address_locks
		        SET failures = 0, locked_until = now() + make_interval(mins => $3)
		      WHERE operator_id = $1::uuid AND key_hash = $2 RETURNING locked_until`,
			operatorID, key, mins).Scan(&newly); err != nil {
			return nil, err
		}
	}
	_, _ = tx.Exec(ctx, `DELETE FROM operator_mfa_address_locks
	                      WHERE operator_id = $1::uuid AND last_failure_at < now() - interval '2 days'
	                        AND (locked_until IS NULL OR locked_until < now())`, operatorID)
	return newly, nil
}

// addrSucceeded clears this address's count after a right code.
func (s *MFA) addrSucceeded(ctx context.Context, tx pgx.Tx, operatorID string) error {
	_, err := tx.Exec(ctx, `DELETE FROM operator_mfa_address_locks WHERE operator_id = $1::uuid AND key_hash = $2`,
		operatorID, s.addrKey(ctx, operatorID))
	return err
}

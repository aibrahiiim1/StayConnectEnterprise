package iamv2

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// SessionEngine is the DARK Phase 1B session-after-grant boundary (D3). It is the interface the future
// session-after-grant path will use to bridge to the iam_v2 engine. Functional execution is
// SCRATCH/TEST ONLY: in production no enabled adapter is constructed and none of these methods is
// invoked (zero iam_v2 read/write). Phase 2 package/purchase/entitlement commerce is NOT implemented
// here.
type SessionEngine interface {
	// ConsumeAuthContext atomically consumes a one-time auth_context, validating TTL and one-time
	// state and method/subject coherence. Exactly one concurrent caller may win.
	ConsumeAuthContext(ctx context.Context, id string, expect Method, now time.Time) (Subject, error)
	// ReserveDeviceCapacity validates/reserves a device slot (iam_v2 reserve_device_slot). Scratch only.
	ReserveDeviceCapacity(ctx context.Context, tenantID, siteID, deviceID, entitlementID string, idempotencyKey string) error
	// StartSession creates a session after a proven entitlement grant. Scratch only.
	StartSession(ctx context.Context, spec SessionSpec) (sessionID string, err error)
	// CloseSession closes a session idempotently (iam_v2 close_session). Scratch only.
	CloseSession(ctx context.Context, sessionID string, now time.Time) error
	// IngestSample ingests a watermarked accounting sample (iam_v2 ingest_sample). Scratch only.
	IngestSample(ctx context.Context, sample AccountingSample) error
}

// SessionSpec describes a session to start after a grant (dark/scratch).
type SessionSpec struct {
	TenantID       string
	SiteID         string
	EntitlementID  string
	DeviceID       string
	AuthContextID  string
	IdempotencyKey string // deterministic; a repeat with the same key must not double-create
	Now            time.Time
}

// AccountingSample is a watermarked usage sample (dark/scratch).
type AccountingSample struct {
	SessionID string
	Seq       int64 // per-session monotonic watermark (idempotency)
	BytesUp   int64
	BytesDown int64
	Now       time.Time
}

// ConsumeAuthContext (Tx method) is the atomic one-time + TTL consume used by the session boundary.
// It is exposed on Tx so adapters/engine share one transaction. The UPDATE ... WHERE consumed_at IS
// NULL AND expires_at > now RETURNING makes exactly one concurrent caller win; a follow-up read
// classifies a miss as expired / already-consumed / mismatch / not-found (deterministic typed error).
func (t *pgTx) ConsumeAuthContext(ctx context.Context, id string, expect Method, now time.Time) (Subject, error) {
	var voucherID, accountID, principalID *string
	err := t.tx.QueryRow(ctx,
		`UPDATE iam_v2.auth_contexts
		    SET consumed_at=$2
		  WHERE id=$1 AND consumed_at IS NULL AND expires_at > $2 AND method=$3
		 RETURNING voucher_id::text, guest_account_id::text, guest_principal_id::text`,
		id, now, string(expect)).Scan(&voucherID, &accountID, &principalID)
	if err == nil {
		return subjectFrom(voucherID, accountID, principalID), nil
	}
	if err != pgx.ErrNoRows {
		return Subject{}, err
	}
	// classify the miss
	var method string
	var consumedAt, expiresAt *time.Time
	rerr := t.tx.QueryRow(ctx,
		`SELECT method, consumed_at, expires_at FROM iam_v2.auth_contexts WHERE id=$1`, id).
		Scan(&method, &consumedAt, &expiresAt)
	if rerr == pgx.ErrNoRows {
		return Subject{}, &Error{Code: ErrACNotFound}
	}
	if rerr != nil {
		return Subject{}, rerr
	}
	switch {
	case consumedAt != nil:
		return Subject{}, &Error{Code: ErrACConsumed}
	case expiresAt != nil && !expiresAt.After(now):
		return Subject{}, &Error{Code: ErrACExpired}
	case method != string(expect):
		return Subject{}, &Error{Code: ErrACMismatch}
	default:
		return Subject{}, &Error{Code: ErrConflict}
	}
}

func subjectFrom(voucherID, accountID, principalID *string) Subject {
	s := Subject{}
	if voucherID != nil {
		s.VoucherID = *voucherID
	}
	if accountID != nil {
		s.GuestAccountID = *accountID
	}
	if principalID != nil {
		s.PrincipalID = *principalID
	}
	return s
}

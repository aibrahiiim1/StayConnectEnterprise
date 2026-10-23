package iamv2

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ConsumeAuthContextRequest pins a consume to its namespace, device and network so a context is used only
// where it was created; no field may be substituted downstream.
type ConsumeAuthContextRequest struct {
	AuthContextID          string
	TenantID               string
	SiteID                 string
	ExpectedMethod         Method
	ExpectedDeviceID       string
	ExpectedGuestNetworkID string
	Now                    time.Time
}

// The SessionEngine must use these pins and accept no caller-supplied replacements.
type ConsumedContext struct {
	AuthContextID  string
	TenantID       string
	SiteID         string
	Method         Method
	Subject        Subject
	DeviceID       string
	GuestNetworkID string
}

// SessionEngine is the session-after-grant boundary; execution is scratch/test only, and in production no
// enabled adapter is constructed.
type SessionEngine interface {
	// ConsumeAuthContext atomically consumes a one-time auth_context pinned to its tenant/site/method/
	// device/guest-network; exactly one concurrent caller may win.
	ConsumeAuthContext(ctx context.Context, req ConsumeAuthContextRequest) (ConsumedContext, error)
	ReserveDeviceCapacity(ctx context.Context, tenantID, siteID, deviceID, entitlementID string, idempotencyKey string) error
	StartSession(ctx context.Context, spec SessionSpec) (sessionID string, err error)
	CloseSession(ctx context.Context, sessionID string, now time.Time) error
	IngestSample(ctx context.Context, sample AccountingSample) error
}

type SessionSpec struct {
	TenantID       string
	SiteID         string
	EntitlementID  string
	DeviceID       string
	AuthContextID  string
	IdempotencyKey string // deterministic; a repeat with the same key must not double-create
	Now            time.Time
}

type AccountingSample struct {
	SessionID string
	Seq       int64 // per-session monotonic watermark (idempotency)
	BytesUp   int64
	BytesDown int64
	Now       time.Time
}

// ConsumeAuthContext is the atomic one-time consume; a cross-tenant or cross-site lookup is
// indistinguishable from "not found" (the classification SELECT is scoped to tenant+site).
func (t *pgTx) ConsumeAuthContext(ctx context.Context, req ConsumeAuthContextRequest) (ConsumedContext, error) {
	var voucherID, accountID, principalID *string
	err := t.tx.QueryRow(ctx,
		`UPDATE iam_v2.auth_contexts
		    SET consumed_at=$2
		  WHERE id=$1 AND tenant_id=$3 AND site_id=$4 AND method=$5
		    AND device_id=$6 AND guest_network_id=$7
		    AND consumed_at IS NULL AND expires_at > $2
		 RETURNING voucher_id::text, guest_account_id::text, guest_principal_id::text`,
		req.AuthContextID, req.Now, req.TenantID, req.SiteID, string(req.ExpectedMethod),
		req.ExpectedDeviceID, req.ExpectedGuestNetworkID).Scan(&voucherID, &accountID, &principalID)
	if err == nil {
		return ConsumedContext{
			AuthContextID: req.AuthContextID, TenantID: req.TenantID, SiteID: req.SiteID,
			Method: req.ExpectedMethod, Subject: subjectFrom(voucherID, accountID, principalID),
			DeviceID: req.ExpectedDeviceID, GuestNetworkID: req.ExpectedGuestNetworkID,
		}, nil
	}
	if err != pgx.ErrNoRows {
		return ConsumedContext{}, err
	}
	var method, deviceID, gnID string
	var consumedAt, expiresAt *time.Time
	rerr := t.tx.QueryRow(ctx,
		`SELECT method, device_id::text, guest_network_id::text, consumed_at, expires_at
		   FROM iam_v2.auth_contexts WHERE id=$1 AND tenant_id=$2 AND site_id=$3`,
		req.AuthContextID, req.TenantID, req.SiteID).Scan(&method, &deviceID, &gnID, &consumedAt, &expiresAt)
	if rerr == pgx.ErrNoRows {
		return ConsumedContext{}, &Error{Code: ErrACNotFound}
	}
	if rerr != nil {
		return ConsumedContext{}, rerr
	}
	switch {
	case consumedAt != nil:
		return ConsumedContext{}, &Error{Code: ErrACConsumed}
	case expiresAt != nil && !expiresAt.After(req.Now):
		return ConsumedContext{}, &Error{Code: ErrACExpired}
	case method != string(req.ExpectedMethod):
		return ConsumedContext{}, &Error{Code: ErrACMismatch}
	case deviceID != req.ExpectedDeviceID || gnID != req.ExpectedGuestNetworkID:
		return ConsumedContext{}, &Error{Code: ErrACMismatch}
	default:
		return ConsumedContext{}, &Error{Code: ErrConflict}
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

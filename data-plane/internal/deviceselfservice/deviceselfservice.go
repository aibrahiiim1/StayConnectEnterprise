// Package deviceselfservice lets an authenticated guest list the devices on their own entitlement and release
// an offline one.
//
//   - It accepts no subject identifier (MAC, room, stay, entitlement...); the caller's entitlement is resolved
//     from the authenticated context by the layer above.
//   - It does not decide whether a device may be released: the offline check and removal must be atomic, so both
//     happen inside iam_v2.p6_guest_release_device.
//   - It never explains a refusal to the guest, so "not yours" and "does not exist" are indistinguishable; the
//     reason goes to the durable audit.
//
// Both the deployment gate and the per-appliance product setting must permit the surface; they are separate
// questions and never collapsed.
package deviceselfservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Outcome string

const (
	OutcomeOK               Outcome = "OK"
	OutcomeRefusedOnline    Outcome = "REFUSED_ONLINE"
	OutcomeRefusedNotFound  Outcome = "REFUSED_NOT_FOUND"
	OutcomeRefusedAlready   Outcome = "REFUSED_ALREADY_RELEASED"
	OutcomeRefusedDisabled  Outcome = "REFUSED_DISABLED"
	OutcomeRefusedThrottled Outcome = "REFUSED_THROTTLED"
)

// Released is false for every refusal; callers treat all refusals identically on the guest surface.
func (o Outcome) Released() bool { return o == OutcomeOK }

// ErrDisabled is not a refusal outcome, so callers cannot render "you may not remove that device" when the hotel
// does not offer device management at all.
var ErrDisabled = errors.New("guest device self-service is not enabled on this appliance")

// Device deliberately carries no MAC address: a stable network identifier would be exposed if the listing were
// ever mis-scoped.
type Device struct {
	ID       string
	LastSeen *time.Time
	Online   bool
	// Removable is advisory (true when the list was built); the release path re-decides under lock.
	Removable bool
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// EnabledForAppliance is a local read with no Central call, so it works without an uplink. A missing row is
// off: defaulting to true would silently enable the feature on every unconfigured appliance.
func (s *Service) EnabledForAppliance(ctx context.Context, tenant, site, appliance string) (bool, error) {
	var on bool
	err := s.pool.QueryRow(ctx, `SELECT guest_device_self_service
		FROM iam_v2.appliance_product_settings
		WHERE tenant_id=$1 AND site_id=$2 AND appliance_id=$3`, tenant, site, appliance).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read guest_device_self_service: %w", err)
	}
	return on, nil
}

// SetForAppliance scope and actor are the server's, never from a request body: tenant/site/appliance come from
// the local assignment, operatorID from the session, and foreign keys refuse anything invented.
func (s *Service) SetForAppliance(ctx context.Context, tenant, site, appliance string,
	on bool, operatorID, operatorLabel, reason string) error {
	// One controlled operation: svc_edged has no direct write on either table, so a setting cannot move
	// without its audit.
	_, err := s.pool.Exec(ctx,
		`SELECT iam_v2.p6_set_guest_device_self_service($1,$2,$3,$4,$5,$6,$7)`,
		tenant, site, appliance, on, operatorID, operatorLabel, nullIfEmpty(reason))
	if err != nil {
		return fmt.Errorf("set guest_device_self_service: %w", err)
	}
	return nil
}

// ListOwnDevices lists devices on the caller's own entitlement, scoped in the WHERE clause. Listing is not
// audited (only RELEASE changes durable state; auditing reads would need a write grant on the audit table).
// Released bindings are excluded: the list means "what is using your allowance". Every row is AUTHORIZED, so
// Removable reduces to "not online".
func (s *Service) ListOwnDevices(ctx context.Context, entitlementID string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id::text,
		       d.last_seen,
		       (live.n > 0) AS online,
		       (live.n = 0) AS removable
		  FROM iam_v2.entitlement_devices ed
		  JOIN iam_v2.devices d ON d.id = ed.device_id
		  CROSS JOIN LATERAL (
		      SELECT count(*) AS n FROM iam_v2.sessions se
		       WHERE se.entitlement_id = ed.entitlement_id AND se.device_id = ed.device_id
		         AND se.state IN ('active','PENDING_ENFORCEMENT')) live
		 WHERE ed.entitlement_id = $1
		   AND ed.status = 'AUTHORIZED'
		 ORDER BY d.last_seen DESC NULLS LAST, d.id`, entitlementID)
	if err != nil {
		return nil, fmt.Errorf("list own devices: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.LastSeen, &d.Online, &d.Removable); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Release delegates the whole decision to the database, which alone can read offline state, binding status
// and throttle atomically; this function must hold no logic that could disagree.
func (s *Service) Release(ctx context.Context, entitlementID, deviceID string) (Outcome, error) {
	// Use the policy entry point, which takes no throttle parameter: a caller able to pass its own hourly limit
	// could bypass the policy, so the three-argument primitive is not granted to runtime roles.
	var out string
	if err := s.pool.QueryRow(ctx,
		`SELECT iam_v2.p6_guest_release_device_policy($1,$2)`, entitlementID, deviceID).Scan(&out); err != nil {
		return "", fmt.Errorf("release device: %w", err)
	}
	return Outcome(out), nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// EntitlementForDevice is the whole subject derivation: the device identity is server-resolved from source and
// hardware address, and the entitlement is the live one holding an AUTHORIZED binding for it. A device with
// none gets ErrNoEntitlement, which callers must render like every other refusal.
func (s *Service) EntitlementForDevice(ctx context.Context, tenant, site, device string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT e.id::text
		  FROM iam_v2.entitlement_devices ed
		  JOIN iam_v2.entitlements e ON e.id = ed.entitlement_id
		 WHERE ed.tenant_id = $1 AND ed.site_id = $2 AND ed.device_id = $3
		   AND ed.status = 'AUTHORIZED'
		   AND e.status IN ('ACTIVE','PENDING','SUSPENDED')
		 ORDER BY ed.last_authorized DESC NULLS LAST
		 LIMIT 1`, tenant, site, device).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoEntitlement
	}
	if err != nil {
		return "", fmt.Errorf("resolve entitlement for device: %w", err)
	}
	return id, nil
}

// ErrNoEntitlement is distinct from a refusal for logging only; it must not be distinguishable to the guest.
var ErrNoEntitlement = errors.New("no live authorized entitlement for this device")

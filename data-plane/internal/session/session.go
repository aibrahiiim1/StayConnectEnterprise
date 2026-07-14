// Package session manages guest session rows in the control-plane DB.
package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Manager struct {
	DB          *pgxpool.Pool
	TenantID    string
	SiteID      string
	ApplianceID string

	// LicensedLimit returns the appliance-wide cap on concurrently authorized
	// guest sessions from the LOCAL signed license (-1 = unlimited). It is
	// evaluated inside the same transaction that inserts the session row,
	// under an advisory lock, so simultaneous logins can never exceed the cap.
	// nil = no licensing wired (dev) = unlimited.
	LicensedLimit func() int64
}

// CapacityError is returned when the licensed concurrent-online-guest cap is
// reached. Handlers surface it as LICENSE_CAPACITY_REACHED; nothing (nft,
// shaping, accounting, session row) is created for the rejected guest.
type CapacityError struct {
	Limit   int64
	Current int64
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("LICENSE_CAPACITY_REACHED: %d of %d concurrent online guests in use", e.Current, e.Limit)
}

// MaxDevicesError is returned when the per-credential Access Plan max_devices
// limit is reached. Handlers surface it as MAX_DEVICES_REACHED; like
// CapacityError, nothing (nft, shaping, accounting, session row, voucher
// activation) is created for the rejected device.
type MaxDevicesError struct {
	Limit   int64
	Current int64
}

func (e *MaxDevicesError) Error() string {
	return fmt.Sprintf("MAX_DEVICES_REACHED: %d of %d devices already active for this credential", e.Current, e.Limit)
}

// reserveDeviceSlot enforces the Access Plan max_devices limit for one
// credential (a voucher or a guest account) ATOMICALLY, inside tx.
//
// Canonical device identity is the guest MAC. The steps, all under a
// per-credential advisory lock so two concurrent logins for the SAME
// credential are serialized (a race can never exceed the limit):
//
//  1. Take a per-credential xact advisory lock (a different key namespace than
//     the appliance-capacity lock, and always acquired BEFORE it, so lock
//     ordering is identical on every path — no deadlock).
//  2. Close any existing active session for THIS credential on THIS device — a
//     reconnect/retry from an already-authorized device replaces its own
//     session and does NOT consume a second slot (idempotent).
//  3. Count the DISTINCT other devices still active on this credential. If that
//     already meets the limit, a genuinely new device is rejected.
//
// credCol is a fixed internal identifier ("voucher_id" or "guest_account_id"),
// never user input, so interpolating it is safe. maxDevices <= 0 means the plan
// does not cap devices (unlimited) and the check is skipped.
func (m *Manager) reserveDeviceSlot(ctx context.Context, tx pgx.Tx, credCol, credID string, mac net.HardwareAddr, maxDevices int64) error {
	if maxDevices <= 0 || credID == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 11))`, credID); err != nil {
		return fmt.Errorf("device-slot lock: %w", err)
	}
	// A reconnect from the same device replaces its own prior session so it is
	// not double-counted. Rolled back with the tx if we then reject.
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
        UPDATE sessions SET state='closed', ended_at=now(), end_reason='policy'
         WHERE %s = $1 AND mac = $2::macaddr AND state='active'`, credCol),
		credID, mac.String()); err != nil {
		return fmt.Errorf("device-slot reconcile: %w", err)
	}
	var current int64
	if err := tx.QueryRow(ctx, fmt.Sprintf(
		`SELECT count(DISTINCT mac) FROM sessions WHERE %s = $1 AND state='active'`, credCol),
		credID).Scan(&current); err != nil {
		return fmt.Errorf("device-slot count: %w", err)
	}
	if current >= maxDevices {
		return &MaxDevicesError{Limit: maxDevices, Current: current}
	}
	return nil
}

// ActiveDevices counts the DISTINCT devices (MACs) currently active for a
// credential. credCol must be "voucher_id" or "guest_account_id".
func (m *Manager) ActiveDevices(ctx context.Context, credCol, credID string) (int64, error) {
	if credCol != "voucher_id" && credCol != "guest_account_id" {
		return 0, fmt.Errorf("bad credential column")
	}
	var n int64
	err := m.DB.QueryRow(ctx, fmt.Sprintf(
		`SELECT count(DISTINCT mac) FROM sessions WHERE %s = $1 AND state='active'`, credCol),
		credID).Scan(&n)
	return n, err
}

// gateCapacity enforces the licensed cap ATOMICALLY: it takes a per-APPLIANCE
// advisory lock inside tx (serializing concurrent authorizations for THIS
// appliance only — the second login waits for the first to commit, then sees
// its row), counts this appliance's currently active sessions across ALL its
// guest VLANs/networks, and fails when the cap is reached. The lock/count key
// is the appliance_id (the license scope), so two appliances under the same
// customer never block each other or leak capacity. Fully local — no Central
// round-trip.
func (m *Manager) gateCapacity(ctx context.Context, tx pgx.Tx) error {
	if m.LicensedLimit == nil {
		return nil
	}
	limit := m.LicensedLimit()
	if limit < 0 {
		return nil // unlimited
	}
	// The licensed cap is per-APPLIANCE (one license per appliance). Lock and
	// count on THIS appliance's id so two appliances under the same customer
	// authorize guests concurrently, each enforcing only its own limit — no
	// cross-appliance serialization or capacity leakage.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 7))`, m.ApplianceID); err != nil {
		return fmt.Errorf("capacity lock: %w", err)
	}
	var current int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE appliance_id = $1 AND state = 'active'`,
		m.ApplianceID).Scan(&current); err != nil {
		return fmt.Errorf("capacity count: %w", err)
	}
	if current >= limit {
		return &CapacityError{Limit: limit, Current: current}
	}
	return nil
}

// ActiveCount returns the number of currently active guest sessions across the
// whole appliance (all VLANs/networks).
func (m *Manager) ActiveCount(ctx context.Context) (int64, error) {
	var n int64
	err := m.DB.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE appliance_id = $1 AND state = 'active'`, m.ApplianceID).Scan(&n)
	return n, err
}

// ConcurrencyStatus holds the current / limit values for max_concurrent_devices.
// Configured distinguishes "limit not set" (allow) from "limit is 0" (block).
type ConcurrencyStatus struct {
	Configured bool
	Limit      int64 // -1 = unlimited
	Current    int64
}

// CheckConcurrency returns the current active-session count and the tenant
// plan limit. Callers should reject new authorizations when Limit > 0 and
// Current >= Limit.
func (m *Manager) CheckConcurrency(ctx context.Context) (ConcurrencyStatus, error) {
	var out ConcurrencyStatus
	// Read the limit from the merged effective view. Unlike the control-plane
	// helper, we don't fail when the tenant has no subscription — the data
	// plane is reachable only to already-enrolled tenants.
	var lim *int64
	err := m.DB.QueryRow(ctx, `
        SELECT int_value FROM tenant_effective_limits
         WHERE tenant_id = $1 AND key = 'max_concurrent_devices' AND value_type = 'int'
         LIMIT 1
    `, m.TenantID).Scan(&lim)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("lookup limit: %w", err)
	}
	if lim != nil {
		out.Configured = true
		out.Limit = *lim
	}
	if err := m.DB.QueryRow(ctx, `
        SELECT count(*) FROM sessions
         WHERE tenant_id = $1 AND state = 'active'
    `, m.TenantID).Scan(&out.Current); err != nil {
		return out, fmt.Errorf("count active: %w", err)
	}
	return out, nil
}

type Authorized struct {
	SessionID string
	GuestID   string
	ExpiresAt *time.Time
}

// Start upserts the guest, creates a session, and returns identifiers.
// maxDevices is the voucher's Access Plan max_concurrent_devices (<=0 =
// unlimited); it is enforced atomically alongside the licensed capacity.
func (m *Manager) Start(ctx context.Context, mac net.HardwareAddr, ip net.IP, voucherID string, maxDevices, durationSeconds int) (*Authorized, error) {
	tx, err := m.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var guestID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO guests (tenant_id, mac, last_seen_at)
        VALUES ($1, $2::macaddr, now())
        ON CONFLICT (tenant_id, mac) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at
        RETURNING id
    `, m.TenantID, mac.String()).Scan(&guestID); err != nil {
		return nil, fmt.Errorf("upsert guest: %w", err)
	}

	// Per-credential max-devices gate, then licensed-capacity gate, then session
	// creation — all one atomic unit. Locks are always acquired credential-first,
	// appliance-second, so ordering is identical on every path.
	if err := m.reserveDeviceSlot(ctx, tx, "voucher_id", voucherID, mac, int64(maxDevices)); err != nil {
		return nil, err
	}
	if err := m.gateCapacity(ctx, tx); err != nil {
		return nil, err
	}
	expiresAt := computeExpiresAt(durationSeconds)
	var sessionID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO sessions (tenant_id, site_id, appliance_id, guest_id, voucher_id, ip, mac, state, expires_at)
        VALUES ($1, $2, $3, $4, NULLIF($5,'')::uuid, $6::inet, $7::macaddr, 'active', $8)
        RETURNING id
    `, m.TenantID, m.SiteID, m.ApplianceID, guestID, voucherID, ip.String(), mac.String(), expiresAt).Scan(&sessionID); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	out := &Authorized{SessionID: sessionID, GuestID: guestID, ExpiresAt: expiresAt}
	return out, nil
}

// computeExpiresAt returns nil for "no time limit" (durationSeconds == 0)
// or a pointer to now+duration. Returning a pointer lets pgx encode NULL
// for unlimited sessions.
func computeExpiresAt(durationSeconds int) *time.Time {
	if durationSeconds <= 0 {
		return nil
	}
	t := time.Now().Add(time.Duration(durationSeconds) * time.Second)
	return &t
}

// StartOTP creates a session backed by an OTP-verified contact identifier
// (email/phone) instead of a voucher. The guest row is upserted by MAC and
// has its email/phone field stamped + verified_at set.
func (m *Manager) StartOTP(ctx context.Context, mac net.HardwareAddr, ip net.IP, channel, destination string, durationSeconds int) (*Authorized, error) {
	tx, err := m.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Upsert guest by MAC; stamp the contact identifier & verified_at.
	field := "email"
	if channel == "sms" {
		field = "phone"
	}
	var guestID string
	q := fmt.Sprintf(`
        INSERT INTO guests (tenant_id, mac, %s, %s_verified_at, last_seen_at)
        VALUES ($1, $2::macaddr, $3, now(), now())
        ON CONFLICT (tenant_id, mac) DO UPDATE
          SET %s = EXCLUDED.%s,
              %s_verified_at = now(),
              last_seen_at = now()
        RETURNING id
    `, field, field, field, field, field)
	if err := tx.QueryRow(ctx, q, m.TenantID, mac.String(), destination).Scan(&guestID); err != nil {
		return nil, fmt.Errorf("upsert guest (otp): %w", err)
	}

	// Licensed-capacity gate + session creation are one atomic unit.
	if err := m.gateCapacity(ctx, tx); err != nil {
		return nil, err
	}
	expiresAt := computeExpiresAt(durationSeconds)
	var sessionID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO sessions (tenant_id, site_id, appliance_id, guest_id, ip, mac, state, expires_at)
        VALUES ($1, $2, $3, $4, $5::inet, $6::macaddr, 'active', $7)
        RETURNING id
    `, m.TenantID, m.SiteID, m.ApplianceID, guestID, ip.String(), mac.String(), expiresAt).Scan(&sessionID); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Authorized{SessionID: sessionID, GuestID: guestID, ExpiresAt: expiresAt}, nil
}

// StartGuestAccount creates a session for a guest validated by username/password.
// Mirrors StartOTP: upsert guest by MAC, atomic licensed-capacity gate, insert the
// session with guest_account_id set (and no voucher). Uses the exact same
// production pipeline and capacity reservation as every other method.
func (m *Manager) StartGuestAccount(ctx context.Context, mac net.HardwareAddr, ip net.IP, accountID, displayName string, maxDevices, durationSeconds int) (*Authorized, error) {
	tx, err := m.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var guestID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO guests (tenant_id, mac, display_name, last_seen_at)
        VALUES ($1, $2::macaddr, NULLIF($3,''), now())
        ON CONFLICT (tenant_id, mac) DO UPDATE
          SET display_name = COALESCE(NULLIF(EXCLUDED.display_name,''), guests.display_name),
              last_seen_at = now()
        RETURNING id
    `, m.TenantID, mac.String(), displayName).Scan(&guestID); err != nil {
		return nil, fmt.Errorf("upsert guest (account): %w", err)
	}
	// Per-credential max-devices gate, then licensed-capacity gate (same lock
	// order as the voucher path).
	if err := m.reserveDeviceSlot(ctx, tx, "guest_account_id", accountID, mac, int64(maxDevices)); err != nil {
		return nil, err
	}
	if err := m.gateCapacity(ctx, tx); err != nil {
		return nil, err
	}
	expiresAt := computeExpiresAt(durationSeconds)
	var sessionID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO sessions (tenant_id, site_id, appliance_id, guest_id, guest_account_id, ip, mac, state, expires_at)
        VALUES ($1, $2, $3, $4, $5, $6::inet, $7::macaddr, 'active', $8)
        RETURNING id
    `, m.TenantID, m.SiteID, m.ApplianceID, guestID, accountID, ip.String(), mac.String(), expiresAt).Scan(&sessionID); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Authorized{SessionID: sessionID, GuestID: guestID, ExpiresAt: expiresAt}, nil
}

// StartPMS creates a session for a guest validated against a PMS. Updates
// guest row by MAC; if reservationID is non-empty we also store it in
// metadata for the audit trail / future loyalty hooks.
func (m *Manager) StartPMS(ctx context.Context, mac net.HardwareAddr, ip net.IP, roomNumber, displayName, reservationID string, durationSeconds int) (*Authorized, error) {
	tx, err := m.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var guestID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO guests (tenant_id, mac, display_name, last_seen_at)
        VALUES ($1, $2::macaddr, NULLIF($3,''), now())
        ON CONFLICT (tenant_id, mac) DO UPDATE
          SET display_name = COALESCE(NULLIF(EXCLUDED.display_name,''), guests.display_name),
              last_seen_at = EXCLUDED.last_seen_at
        RETURNING id
    `, m.TenantID, mac.String(), displayName).Scan(&guestID); err != nil {
		return nil, fmt.Errorf("upsert guest (pms): %w", err)
	}

	// Licensed-capacity gate + session creation are one atomic unit.
	if err := m.gateCapacity(ctx, tx); err != nil {
		return nil, err
	}
	expiresAt := computeExpiresAt(durationSeconds)
	var sessionID string
	if err := tx.QueryRow(ctx, `
        INSERT INTO sessions (tenant_id, site_id, appliance_id, guest_id, ip, mac, state, expires_at)
        VALUES ($1, $2, $3, $4, $5::inet, $6::macaddr, 'active', $7)
        RETURNING id
    `, m.TenantID, m.SiteID, m.ApplianceID, guestID, ip.String(), mac.String(), expiresAt).Scan(&sessionID); err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out := &Authorized{SessionID: sessionID, GuestID: guestID, ExpiresAt: expiresAt}
	_ = roomNumber
	_ = reservationID
	return out, nil
}

// End closes the active session for the given IP (if any).
func (m *Manager) End(ctx context.Context, ip net.IP, reason string) error {
	_, err := m.DB.Exec(ctx, `
        UPDATE sessions
           SET state = 'closed', ended_at = now(), end_reason = $2
         WHERE tenant_id = $1 AND ip = $3::inet AND state = 'active'
    `, m.TenantID, reason, ip.String())
	return err
}

// FindActive returns session state for a given IP, if any.
func (m *Manager) FindActive(ctx context.Context, ip net.IP) (string, bool, error) {
	var id string
	err := m.DB.QueryRow(ctx, `
        SELECT id FROM sessions
         WHERE tenant_id = $1 AND ip = $2::inet AND state = 'active'
         ORDER BY started_at DESC LIMIT 1
    `, m.TenantID, ip.String()).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

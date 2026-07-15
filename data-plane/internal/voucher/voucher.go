// Package voucher validates + consumes vouchers against the control-plane DB.
//
// Phase 1 keeps this simple: the appliance talks to Postgres directly. Phase 5
// splits planes and introduces a local SQLite cache + NATS sync.
package voucher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// normalizeCode mirrors control-plane's crockford.Normalize: strip whitespace
// and dashes, uppercase, fold I/L→1, O→0, drop U. Kept here to avoid a
// cross-module dependency from the appliance on the control-plane module.
func normalizeCode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '-' || r == ' ' || r == '\t' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			r -= 32
		}
		switch r {
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		case 'U':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var (
	ErrNotFound  = errors.New("voucher not found")
	ErrExpired   = errors.New("voucher expired")
	ErrExhausted = errors.New("voucher exhausted")
	ErrRevoked   = errors.New("voucher revoked")
)

type Redeemed struct {
	VoucherID       string
	TemplateID      string
	TenantID        string
	DurationSeconds int // remaining seconds for this session (0 = unlimited)
	DataCapBytes    int64
	DownKbps        int
	UpKbps          int
	MaxDevices      int // plan max_concurrent_devices (<=0 = unlimited)
}

type Store struct {
	DB *pgxpool.Pool
}

// Validate fetches & evaluates a voucher. Does not mutate.
// code is normalized (dashes/whitespace stripped, uppercased, I→1 L→1 O→0)
// to tolerate print-format input like XXXX-XXXX-XXXX.
//
// Duration model (canonical): a voucher's plan Duration is a VALIDITY WINDOW
// that opens at the first successful activation and runs on wall-clock time.
// `vouchers.expires_at` (stamped once by Activate) is the durable window end —
// device count, reconnects, crashes and reboots never move it, so a "10-minute"
// voucher yields 10 minutes total, shared across all its allowed devices. The
// plan Data cap is an AGGREGATE across every session/device, evaluated live as
// SUM(bytes) over the voucher's sessions. Neither quantity is accrued on close,
// so usage can never be double-counted (see the session package).
func (s *Store) Validate(ctx context.Context, tenantID, code string) (*Redeemed, error) {
	code = normalizeCode(code)
	row := s.DB.QueryRow(ctx, `
        SELECT v.id, v.template_id, v.tenant_id, v.state, v.activated_at, v.expires_at,
               t.duration_seconds, t.data_cap_bytes, t.down_kbps, t.up_kbps,
               t.max_concurrent_devices,
               COALESCE((SELECT SUM(se.bytes_up + se.bytes_down)
                           FROM sessions se WHERE se.voucher_id = v.id), 0)
          FROM vouchers v
          JOIN ticket_templates t ON t.id = v.template_id
         WHERE v.tenant_id = $1 AND v.code = $2
    `, tenantID, code)

	var (
		id, tplID, tenID, state string
		activatedAt, windowEnd  *time.Time
		duration                *int
		dataCap                 *int64
		down, up                *int
		maxDevices              int
		usedBytes               int64
	)
	if err := row.Scan(&id, &tplID, &tenID, &state, &activatedAt, &windowEnd,
		&duration, &dataCap, &down, &up, &maxDevices, &usedBytes); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query voucher: %w", err)
	}

	switch state {
	case "revoked":
		return nil, ErrRevoked
	case "expired":
		return nil, ErrExpired
	case "exhausted":
		return nil, ErrExhausted
	}

	now := time.Now()
	// Time: validity window from first activation. windowEnd (vouchers.expires_at)
	// is the durable window; for a LEGACY voucher activated before windows were
	// stamped, derive it from activated_at + plan duration so it stays correct.
	if windowEnd == nil && activatedAt != nil && duration != nil {
		w := activatedAt.Add(time.Duration(*duration) * time.Second)
		windowEnd = &w
	}
	remaining := 0
	switch {
	case windowEnd != nil:
		if !now.Before(*windowEnd) {
			return nil, ErrExpired // window closed → time-exhausted
		}
		remaining = int(windowEnd.Sub(now).Seconds())
		if remaining <= 0 {
			return nil, ErrExpired
		}
	case activatedAt == nil:
		// Never activated: the window opens on first activation; grant the full
		// plan duration (0/nil duration = unlimited time).
		if duration != nil {
			remaining = *duration
		}
	default:
		// Activated on an unlimited-duration plan: no time cap.
	}

	r := &Redeemed{
		VoucherID:       id,
		TemplateID:      tplID,
		TenantID:        tenID,
		DurationSeconds: remaining,
		MaxDevices:      maxDevices,
	}
	// Data: aggregate across all sessions/devices.
	if dataCap != nil {
		r.DataCapBytes = *dataCap - usedBytes
		if r.DataCapBytes <= 0 {
			return nil, ErrExhausted
		}
	}
	if down != nil {
		r.DownKbps = *down
	}
	if up != nil {
		r.UpKbps = *up
	}
	return r, nil
}

// LoadTemplate returns the parameters of a ticket_template directly. Used
// by OTP/social auth flows that don't have a voucher but still apply the
// template's duration/data/bandwidth caps to the resulting session.
func (s *Store) LoadTemplate(ctx context.Context, templateID string) (*Redeemed, error) {
	var (
		duration   *int
		dataCap    *int64
		down, up   *int
		maxDevices int
	)
	err := s.DB.QueryRow(ctx, `
        SELECT duration_seconds, data_cap_bytes, down_kbps, up_kbps, max_concurrent_devices
          FROM ticket_templates WHERE id = $1
    `, templateID).Scan(&duration, &dataCap, &down, &up, &maxDevices)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r := &Redeemed{TemplateID: templateID, MaxDevices: maxDevices}
	if duration != nil {
		r.DurationSeconds = *duration
	}
	if dataCap != nil {
		r.DataCapBytes = *dataCap
	}
	if down != nil {
		r.DownKbps = *down
	}
	if up != nil {
		r.UpKbps = *up
	}
	return r, nil
}

// Activate flips voucher.state to 'active', stamps activated_at, and — on the
// FIRST activation — opens the durable validity window by stamping expires_at =
// now + plan.duration_seconds. COALESCE makes this idempotent: later
// redemptions/reconnects keep the original window, so the clock can never be
// reset. A plan with no duration (unlimited time) leaves expires_at NULL.
func (s *Store) Activate(ctx context.Context, voucherID string) error {
	_, err := s.DB.Exec(ctx, `
        UPDATE vouchers v
           SET state = 'active',
               activated_at = COALESCE(v.activated_at, now()),
               expires_at = COALESCE(v.expires_at,
                   CASE WHEN t.duration_seconds IS NOT NULL
                        THEN COALESCE(v.activated_at, now()) + make_interval(secs => t.duration_seconds)
                        ELSE NULL END)
          FROM ticket_templates t
         WHERE v.id = $1 AND t.id = v.template_id AND v.state IN ('unused','active')
    `, voucherID)
	return err
}

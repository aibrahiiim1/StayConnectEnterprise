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
func (s *Store) Validate(ctx context.Context, tenantID, code string) (*Redeemed, error) {
	code = normalizeCode(code)
	row := s.DB.QueryRow(ctx, `
        SELECT v.id, v.template_id, v.tenant_id, v.state, v.expires_at,
               v.bytes_used, v.seconds_used,
               t.duration_seconds, t.data_cap_bytes, t.down_kbps, t.up_kbps,
               t.max_concurrent_devices
          FROM vouchers v
          JOIN ticket_templates t ON t.id = v.template_id
         WHERE v.tenant_id = $1 AND v.code = $2
    `, tenantID, code)

	var (
		id, tplID, tenID, state string
		expiresAt               *time.Time
		bytesUsed               int64
		secondsUsed             int
		duration                *int
		dataCap                 *int64
		down, up                *int
		maxDevices              int
	)
	if err := row.Scan(&id, &tplID, &tenID, &state, &expiresAt,
		&bytesUsed, &secondsUsed, &duration, &dataCap, &down, &up, &maxDevices); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query voucher: %w", err)
	}

	switch state {
	case "revoked":
		return nil, ErrRevoked
	case "expired", "exhausted":
		return nil, ErrExpired
	}
	if expiresAt != nil && time.Now().After(*expiresAt) {
		return nil, ErrExpired
	}
	remaining := 0
	if duration != nil {
		remaining = *duration - secondsUsed
		if remaining <= 0 {
			return nil, ErrExhausted
		}
	}
	r := &Redeemed{
		VoucherID:       id,
		TemplateID:      tplID,
		TenantID:        tenID,
		DurationSeconds: remaining,
		MaxDevices:      maxDevices,
	}
	if dataCap != nil {
		r.DataCapBytes = *dataCap - bytesUsed
		if dataCap != nil && r.DataCapBytes <= 0 {
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

// Activate flips voucher.state to 'active' and stamps activated_at if not set.
func (s *Store) Activate(ctx context.Context, voucherID string) error {
	_, err := s.DB.Exec(ctx, `
        UPDATE vouchers
           SET state = 'active',
               activated_at = COALESCE(activated_at, now())
         WHERE id = $1 AND state IN ('unused','active')
    `, voucherID)
	return err
}

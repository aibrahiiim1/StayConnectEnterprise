// Package pmsguard enforces brute-force protection on PMS verify attempts.
//
// Two independent rules:
//
//   1. Per-room lockout — too many failed attempts on a single room number
//      within the lockout window blocks further tries until the window slides.
//   2. Per-IP rate limit — too many attempts (success or failure) from a
//      single source IP suggests scanning; throttle.
//
// All limits read from a single config struct so tenants can override the
// defaults via tenants.auth_methods.pms.
package pmsguard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	MaxFailuresPerRoom    int           // default 5
	LockoutWindow         time.Duration // default 15m
	MaxAttemptsPerIP      int           // default 30 in IPWindow
	IPWindow              time.Duration // default 15m
}

func (c Config) withDefaults() Config {
	if c.MaxFailuresPerRoom <= 0 {
		c.MaxFailuresPerRoom = 5
	}
	if c.LockoutWindow <= 0 {
		c.LockoutWindow = 15 * time.Minute
	}
	if c.MaxAttemptsPerIP <= 0 {
		c.MaxAttemptsPerIP = 30
	}
	if c.IPWindow <= 0 {
		c.IPWindow = 15 * time.Minute
	}
	return c
}

var (
	ErrRoomLocked    = errors.New("pmsguard: too many failed attempts on this room — try again later")
	ErrIPRateLimited = errors.New("pmsguard: too many requests from your network — try again later")
)

// CheckRoom returns ErrRoomLocked if the room has exceeded the failure budget.
func CheckRoom(ctx context.Context, db *pgxpool.Pool, cfg Config, tenantID, roomNumber string) error {
	cfg = cfg.withDefaults()
	var failures int
	if err := db.QueryRow(ctx, `
        SELECT count(*) FROM pms_attempts
         WHERE tenant_id = $1
           AND lower(room_number) = lower($2)
           AND success = false
           AND attempted_at > now() - $3::interval
    `, tenantID, roomNumber, fmt.Sprintf("%d seconds", int(cfg.LockoutWindow.Seconds()))).Scan(&failures); err != nil {
		return fmt.Errorf("pmsguard: room lookup: %w", err)
	}
	if failures >= cfg.MaxFailuresPerRoom {
		return ErrRoomLocked
	}
	return nil
}

// CheckIP returns ErrIPRateLimited when an IP has exceeded the attempt budget.
func CheckIP(ctx context.Context, db *pgxpool.Pool, cfg Config, tenantID, ip string) error {
	cfg = cfg.withDefaults()
	if ip == "" {
		return nil
	}
	var attempts int
	if err := db.QueryRow(ctx, `
        SELECT count(*) FROM pms_attempts
         WHERE tenant_id = $1
           AND ip = $2::inet
           AND attempted_at > now() - $3::interval
    `, tenantID, ip, fmt.Sprintf("%d seconds", int(cfg.IPWindow.Seconds()))).Scan(&attempts); err != nil {
		return fmt.Errorf("pmsguard: ip lookup: %w", err)
	}
	if attempts >= cfg.MaxAttemptsPerIP {
		return ErrIPRateLimited
	}
	return nil
}

// Record persists an attempt. Caller passes success=true once validation
// has actually returned a valid Result.
func Record(ctx context.Context, db *pgxpool.Pool, tenantID, applianceID, roomNumber, secondaryKind, ip, errCode string, success bool) {
	var errArg any
	if errCode != "" {
		errArg = errCode
	}
	_, err := db.Exec(ctx, `
        INSERT INTO pms_attempts
          (tenant_id, appliance_id, room_number, secondary_kind, ip, success, error_code)
        VALUES
          ($1, NULLIF($2,'')::uuid, $3, $4,
           CASE WHEN $5 = '' THEN NULL ELSE $5::inet END,
           $6, $7)
    `, tenantID, applianceID, roomNumber, secondaryKind, ip, success, errArg)
	if err != nil {
		// Logged at the call site; never block auth on telemetry write failure.
		_ = err
	}
}

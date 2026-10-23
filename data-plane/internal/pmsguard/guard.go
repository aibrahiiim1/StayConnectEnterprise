// Package pmsguard throttles PMS verify attempts: a per-room failure lockout and a per-IP attempt limit.
package pmsguard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	MaxFailuresPerRoom int           // default 5
	LockoutWindow      time.Duration // default 15m
	MaxAttemptsPerIP   int           // default 30 in IPWindow
	IPWindow           time.Duration // default 15m
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
		// Never block auth on an attempt-log write failure.
		_ = err
	}
}

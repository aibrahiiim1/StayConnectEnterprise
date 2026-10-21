package signinattempt

// THE NETWORK PAUSE (migration 0136; GDPR audit 2026-10-19, A4).
//
// The per-device rule is defeated by changing the MAC address, so wrong room sign-ins are also counted per
// guest NETWORK, whichever devices they came from. When a network reaches its threshold, room sign-in there is
// paused for a while; staff can end the pause, or confirm a genuine guest's device so it may try again. The
// thresholds and every decision live in the database functions; this file calls them.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
)

// NetworkPolicy is a site's network-pause settings.
type NetworkPolicy struct {
	MaxFailures   int // wrong room sign-ins on one network
	WindowSeconds int // within this rolling window
	PauseSeconds  int // pause room sign-in there for this long
}

// The accepted bounds, matching the CHECK of migration 0136. The lower threshold keeps a busy network's
// ordinary typing mistakes from pausing it; the upper bounds keep a mistyped setting from pausing for a day.
const (
	MinNetworkFailures      = 10
	MaxNetworkFailures      = 1000
	MinNetworkWindowSeconds = 60
	MaxNetworkWindowSeconds = 3600
	MinNetworkPauseSeconds  = 60
	MaxNetworkPauseSeconds  = 3600
)

// NetworkDefaults are the approved defaults: 30 wrong room sign-ins in 10 minutes pause it for 15 minutes.
func NetworkDefaults() NetworkPolicy {
	return NetworkPolicy{MaxFailures: 30, WindowSeconds: 600, PauseSeconds: 900}
}

// Validate reports why the settings are unacceptable, or nil.
func (p NetworkPolicy) Validate() error {
	if p.MaxFailures < MinNetworkFailures || p.MaxFailures > MaxNetworkFailures {
		return fmt.Errorf("wrong room sign-ins before a pause must be between %d and %d (got %d)",
			MinNetworkFailures, MaxNetworkFailures, p.MaxFailures)
	}
	if p.WindowSeconds < MinNetworkWindowSeconds || p.WindowSeconds > MaxNetworkWindowSeconds {
		return fmt.Errorf("the counting window must be between %d and %d seconds (got %d)",
			MinNetworkWindowSeconds, MaxNetworkWindowSeconds, p.WindowSeconds)
	}
	if p.PauseSeconds < MinNetworkPauseSeconds || p.PauseSeconds > MaxNetworkPauseSeconds {
		return fmt.Errorf("the pause must be between %d and %d seconds (got %d)",
			MinNetworkPauseSeconds, MaxNetworkPauseSeconds, p.PauseSeconds)
	}
	return nil
}

// CheckNetwork asks whether room sign-in is paused on this network for this device. A confirmed device is not
// paused. As with Check, an error is not an allow: the caller refuses with a service failure.
func (s *ProtectionStore) CheckNetwork(ctx context.Context, tenantID, siteID, networkID string, mac net.HardwareAddr) (Gate, error) {
	var g Gate
	if s == nil || s.pool == nil || networkID == "" || len(mac) == 0 {
		return g, nil
	}
	var remaining *int
	err := s.pool.QueryRow(ctx,
		`SELECT paused, remaining_seconds FROM iam_v2.guest_signin_network_gate($1::uuid,$2::uuid,$3::uuid,$4::macaddr)`,
		tenantID, siteID, networkID, mac.String()).Scan(&g.Restricted, &remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return Gate{}, nil
	}
	if err != nil {
		return Gate{}, err
	}
	if g.Restricted && remaining != nil {
		g.RemainingSeconds = *remaining
	}
	return g, nil
}

// NoteNetworkFailure reports one wrong room sign-in on a network, after its attempt row is written, and says
// whether it started a pause.
func (s *ProtectionStore) NoteNetworkFailure(ctx context.Context, tenantID, siteID, networkID, networkName string) (pausedNow bool, expiresAt *time.Time, count int, err error) {
	if s == nil || s.pool == nil || networkID == "" {
		return false, nil, 0, nil
	}
	err = s.pool.QueryRow(ctx, `
		SELECT paused_now, expires_at, failure_count
		  FROM iam_v2.guest_signin_note_network_failure($1::uuid,$2::uuid,$3::uuid,$4)`,
		tenantID, siteID, networkID, networkName).Scan(&pausedNow, &expiresAt, &count)
	return pausedNow, expiresAt, count, err
}

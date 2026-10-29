package signinattempt

// The per-device rule is defeated by changing MAC, so wrong room sign-ins are also counted per guest
// network. At the threshold, room sign-in there pauses; staff can end the pause or confirm a genuine
// guest's device. Thresholds and decisions live in the database functions.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
)

type NetworkPolicy struct {
	MaxFailures   int // wrong room sign-ins on one network
	WindowSeconds int // within this rolling window
	PauseSeconds  int // pause room sign-in there for this long
}

// Bounds match the database CHECK. The lower threshold keeps ordinary typing mistakes on a busy network from
// pausing it; the upper bounds keep a mistyped setting from pausing for a day.
const (
	MinNetworkFailures      = 10
	MaxNetworkFailures      = 1000
	MinNetworkWindowSeconds = 60
	MaxNetworkWindowSeconds = 3600
	MinNetworkPauseSeconds  = 60
	MaxNetworkPauseSeconds  = 3600
)

func NetworkDefaults() NetworkPolicy {
	// 60 since migration 0170 (AD-31): a busy network of ordinary typing mistakes should not pause room sign-in for everyone.
	return NetworkPolicy{MaxFailures: 60, WindowSeconds: 600, PauseSeconds: 900}
}

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
	if s.disabled() || networkID == "" || len(mac) == 0 {
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

func (s *ProtectionStore) NoteNetworkFailure(ctx context.Context, tenantID, siteID, networkID, networkName string) (pausedNow bool, expiresAt *time.Time, count int, err error) {
	if s.disabled() || networkID == "" {
		return false, nil, 0, nil
	}
	err = s.pool.QueryRow(ctx, `
		SELECT paused_now, expires_at, failure_count
		  FROM iam_v2.guest_signin_note_network_failure($1::uuid,$2::uuid,$3::uuid,$4)`,
		tenantID, siteID, networkID, networkName).Scan(&pausedNow, &expiresAt, &count)
	return pausedNow, expiresAt, count, err
}

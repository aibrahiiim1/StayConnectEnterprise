// Package tenantcfg reads the tenant's auth_methods bundle on-demand. It's a
// thin DB read with no caching for now; Phase 5 should add a NATS-pushed
// snapshot to avoid the per-request query.
package tenantcfg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type AuthMethod struct {
	Enabled    bool   `json:"enabled"`
	TemplateID string `json:"template_id,omitempty"`
}

type AuthMethods struct {
	Voucher *AuthMethod `json:"voucher,omitempty"`
	Email   *AuthMethod `json:"email,omitempty"`
	SMS     *AuthMethod `json:"sms,omitempty"`
	// Social is keyed by provider name (e.g. "google", "apple"). Each entry
	// has its own enabled flag + template_id so providers can be turned on
	// independently and route to different ticket templates if desired.
	Social map[string]*AuthMethod `json:"social,omitempty"`
	PMS    *PMSConfig             `json:"pms,omitempty"`
}

// PMSConfig configures the room-number-based guest auth flow. See migration
// 0011 for the documented shape.
type PMSConfig struct {
	Enabled              bool   `json:"enabled"`
	TemplateID           string `json:"template_id,omitempty"`
	Provider             string `json:"provider,omitempty"`             // "stub" | "protel-fias" | ...
	Mode                 string `json:"mode,omitempty"`                 // "room_lastname" | "room_firstname" | "room_reservation" | "either"
	MaxFailuresPerRoom   int    `json:"max_failures_per_room,omitempty"` // 0 = use guard default
	LockoutWindowMinutes int    `json:"lockout_window_minutes,omitempty"`
}

func Load(ctx context.Context, db *pgxpool.Pool, tenantID string) (*AuthMethods, error) {
	var raw []byte
	if err := db.QueryRow(ctx,
		`SELECT auth_methods::text FROM tenants WHERE id = $1`, tenantID,
	).Scan(&raw); err != nil {
		return nil, fmt.Errorf("tenantcfg: load: %w", err)
	}
	var out AuthMethods
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("tenantcfg: parse: %w", err)
	}
	return &out, nil
}

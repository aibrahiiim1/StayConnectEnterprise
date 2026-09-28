// Package tenantcfg reads the tenant's auth_methods bundle on-demand. It's a
// thin DB read with no caching: every request reads the current site setting.
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
	// WhatsApp is a one-time code delivered as a WhatsApp authentication-template message. It is a separate
	// channel from SMS (its own licence module, whatsapp_otp, and its own provider), never an SMS variant.
	WhatsApp *AuthMethod `json:"whatsapp,omitempty"`
	// Social is keyed by provider name (e.g. "google", "apple"). Each entry
	// has its own enabled flag + template_id so providers can be turned on
	// independently and route to different ticket templates if desired.
	Social map[string]*AuthMethod `json:"social,omitempty"`
	PMS    *PMSConfig             `json:"pms,omitempty"`
	// GuestAccount is the username/password method. Basic-access (never license-
	// gated); shown on the portal only when enabled.
	GuestAccount *AuthMethod `json:"guest_account,omitempty"`
	// Open is "Clients may choose a package without signing in" (core, default off). The entitlement subject
	// is an opaque anonymous access subject, never the device.
	Open *AuthMethod `json:"open,omitempty"`
}

// PMSConfig configures the room-number-based guest auth flow. See migration
// 0011 for the documented shape.
type PMSConfig struct {
	Enabled              bool   `json:"enabled"`
	TemplateID           string `json:"template_id,omitempty"`
	Provider             string `json:"provider,omitempty"`              // "stub" | "protel-fias" | ...
	Mode                 string `json:"mode,omitempty"`                  // "room_lastname" | "room_firstname" | "room_reservation" | "either"
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

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
	// Portal holds the site's journey settings (contract §6.2): which method leads the landing page and for
	// how long a verified device is remembered. Operational values, persisted and audited with the switches.
	Portal *PortalConfig `json:"portal,omitempty"`
}

// PortalConfig is the site's Client Portal journey configuration.
type PortalConfig struct {
	// PrimaryMethod leads the landing page: pms | email | sms | whatsapp | guest_account | voucher | "" (auto).
	PrimaryMethod string `json:"primary_method,omitempty"`
	// RememberDeviceDays is how long a device that verified a code or an identity provider may reconnect
	// without proving again (0 = never remember). nil = the default of 30 days.
	RememberDeviceDays *int `json:"remember_device_days,omitempty"`
}

// DefaultRememberDeviceDays is the contract default (§2.3).
const DefaultRememberDeviceDays = 30

// RememberDeviceDays answers the effective setting, bounded to 0..365.
func (a *AuthMethods) RememberDeviceDays() int {
	if a == nil || a.Portal == nil || a.Portal.RememberDeviceDays == nil {
		return DefaultRememberDeviceDays
	}
	d := *a.Portal.RememberDeviceDays
	if d < 0 {
		return 0
	}
	if d > 365 {
		return 365
	}
	return d
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

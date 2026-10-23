// Package tenantcfg reads the tenant's auth_methods bundle uncached, so every request sees the current setting.
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
	// EmailAccount is self-registered email and password sign-in, licensed by the email_otp module; it replaced
	// the former "email" one-time-code switch.
	EmailAccount *AuthMethod `json:"email_account,omitempty"`
	SMS          *AuthMethod `json:"sms,omitempty"`
	// WhatsApp is a separate channel from SMS (own licence module whatsapp_otp, own provider).
	WhatsApp *AuthMethod `json:"whatsapp,omitempty"`
	// Social is keyed by provider name; each entry is enabled and routed independently.
	Social map[string]*AuthMethod `json:"social,omitempty"`
	PMS    *PMSConfig             `json:"pms,omitempty"`
	// GuestAccount is username/password for staff-created Personal accounts; never license-gated.
	GuestAccount *AuthMethod `json:"guest_account,omitempty"`
	// Open lets clients choose a package without signing in; the subject is an opaque anonymous one, never the device.
	Open *AuthMethod `json:"open,omitempty"`
	// Portal holds the landing-page journey settings.
	Portal *PortalConfig `json:"portal,omitempty"`
}

// PortalConfig is the site's Client Portal journey configuration.
type PortalConfig struct {
	// PrimaryMethod leads the landing page: pms | email_account | sms | whatsapp | guest_account | voucher | "" (auto).
	PrimaryMethod string `json:"primary_method,omitempty"`
	// RememberDeviceDays: 0 = never remember, nil = DefaultRememberDeviceDays.
	RememberDeviceDays *int `json:"remember_device_days,omitempty"`
}

const DefaultRememberDeviceDays = 30

// RememberDeviceDays returns the effective setting, bounded to 0..365.
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

// PMSConfig configures the room-number-based guest auth flow.
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
	return Parse(raw)
}

// Parse decodes a stored auth_methods document. A leftover "email" switch reads as "email_account" (and a
// primary method "email" is renamed likewise) because the stored rename is a manual owner step
// (deploy/scripts/convert-email-method-to-email-account.sql); only its enabled state is carried over.
func Parse(raw []byte) (*AuthMethods, error) {
	var out AuthMethods
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("tenantcfg: parse: %w", err)
	}
	if out.EmailAccount == nil {
		var legacy struct {
			Email *struct {
				Enabled bool `json:"enabled"`
			} `json:"email"`
		}
		if json.Unmarshal(raw, &legacy) == nil && legacy.Email != nil {
			out.EmailAccount = &AuthMethod{Enabled: legacy.Email.Enabled}
		}
	}
	if out.Portal != nil && out.Portal.PrimaryMethod == "email" {
		out.Portal.PrimaryMethod = "email_account"
	}
	return &out, nil
}

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
	// TemporaryAccess is the email-code method's verification window (read on the email method only).
	TemporaryAccess *TemporaryAccess `json:"temporary_access,omitempty"`
}

// TemporaryAccess: when an email code is issued, the requesting device may use the internet for a short,
// metered window so the code can be read in any mail app (contract §6.6). Every field is an operational
// setting: absent means the default, and Effective clamps a stored value into its bounds.
type TemporaryAccess struct {
	Enabled             *bool `json:"enabled,omitempty"`
	Minutes             *int  `json:"minutes,omitempty"`
	DownKbps            *int  `json:"down_kbps,omitempty"`
	UpKbps              *int  `json:"up_kbps,omitempty"`
	DataMB              *int  `json:"data_mb,omitempty"`
	PerDeviceDaily      *int  `json:"per_device_daily,omitempty"`
	PerDestinationDaily *int  `json:"per_destination_daily,omitempty"`
	MaxConcurrent       *int  `json:"max_concurrent,omitempty"`
	// RetryAfterMinutes is how long a device waits after a window ends before it may be given another (the
	// client may still not have the code). A window ended by choosing a different address does not start it.
	RetryAfterMinutes *int `json:"retry_after_minutes,omitempty"`
}

// TemporaryAccessBound is one setting's default and inclusive bounds.
type TemporaryAccessBound struct{ Default, Min, Max int }

// TemporaryAccessBounds are the defaults the Product Owner set (2026-10-11) and the safe limits a site
// administrator may choose within. edged validates against the same table.
var TemporaryAccessBounds = map[string]TemporaryAccessBound{
	"minutes":               {10, 1, 30},
	"down_kbps":             {2000, 128, 20000},
	"up_kbps":               {1000, 64, 10000},
	"data_mb":               {25, 1, 200},
	"per_device_daily":      {3, 1, 10},
	"per_destination_daily": {3, 1, 10},
	"max_concurrent":        {50, 1, 500},
	// Product Owner, 2026-10-12: a client who still has no code may have the internet again after 30 minutes.
	// 0 = immediately (the daily limits still apply).
	"retry_after_minutes": {30, 0, 1440},
}

// TemporaryAccessEffective is the setting as enforced.
type TemporaryAccessEffective struct {
	Enabled                                            bool
	Minutes, DownKbps, UpKbps, DataMB                  int
	PerDeviceDaily, PerDestinationDaily, MaxConcurrent int
	RetryAfterMinutes                                  int
}

func clampSetting(name string, v *int) int {
	b := TemporaryAccessBounds[name]
	if v == nil {
		return b.Default
	}
	if *v < b.Min {
		return b.Min
	}
	if *v > b.Max {
		return b.Max
	}
	return *v
}

// Effective answers the window the site has, defaults filled in (enabled by default).
func (t *TemporaryAccess) Effective() TemporaryAccessEffective {
	if t == nil {
		t = &TemporaryAccess{}
	}
	on := true
	if t.Enabled != nil {
		on = *t.Enabled
	}
	return TemporaryAccessEffective{
		Enabled: on, Minutes: clampSetting("minutes", t.Minutes), DownKbps: clampSetting("down_kbps", t.DownKbps),
		UpKbps: clampSetting("up_kbps", t.UpKbps), DataMB: clampSetting("data_mb", t.DataMB),
		PerDeviceDaily: clampSetting("per_device_daily", t.PerDeviceDaily), PerDestinationDaily: clampSetting("per_destination_daily", t.PerDestinationDaily),
		MaxConcurrent:     clampSetting("max_concurrent", t.MaxConcurrent),
		RetryAfterMinutes: clampSetting("retry_after_minutes", t.RetryAfterMinutes),
	}
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
	// MailProviders are the email services a device may reach BEFORE sign-in so it can read its one-time code
	// without leaving the Wi-Fi: preset ids from internal/mailreach (gmail, outlook, yahoo, icloud, ...).
	// MailDomains are host names the site adds for a provider the presets do not know. Both open the walled
	// garden only while the Email code method is switched on.
	MailProviders []string `json:"mail_providers,omitempty"`
	MailDomains   []string `json:"mail_domains,omitempty"`
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

// Package license verifies vendor-signed entitlements locally so guest access does not depend on cloud reachability.
package license

import (
	"encoding/json"
	"fmt"
	"time"
)

// A zero-value feature is not entitled.
type Features struct {
	PMS         bool `json:"pms"`
	PaidWiFi    bool `json:"paid_wifi"`
	SMSOTP      bool `json:"sms_otp"`
	EmailOTP    bool `json:"email_otp"`
	SocialLogin bool `json:"social_login"`
	HA          bool `json:"ha"`
	WhiteLabel  bool `json:"white_label"`
}

// Zero and -1 both mean unlimited for legacy plan limits.
type Limits struct {
	MaxAppliancesForSite       int `json:"max_appliances_for_site"`
	MaxConcurrentGuestSessions int `json:"max_concurrent_guest_sessions"`
	MaxLocalOperators          int `json:"max_local_operators"`
	MaxGuestAccessPlans        int `json:"max_guest_access_plans"`
	AccountingRetentionDays    int `json:"accounting_retention_days"`
	AuditRetentionDays         int `json:"audit_retention_days"`
}

type DocStatus string

const (
	DocActive    DocStatus = "active"
	DocSuspended DocStatus = "suspended"
)

// The signature covers the envelope's exact payload bytes, without JSON canonicalization.
type Document struct {
	LicenseID          string    `json:"license_id"`
	TenantID           string    `json:"tenant_id"`
	SiteID             string    `json:"site_id"`
	ApplianceIDs       []string  `json:"appliance_ids"`
	CommercialPlanCode string    `json:"commercial_plan_code"`
	Status             DocStatus `json:"status"`
	IssuedAt           time.Time `json:"issued_at"`
	ValidUntil         time.Time `json:"valid_until"`
	OfflineGraceDays   int       `json:"offline_grace_days"`
	Features           Features  `json:"features"`
	Limits             Limits    `json:"limits"`

	// Optional binding fields preserve compatibility with unbound legacy licenses.
	ApplianceID            string    `json:"appliance_id,omitempty"`
	ApplianceSerial        string    `json:"appliance_serial,omitempty"`
	HardwareFingerprint    string    `json:"hardware_fingerprint,omitempty"`
	IdentityKeyFingerprint string    `json:"identity_key_fingerprint,omitempty"`
	WANMAC                 string    `json:"wan_mac,omitempty"`
	ValidFrom              time.Time `json:"valid_from,omitempty"`
	SignerKeyID            string    `json:"signer_key_id,omitempty"`

	// A monotonic version rejects replay of older licenses.
	MaxConcurrentOnlineGuests int    `json:"max_concurrent_online_guests,omitempty"` // 0 means unlimited.
	GracePeriodDays           int    `json:"grace_period_days,omitempty"`
	LicenseVersion            int64  `json:"license_version,omitempty"`
	SupersedesLicenseID       string `json:"supersedes_license_id,omitempty"`

	// Modules alone authorize commercial capabilities; Features is a compatibility projection.
	Modules Modules `json:"modules"`

	// Unknown versions fail closed to avoid misreading fields.
	SchemaVersion int `json:"schema_version"`
}

const CurrentSchemaVersion = 4
const MinSchemaVersion = 1

// For v3+, zero explicitly means no renewal grace; older documents use offline grace when no renewal grace is set.
func (d *Document) EffectiveGraceDays() int {
	if d.SchemaVersion >= 3 {
		if d.GracePeriodDays < 0 {
			return 0
		}
		return d.GracePeriodDays
	}
	if d.GracePeriodDays > 0 {
		return d.GracePeriodDays
	}
	return d.OfflineGraceDays
}

// The cap spans all guest networks; older documents use the plan-derived limit.
func (d *Document) EffectiveMaxConcurrentOnlineGuests() int {
	if d.MaxConcurrentOnlineGuests > 0 {
		return d.MaxConcurrentOnlineGuests
	}
	return d.Limits.MaxConcurrentGuestSessions
}

// Verification uses the embedded payload bytes without re-serializing them.
type Envelope struct {
	PayloadB64 string `json:"payload"`
	SigB64     string `json:"signature"`
	KeyID      string `json:"key_id"`
}

type State string

const (
	StateActive State = "Active"
	// Renewal grace preserves guest access; cloud staleness only raises a warning.
	StateGracePeriod State = "GracePeriod"
	// Legacy restricted state permits basic access but blocks gated features and provisioning.
	StateRestricted State = "Restricted"
	// Expiry blocks new sessions while existing sessions end naturally and admin can upload a license.
	StateExpired   State = "Expired"
	StateSuspended State = "Suspended"
	// Revocation requires an authenticated notice and restricts admin to the license page.
	StateRevoked State = "Revoked"
	// Production fails closed without a signed license; the UI can distinguish pending activation from expiry.
	StateUnlicensed State = "unlicensed"
)

// License state only gates new authorization; existing sessions end through their normal limits.
func (s State) AllowsNewSessions() bool {
	switch s {
	case StateActive, StateGracePeriod, StateRestricted:
		return true
	default:
		return false
	}
}

func (s State) AllowsProvisioning() bool {
	return s == StateActive || s == StateGracePeriod
}

func FeatureEnabled(st State, entitled bool) bool {
	if !entitled {
		return false
	}
	switch st {
	case StateActive, StateGracePeriod:
		return true
	default:
		return false
	}
}

func (d *Document) Validate() error {
	if d.SchemaVersion < MinSchemaVersion || d.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", d.SchemaVersion)
	}
	if d.LicenseID == "" || d.TenantID == "" || d.SiteID == "" {
		return fmt.Errorf("license_id, tenant_id and site_id are required")
	}
	if !d.ValidUntil.After(d.IssuedAt) {
		return fmt.Errorf("valid_until must be after issued_at")
	}
	if d.OfflineGraceDays < 0 || d.OfflineGraceDays > 365 {
		return fmt.Errorf("offline_grace_days out of range")
	}
	if d.Status != DocActive && d.Status != DocSuspended {
		return fmt.Errorf("unknown status %q", d.Status)
	}
	if d.SchemaVersion >= 4 {
		if d.Modules == nil {
			return fmt.Errorf("schema v4 requires modules")
		}
		// Reject independent feature claims that could bypass module grants.
		if d.Features != ProjectFeatures(d.Modules) {
			return fmt.Errorf("features disagree with the modules projection")
		}
	}
	return nil
}

func (d *Document) Marshal() ([]byte, error) { return json.Marshal(d) }

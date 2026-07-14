// Package license implements StayConnect's vendor-signed entitlement
// documents.
//
// The cloud control plane signs an entitlement payload with the vendor's
// Ed25519 private key. Appliances hold only the public verification key and
// validate entitlements entirely offline — no cloud round-trip is needed for
// any guest or admin operation. The document is delivered on enrollment and
// on every renewal/change via sync; the appliance persists the latest copy
// and evaluates its operational state locally.
package license

import (
	"encoding/json"
	"fmt"
	"time"
)

// Features are the boolean commercial entitlements a CommercialPlan grants.
// Zero value = not entitled.
type Features struct {
	PMS         bool `json:"pms"`
	PaidWiFi    bool `json:"paid_wifi"`
	SMSOTP      bool `json:"sms_otp"`
	EmailOTP    bool `json:"email_otp"`
	SocialLogin bool `json:"social_login"`
	HA          bool `json:"ha"`
	WhiteLabel  bool `json:"white_label"`
}

// Limits are the numeric commercial entitlements. 0 means "not set" and is
// treated as unlimited by convention with the existing plan-limit semantics
// (-1 also means unlimited).
type Limits struct {
	MaxAppliancesForSite       int `json:"max_appliances_for_site"`
	MaxConcurrentGuestSessions int `json:"max_concurrent_guest_sessions"`
	MaxLocalOperators          int `json:"max_local_operators"`
	MaxGuestAccessPlans        int `json:"max_guest_access_plans"`
	AccountingRetentionDays    int `json:"accounting_retention_days"`
	AuditRetentionDays         int `json:"audit_retention_days"`
}

// DocStatus is the issuer-declared status baked into the signed payload.
type DocStatus string

const (
	DocActive    DocStatus = "active"
	DocSuspended DocStatus = "suspended" // billing hold — cloud-directed
)

// Document is the entitlement payload that gets signed. Field order and
// content are stable; the signature covers the exact serialized bytes
// embedded in the Envelope, so no JSON canonicalization is required.
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

	// Schema v2 — per-appliance hardware/identity binding. The cryptographic
	// identity key is the primary trust anchor; serial / hardware fingerprint /
	// WAN MAC are hardware-mismatch + clone-detection signals. All optional in
	// the type for v1 backward-compatibility, but populated on every new issue
	// and enforced locally by the appliance (see licstate binding check).
	ApplianceID            string    `json:"appliance_id,omitempty"`
	ApplianceSerial        string    `json:"appliance_serial,omitempty"`
	HardwareFingerprint    string    `json:"hardware_fingerprint,omitempty"`
	IdentityKeyFingerprint string    `json:"identity_key_fingerprint,omitempty"`
	WANMAC                 string    `json:"wan_mac,omitempty"`
	ValidFrom              time.Time `json:"valid_from,omitempty"`
	SignerKeyID            string    `json:"signer_key_id,omitempty"`

	// Schema v3 — the SIMPLE license model. The license itself is the direct
	// source of the commercial entitlement: one appliance, one concurrent
	// online-guest cap, one validity window with an explicit grace period.
	// LicenseVersion is a per-appliance monotonic sequence: the appliance
	// persists the highest version it has accepted and refuses anything lower
	// (anti-rollback / old-license replay protection).
	MaxConcurrentOnlineGuests int    `json:"max_concurrent_online_guests,omitempty"` // 0 = unlimited
	GracePeriodDays           int    `json:"grace_period_days,omitempty"`
	LicenseVersion            int64  `json:"license_version,omitempty"`
	SupersedesLicenseID       string `json:"supersedes_license_id,omitempty"`

	// SchemaVersion allows future payload evolution; verifiers reject
	// versions they do not understand rather than misreading fields.
	SchemaVersion int `json:"schema_version"`
}

// CurrentSchemaVersion is the version new licenses are issued with. Verifiers
// accept MinSchemaVersion..CurrentSchemaVersion so an appliance holding a v1
// license keeps working across the upgrade.
const CurrentSchemaVersion = 3
const MinSchemaVersion = 1

// EffectiveGraceDays is the grace window applied after ValidUntil. The simple
// model (v3) carries it explicitly; older documents fall back to the offline
// grace, which historically doubled as the renewal grace.
func (d *Document) EffectiveGraceDays() int {
	if d.GracePeriodDays > 0 {
		return d.GracePeriodDays
	}
	return d.OfflineGraceDays
}

// EffectiveMaxConcurrentOnlineGuests is the appliance-wide cap on concurrently
// authorized guest sessions across all guest VLANs/networks. 0 = unlimited.
// v3 documents carry it explicitly; older documents fall back to the
// plan-derived limit.
func (d *Document) EffectiveMaxConcurrentOnlineGuests() int {
	if d.MaxConcurrentOnlineGuests > 0 {
		return d.MaxConcurrentOnlineGuests
	}
	return d.Limits.MaxConcurrentGuestSessions
}

// Envelope is the on-disk / on-wire form: the exact signed payload bytes
// (base64 in JSON) plus the Ed25519 signature and the signing key id.
// The payload is NOT re-serialized on verify — the embedded bytes are what
// the signature covers and what gets decoded into a Document.
type Envelope struct {
	PayloadB64 string `json:"payload"`
	SigB64     string `json:"signature"`
	KeyID      string `json:"key_id"`
}

// State is the locally evaluated operational state of an appliance license.
type State string

const (
	// StateActive — document within validity. All entitled features work.
	StateActive State = "Active"
	// StateGracePeriod — validity expired but within offline_grace_days.
	// All guest functionality continues unchanged; Hotel Admin surfaces a
	// prominent renewal warning. Exists so a renewal issued while the
	// appliance was offline never interrupts a hotel.
	StateGracePeriod State = "GracePeriod"
	// StateRestricted — grace exhausted (valid_until + grace .. + 2×grace).
	// Existing guest sessions continue and voucher/PMS guest logins still
	// work, but entitlement-gated features (paid WiFi, SMS OTP, social) are
	// disabled and creating new guest access plans / voucher batches is
	// blocked. Admin is directed to the license page.
	StateRestricted State = "Restricted"
	// StateExpired — beyond valid_until + 2×offline_grace. New guest
	// sessions are refused (portal shows a service notice); existing
	// sessions are allowed to run to their natural end; local admin remains
	// accessible read-only plus license upload.
	StateExpired State = "Expired"
	// StateSuspended — issuer set status=suspended (billing hold). Same
	// enforcement as Restricted, effective immediately on receipt.
	StateSuspended State = "Suspended"
	// StateRevoked — an authenticated revocation notice names this
	// license_id. New sessions refused immediately; admin locked to the
	// license page. Strongest state; never entered by time alone.
	StateRevoked State = "Revoked"
)

// AllowsNewSessions reports whether new guest sessions may be created.
//
// Final simple-model semantics: Active and Grace authorize new sessions;
// Expired, Suspended and Revoked do not. Existing sessions are never dropped
// by a state change (the reaper/quotas end them naturally), and DHCP/DNS/
// portal/Hotel Admin all keep running — only NEW Internet authorization is
// refused. Restricted is a legacy (pre-v3) intermediate window and keeps its
// historical allow-basic-access behavior for old documents.
func (s State) AllowsNewSessions() bool {
	switch s {
	case StateActive, StateGracePeriod, StateRestricted:
		return true
	default:
		return false
	}
}

// AllowsProvisioning reports whether new guest access plans, voucher batches
// and similar management writes are allowed.
func (s State) AllowsProvisioning() bool {
	return s == StateActive || s == StateGracePeriod
}

// FeatureEnabled evaluates a feature entitlement under the current state:
// entitled features degrade in Restricted/Suspended, everything is off in
// Expired/Revoked.
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
	return nil
}

// Marshal serializes a Document for signing.
func (d *Document) Marshal() ([]byte, error) { return json.Marshal(d) }

// Package grace is the After Check-out Access decision core (formerly "Checkout Grace"; Emergency Grace is its built-in
// fallback). A checked-out Stay with an active valid Entitlement at the effective-checkout boundary gets exactly one
// After Check-out conversion per lifecycle episode, whatever the prior package's origin or price; no active Entitlement
// means none. Emergency Grace is the built-in fallback when the configured policy is invalid at conversion, not a
// cloud-outage reauthorization. The feature can be switched off per site; then nothing is granted (no fallback either).
// See docs/architecture/ONEGATE_AFTER_CHECKOUT_ACCESS.md.
package grace

// Policy is pinned at conversion time so later Admin edits never rewrite an existing Guest's Grace terms.
type Policy struct {
	DurationSeconds   int
	DownKbps          int
	UpKbps            int
	DataQuotaBytes    int64
	DeviceLimit       int
	DeviceLimitPolicy string // only "REJECT_NEW_DEVICE" is currently approved
	IsEmergency       bool
}

const DeviceLimitRejectNew = "REJECT_NEW_DEVICE"

// BuiltinEmergencyPolicy: 60 min, 5 Mbps down, 2 Mbps up, 500 MB, REJECT_NEW_DEVICE.
func BuiltinEmergencyPolicy() Policy {
	return Policy{
		DurationSeconds: 3600, DownKbps: 5000, UpKbps: 2000, DataQuotaBytes: 500 << 20,
		DeviceLimit: 0, DeviceLimitPolicy: DeviceLimitRejectNew, IsEmergency: true,
	}
}

// ValidatePolicy is the server-side boundary; an invalid policy makes the caller use the Emergency fallback.
// hasPinnedRevision asserts a valid system Checkout Grace package revision is selected.
func ValidatePolicy(p Policy, enabled, hasPinnedRevision bool) bool {
	if !enabled || !hasPinnedRevision {
		return false
	}
	if p.DurationSeconds <= 0 || p.DurationSeconds > 604800 {
		return false
	}
	if p.DownKbps <= 0 || p.UpKbps <= 0 || p.DataQuotaBytes < 0 {
		return false
	}
	if p.DeviceLimitPolicy != DeviceLimitRejectNew {
		return false
	}
	return true
}

// Operator-facing bounds of the one After Check-out Access policy (Hotel Admin / edged). The database keeps its wider
// structural CHECKs (up to seven days), so a policy published before these bounds existed stays valid for the checkouts it
// already governs; only a NEW publication is held to them.
const (
	MinDurationHours     = 1
	MaxDurationHours     = 72
	DefaultDurationHours = 12

	MinKbps     = 64
	MaxKbps     = 1000000
	DefaultDown = 5000
	DefaultUp   = 2000

	MinQuotaBytes     int64 = 10 << 20  // 10 MiB
	MaxQuotaBytes     int64 = 100 << 30 // 100 GiB
	DefaultQuotaBytes int64 = 1 << 30   // 1 GiB

	MinDeviceLimit     = 1
	MaxDeviceLimit     = 20
	DefaultDeviceLimit = 5
)

// ValidateOperatorPolicy holds a policy an operator publishes to the operator bounds (not the structural ones). It returns
// the reason it is refused, or "".
func ValidateOperatorPolicy(p Policy) string {
	switch {
	case p.DurationSeconds%3600 != 0 || p.DurationSeconds/3600 < MinDurationHours || p.DurationSeconds/3600 > MaxDurationHours:
		return "duration must be a whole number of hours between 1 and 72"
	case p.DownKbps < MinKbps || p.DownKbps > MaxKbps:
		return "download speed must be between 64 kbps and 1,000,000 kbps"
	case p.UpKbps < MinKbps || p.UpKbps > MaxKbps:
		return "upload speed must be between 64 kbps and 1,000,000 kbps"
	case p.DataQuotaBytes < MinQuotaBytes || p.DataQuotaBytes > MaxQuotaBytes:
		return "the shared data quota must be between 10 MiB and 100 GiB"
	case p.DeviceLimit < MinDeviceLimit || p.DeviceLimit > MaxDeviceLimit:
		return "the device allowance must be between 1 and 20"
	case p.DeviceLimitPolicy != DeviceLimitRejectNew:
		return "the device policy must be REJECT_NEW_DEVICE"
	}
	return ""
}

type Trigger string

const (
	TriggerCheckoutGrace Trigger = "CHECKOUT_GRACE"
	TriggerEmergency     Trigger = "EMERGENCY_GRACE"
)

// ConversionRequest is evaluated at the effective-checkout boundary; the entitlement's origin is irrelevant.
type ConversionRequest struct {
	HasActiveEntitlementAtCheckout bool
	AlreadyConvertedThisEpisode    bool
	Configured                     Policy
	ConfiguredValid                bool // enabled, server-valid and a pinned package revision exists
	// Disabled is the site's After Check-out Access switch (off): nothing is granted, and the Emergency fallback does
	// not apply either, since "off" is a decision and the fallback answers a broken configuration.
	Disabled bool
}

// Conversion's Policy is the one pinned for this conversion.
type Conversion struct {
	Create             bool
	Trigger            Trigger
	Policy             Policy
	Reason             string
	IsEmergency        bool
	ConfigInvalidAlert bool // raise CHECKOUT_GRACE_CONFIG_INVALID
}

// DecideConversion: an invalid configured policy falls back to Emergency; an eligible Guest is never skipped.
func DecideConversion(r ConversionRequest) Conversion {
	if r.AlreadyConvertedThisEpisode {
		return Conversion{Create: false, Reason: "ALREADY_CONVERTED_THIS_EPISODE"}
	}
	if !r.HasActiveEntitlementAtCheckout {
		return Conversion{Create: false, Reason: "NO_ACTIVE_ENTITLEMENT_AT_CHECKOUT"}
	}
	if r.Disabled {
		return Conversion{Create: false, Reason: "AFTER_CHECKOUT_DISABLED"}
	}
	if r.ConfiguredValid {
		return Conversion{Create: true, Trigger: TriggerCheckoutGrace, Policy: r.Configured, Reason: "ELIGIBLE"}
	}
	return Conversion{Create: true, Trigger: TriggerEmergency, Policy: BuiltinEmergencyPolicy(),
		Reason: "CONFIG_INVALID_EMERGENCY_FALLBACK", IsEmergency: true, ConfigInvalidAlert: true}
}

// AccessRequest: GraceAgeSeconds is measured from effective_checkout_at; DeviceIsExisting is true iff the device
// was already authorized at the checkout boundary.
type AccessRequest struct {
	Policy           Policy
	GraceAgeSeconds  int
	DeviceIsExisting bool
}

type Access struct {
	Allow          bool
	Reason         string
	DownKbps       int
	UpKbps         int
	DataQuotaBytes int64
}

// DecideAccess under REJECT_NEW_DEVICE: a device authorized at the boundary is allowed within the window even
// above the Grace device limit; a new device is always denied, even below the limit; all are denied after the
// window. DeviceLimit never authorizes a new device.
func DecideAccess(r AccessRequest) Access {
	if r.GraceAgeSeconds < 0 || r.GraceAgeSeconds > r.Policy.DurationSeconds {
		return Access{Allow: false, Reason: "GRACE_WINDOW_ELAPSED"}
	}
	if !r.DeviceIsExisting {
		return Access{Allow: false, Reason: "GRACE_NEW_DEVICE_REJECTED"}
	}
	return allow("GRACE_EXISTING_DEVICE_GRANDFATHERED", r.Policy)
}

// ExistingDevicesOverLimit is for admin visibility only; over-limit devices are grandfathered and it never gates
// admission. Zero when the limit is unset (<=0) or not exceeded.
func ExistingDevicesOverLimit(p Policy, existingDeviceCount int) int {
	if p.DeviceLimit <= 0 || existingDeviceCount <= p.DeviceLimit {
		return 0
	}
	return existingDeviceCount - p.DeviceLimit
}

func allow(reason string, p Policy) Access {
	return Access{Allow: true, Reason: reason, DownKbps: p.DownKbps, UpKbps: p.UpKbps, DataQuotaBytes: p.DataQuotaBytes}
}

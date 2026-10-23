// Package grace is the Checkout Grace and Emergency Grace decision core. A checked-out Stay with an active valid
// Entitlement at the effective-checkout boundary gets exactly one Grace conversion per lifecycle episode,
// whatever the prior package's origin or price; no active Entitlement means no Grace. Emergency Grace is the
// built-in fallback when the configured policy is invalid at conversion, not a cloud-outage reauthorization.
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

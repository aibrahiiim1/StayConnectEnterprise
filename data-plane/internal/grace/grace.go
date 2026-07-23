// Package grace is the Increment-7 Checkout Grace + Emergency Grace decision core, per the authoritative
// Product-Owner semantics:
//
//   - ELIGIBILITY: every checked-out Stay that has an ACTIVE VALID Entitlement at the effective-checkout
//     boundary receives exactly ONE Checkout Grace conversion for the current lifecycle episode — regardless
//     of whether the prior package was FREE, PAID, Stay-based or any other approved source. No active
//     Entitlement at checkout ⇒ no Grace. Origin/price NEVER affects eligibility.
//   - grace_duration_seconds is the LIFETIME of the resulting Grace Entitlement (from effective_checkout_at),
//     NOT the eligibility test. Device activity is NOT the eligibility test.
//   - EMERGENCY GRACE is the fallback used when the Hotel-Admin-configured Checkout Grace policy is invalid or
//     unavailable AT the atomic conversion — a versioned built-in policy — NOT a generic cloud-outage
//     reauthorization. Local-first cloud-outage operation merely keeps enforcing already-authoritative local
//     state; it never mints a new Entitlement for a Guest whose access already expired.
//
// This package grants no session and moves no money.
package grace

// Policy is the Grace policy applied to a conversion — either the Hotel-Admin-configured Checkout Grace policy
// or the built-in Emergency fallback. It is PINNED at conversion time so later Admin edits never rewrite an
// existing Guest's Grace terms.
type Policy struct {
	DurationSeconds   int
	DownKbps          int
	UpKbps            int
	DataQuotaBytes    int64
	DeviceLimit       int
	DeviceLimitPolicy string // only "REJECT_NEW_DEVICE" is currently approved
	IsEmergency       bool
}

// DeviceLimitRejectNew is the only currently-approved device-limit policy.
const DeviceLimitRejectNew = "REJECT_NEW_DEVICE"

// BuiltinEmergencyPolicy is the VERSIONED Emergency Grace fallback used when the configured Checkout Grace
// policy is invalid/unavailable at the atomic conversion: 60 min, 5 Mbps down, 2 Mbps up, 500 MB,
// REJECT_NEW_DEVICE.
func BuiltinEmergencyPolicy() Policy {
	return Policy{
		DurationSeconds: 3600, DownKbps: 5000, UpKbps: 2000, DataQuotaBytes: 500 << 20,
		DeviceLimit: 0, DeviceLimitPolicy: DeviceLimitRejectNew, IsEmergency: true,
	}
}

// ValidatePolicy checks a configured Checkout Grace policy SERVER-SIDE (the UI is never the boundary). A
// disabled or malformed policy is INVALID → the caller uses the Emergency fallback. hasPinnedRevision asserts
// a valid hidden/system Checkout Grace package/policy revision is selected.
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

// Trigger identifies which grace path a conversion took.
type Trigger string

const (
	TriggerCheckoutGrace Trigger = "CHECKOUT_GRACE"
	TriggerEmergency     Trigger = "EMERGENCY_GRACE"
)

// ConversionRequest is evaluated at the effective-checkout boundary. HasActiveEntitlementAtCheckout is TRUE if
// the Stay held ANY active valid Entitlement (FREE/PAID/Stay-based/…) at the boundary — origin irrelevant.
type ConversionRequest struct {
	HasActiveEntitlementAtCheckout bool
	AlreadyConvertedThisEpisode    bool // a Grace conversion already exists for this lifecycle episode
	Configured                     Policy
	ConfiguredValid                bool // the Admin policy is enabled + server-valid + a pinned package revision exists
}

// Conversion is the resolved conversion decision at checkout. Policy is the PINNED policy for this conversion.
type Conversion struct {
	Create             bool
	Trigger            Trigger
	Policy             Policy
	Reason             string
	IsEmergency        bool
	ConfigInvalidAlert bool // raise CHECKOUT_GRACE_CONFIG_INVALID
}

// DecideConversion applies the Product-Owner Checkout Grace rule at the effective-checkout boundary. EVERY
// checked-out Stay with an active valid Entitlement gets exactly ONE Grace conversion per lifecycle episode,
// regardless of the prior package's origin or price. If the configured policy is invalid/unavailable, the
// Emergency fallback policy is used (an otherwise-eligible Guest is still converted, never skipped).
func DecideConversion(r ConversionRequest) Conversion {
	if r.AlreadyConvertedThisEpisode {
		return Conversion{Create: false, Reason: "ALREADY_CONVERTED_THIS_EPISODE"} // idempotent
	}
	if !r.HasActiveEntitlementAtCheckout {
		// a Guest who never obtained an Entitlement before checkout gets no new Grace Entitlement.
		return Conversion{Create: false, Reason: "NO_ACTIVE_ENTITLEMENT_AT_CHECKOUT"}
	}
	if r.ConfiguredValid {
		return Conversion{Create: true, Trigger: TriggerCheckoutGrace, Policy: r.Configured, Reason: "ELIGIBLE"}
	}
	// configured policy invalid/unavailable → Emergency fallback (still convert the eligible Guest).
	return Conversion{Create: true, Trigger: TriggerEmergency, Policy: BuiltinEmergencyPolicy(),
		Reason: "CONFIG_INVALID_EMERGENCY_FALLBACK", IsEmergency: true, ConfigInvalidAlert: true}
}

// AccessRequest is evaluated for a device DURING the active Grace window. GraceAgeSeconds is measured from
// effective_checkout_at (the Grace time/quota counters begin at that boundary). DeviceIsExisting is TRUE iff
// the device was already authorized AT the checkout boundary.
type AccessRequest struct {
	Policy           Policy
	GraceAgeSeconds  int
	DeviceIsExisting bool
}

// Access is the per-device access outcome during the Grace window; shaping applies when allowed.
type Access struct {
	Allow          bool
	Reason         string
	DownKbps       int
	UpKbps         int
	DataQuotaBytes int64
}

// DecideAccess governs a device during the Grace window under the approved REJECT_NEW_DEVICE policy:
//   - existing device (authorized at the boundary) within the window → allowed + shaped, GRANDFATHERED even
//     above the configured Grace device limit (a lower limit never disconnects an already-authorized device);
//   - NEW device (not authorized at the boundary) within the window → DENIED, ALWAYS. Being below
//     grace_device_limit does NOT make a new post-checkout device eligible;
//   - any device after the window elapses → DENIED.
//
// DeviceLimit is used for policy validation, pinning, over-limit visibility and audit — never to authorize a
// new device.
func DecideAccess(r AccessRequest) Access {
	if r.GraceAgeSeconds < 0 || r.GraceAgeSeconds > r.Policy.DurationSeconds {
		return Access{Allow: false, Reason: "GRACE_WINDOW_ELAPSED"}
	}
	if !r.DeviceIsExisting {
		return Access{Allow: false, Reason: "GRACE_NEW_DEVICE_REJECTED"} // REJECT_NEW_DEVICE — no limit exception
	}
	return allow("GRACE_EXISTING_DEVICE_GRANDFATHERED", r.Policy)
}

// ExistingDevicesOverLimit reports how many already-authorized devices exceed the configured Grace device
// limit — for Hotel-Admin visibility/audit only. All such devices are grandfathered (none is disconnected);
// this figure never gates admission. Zero when the limit is unset (<=0) or not exceeded.
func ExistingDevicesOverLimit(p Policy, existingDeviceCount int) int {
	if p.DeviceLimit <= 0 || existingDeviceCount <= p.DeviceLimit {
		return 0
	}
	return existingDeviceCount - p.DeviceLimit
}

func allow(reason string, p Policy) Access {
	return Access{Allow: true, Reason: reason, DownKbps: p.DownKbps, UpKbps: p.UpKbps, DataQuotaBytes: p.DataQuotaBytes}
}

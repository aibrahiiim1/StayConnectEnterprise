// Package grace is the Increment-7 Checkout Grace + Emergency Grace decision core. After a Stay checks out,
// EXISTING entitled devices may retain bounded access for a configured grace window (grandfathering); NEW
// devices are rejected (REJECT_NEW_DEVICE). During a cloud outage the appliance serves EXISTING entitled
// devices locally under a bounded EMERGENCY grace so guests are not cut off. All decisions are deterministic
// and fail closed; this package grants no session and moves no money.
package grace

// Config is the site's checkout-grace configuration (from iam_v2.site_checkout_grace_config). A nil/zero
// GraceDurationSeconds means checkout grace is DISABLED (checkout ends access immediately for that Stay).
type Config struct {
	EligibilityWindowSeconds int    // broad window in which Stay-based eligibility still applies (always > 0)
	GraceDurationSeconds     int    // post-checkout grace window; 0 = disabled
	GraceDeviceLimit         int    // optional cap on devices during grace (0 = inherit plan)
	Policy                   string // "REJECT_NEW_DEVICE" or "" (only REJECT_NEW_DEVICE is defined)
	GraceDownKbps            int
	GraceUpKbps              int
	GraceDataQuotaBytes      int64
}

// Request is the runtime state a grace decision needs. Ages are (now − event), seconds; the caller computes
// them from authoritative timestamps (never client input).
type Request struct {
	CheckedOut         bool // the Stay has checked out
	CheckoutAgeSeconds int  // seconds since effective_checkout_at (only meaningful when CheckedOut)
	DeviceIsExisting   bool // this device already held a live entitlement before the boundary
	CloudOutage        bool // the control plane is unreachable → local-first emergency mode
	OutageAgeSeconds   int  // seconds the outage has lasted (only meaningful when CloudOutage)
}

// Mode is the access mode a decision resolves to.
type Mode string

const (
	ModeNormal    Mode = "NORMAL"    // in-house / within entitlement — full plan access
	ModeGrace     Mode = "GRACE"     // post-checkout grace — existing device, grace shaping
	ModeEmergency Mode = "EMERGENCY" // cloud-outage local fallback — existing device, bounded
	ModeDenied    Mode = "DENIED"    // no access
)

// Decision is the resolved access outcome. When Allow is true and Mode is GRACE, the shaping overrides apply.
type Decision struct {
	Allow          bool
	Mode           Mode
	Reason         string
	DownKbps       int
	UpKbps         int
	DataQuotaBytes int64
}

// Decide resolves access under the grace/emergency rules. emergencyGraceSeconds bounds the cloud-outage
// fallback (0 disables emergency grace). Precedence:
//  1. Cloud outage + EXISTING device within the emergency bound → EMERGENCY allow (local-first, don't cut off
//     an already-connected guest during an outage). A NEW device during an outage is still rejected.
//  2. Not checked out → NORMAL allow (grace is irrelevant while in-house).
//  3. Checked out + grace enabled + within the window + EXISTING device → GRACE allow (grandfathered), with
//     grace shaping. A NEW device during grace is rejected (REJECT_NEW_DEVICE).
//  4. Otherwise → DENIED (grace disabled, window elapsed, or a new device).
func Decide(cfg Config, r Request, emergencyGraceSeconds int) Decision {
	// (1) emergency local-first fallback
	if r.CloudOutage {
		if r.DeviceIsExisting && emergencyGraceSeconds > 0 && r.OutageAgeSeconds <= emergencyGraceSeconds {
			return Decision{Allow: true, Mode: ModeEmergency, Reason: "EMERGENCY_GRACE_EXISTING_DEVICE"}
		}
		if !r.DeviceIsExisting {
			return Decision{Allow: false, Mode: ModeDenied, Reason: "EMERGENCY_NEW_DEVICE_REJECTED"}
		}
		return Decision{Allow: false, Mode: ModeDenied, Reason: "EMERGENCY_GRACE_EXPIRED"}
	}

	// (2) still in-house
	if !r.CheckedOut {
		return Decision{Allow: true, Mode: ModeNormal, Reason: "IN_HOUSE"}
	}

	// (3) post-checkout grace
	graceEnabled := cfg.GraceDurationSeconds > 0
	withinWindow := r.CheckoutAgeSeconds >= 0 && r.CheckoutAgeSeconds <= cfg.GraceDurationSeconds
	if graceEnabled && withinWindow {
		if !r.DeviceIsExisting {
			// REJECT_NEW_DEVICE: grace grandfathers existing devices only.
			return Decision{Allow: false, Mode: ModeDenied, Reason: "GRACE_NEW_DEVICE_REJECTED"}
		}
		return Decision{Allow: true, Mode: ModeGrace, Reason: "CHECKOUT_GRACE_EXISTING_DEVICE",
			DownKbps: cfg.GraceDownKbps, UpKbps: cfg.GraceUpKbps, DataQuotaBytes: cfg.GraceDataQuotaBytes}
	}

	// (4) no grace
	if !graceEnabled {
		return Decision{Allow: false, Mode: ModeDenied, Reason: "GRACE_DISABLED"}
	}
	return Decision{Allow: false, Mode: ModeDenied, Reason: "GRACE_WINDOW_ELAPSED"}
}

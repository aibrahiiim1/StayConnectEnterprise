package iamv2

import (
	"fmt"
	"strconv"
	"strings"
)

// PHASE-6 DARK FLAGS.
//
// Same shape as Phases 2 through 5, and for the same reason: a child flag set while the master is off is a
// STARTUP FAILURE rather than a quiet no-op, because a deployment mistake that silently leaves a surface off
// looks identical to a correct dark deployment until somebody needs the feature.
//
// PHASE 6 HAS TWO CONTROLS AND THEY ARE NOT THE SAME THING.
//
//   - This flag set is the DEPLOYMENT GATE. It decides whether the Phase-6 surfaces are mounted at all, and
//     it stays OFF through acceptance until activation is separately authorized. While it is off the routes
//     are ABSENT (404) rather than present-and-refusing.
//   - The per-appliance `guest_device_self_service` setting in iam_v2.appliance_product_settings is the
//     PRODUCT control a hotel operates, and it defaults OFF independently.
//
// The guest surface requires BOTH. Collapsing them into one control would mean that shipping the product
// setting also shipped the activation, which is precisely the boundary this phase is required to keep.
const (
	EnvPhase6Master        = "STAYCONNECT_PHASE6_MASTER"
	EnvPhase6DeviceGuest   = "STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST"
	EnvPhase6DeviceAdmin   = "STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN"
	EnvPhase6AggregateTime = "STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME"
)

// Phase6Config is the guest-device-self-service / aggregate-online-time flag set.
type Phase6Config struct {
	MasterEnabled        bool
	DeviceGuestEnabled   bool
	DeviceAdminEnabled   bool
	AggregateTimeEnabled bool
}

// DeviceGuestOn reports whether the guest device-management surface should be mounted. It answers only the
// DEPLOYMENT question; the per-appliance product setting is a separate and additionally required check that
// lives in the database, because a hotel must be able to turn the feature off without a redeployment.
func (c Phase6Config) DeviceGuestOn() bool { return c.MasterEnabled && c.DeviceGuestEnabled }

// DeviceAdminOn reports whether the operator surface for the per-appliance setting should be mounted.
func (c Phase6Config) DeviceAdminOn() bool { return c.MasterEnabled && c.DeviceAdminEnabled }

// AggregateTimeOn reports whether AGGREGATE_ONLINE_TIME accounting and enforcement are active.
//
// This gate governs the RUNTIME, not the recorded semantics of any package revision. An entitlement whose
// immutable snapshot says AGGREGATE_ONLINE_TIME keeps saying so whether this flag is on or off; the flag only
// decides whether this build acts on it. That separation is what lets the mode be delivered dark without
// making previously stamped revisions ambiguous.
func (c Phase6Config) AggregateTimeOn() bool { return c.MasterEnabled && c.AggregateTimeEnabled }

// Enabled reports whether ANY Phase-6 surface is on.
func (c Phase6Config) Enabled() bool {
	return c.DeviceGuestOn() || c.DeviceAdminOn() || c.AggregateTimeOn()
}

// Validate refuses an incoherent flag set.
func (c Phase6Config) Validate() error {
	if c.MasterEnabled {
		return nil
	}
	for _, f := range []struct {
		on   bool
		name string
	}{
		{c.DeviceGuestEnabled, EnvPhase6DeviceGuest},
		{c.DeviceAdminEnabled, EnvPhase6DeviceAdmin},
		{c.AggregateTimeEnabled, EnvPhase6AggregateTime},
	} {
		if f.on {
			return fmt.Errorf("%s is set while %s is off: a child flag without its master is a deployment "+
				"mistake, and it must be loud rather than silently off anyway", f.name, EnvPhase6Master)
		}
	}
	return nil
}

// LoadPhase6ConfigFromEnv reads the flag set. Every flag defaults OFF and an unparseable value is an error,
// never a false: "true-ish" spelling mistakes must not decide whether a surface exists.
func LoadPhase6ConfigFromEnv(get Getenv) (Phase6Config, error) {
	master, err := parseBoolStrict(EnvPhase6Master, get(EnvPhase6Master))
	if err != nil {
		return Phase6Config{}, err
	}
	guest, err := parseBoolStrict(EnvPhase6DeviceGuest, get(EnvPhase6DeviceGuest))
	if err != nil {
		return Phase6Config{}, err
	}
	admin, err := parseBoolStrict(EnvPhase6DeviceAdmin, get(EnvPhase6DeviceAdmin))
	if err != nil {
		return Phase6Config{}, err
	}
	aggregate, err := parseBoolStrict(EnvPhase6AggregateTime, get(EnvPhase6AggregateTime))
	if err != nil {
		return Phase6Config{}, err
	}
	c := Phase6Config{
		MasterEnabled:        master,
		DeviceGuestEnabled:   guest,
		DeviceAdminEnabled:   admin,
		AggregateTimeEnabled: aggregate,
	}
	if err := c.Validate(); err != nil {
		return Phase6Config{}, err
	}
	return c, nil
}

// SafeFlagSummary is a log-safe description (no secrets).
func (c Phase6Config) SafeFlagSummary() string {
	var b strings.Builder
	b.WriteString("phase6 master=")
	b.WriteString(strconv.FormatBool(c.MasterEnabled))
	b.WriteString(" device_guest=")
	b.WriteString(strconv.FormatBool(c.DeviceGuestEnabled))
	b.WriteString(" device_admin=")
	b.WriteString(strconv.FormatBool(c.DeviceAdminEnabled))
	b.WriteString(" aggregate_online_time=")
	b.WriteString(strconv.FormatBool(c.AggregateTimeEnabled))
	return b.String()
}

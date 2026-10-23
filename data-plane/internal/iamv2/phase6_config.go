package iamv2

import (
	"fmt"
	"strconv"
	"strings"
)

// A child flag set while the master is off is a startup failure, not a quiet no-op.
//
// Two independent controls exist and the guest surface requires BOTH: this flag set is the deployment gate
// (routes are not mounted, 404, while off), and the per-appliance `guest_device_self_service` setting in
// iam_v2.appliance_product_settings is the product control a hotel operates. Collapsing them would make
// shipping the setting also ship the activation.
const (
	EnvPhase6Master        = "STAYCONNECT_PHASE6_MASTER"
	EnvPhase6DeviceGuest   = "STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST"
	EnvPhase6DeviceAdmin   = "STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN"
	EnvPhase6AggregateTime = "STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME"
)

type Phase6Config struct {
	MasterEnabled        bool
	DeviceGuestEnabled   bool
	DeviceAdminEnabled   bool
	AggregateTimeEnabled bool
}

// DeviceGuestOn answers only the deployment question; the per-appliance product setting is a separate,
// additionally required database check, so a hotel can turn the feature off without a redeployment.
func (c Phase6Config) DeviceGuestOn() bool { return c.MasterEnabled && c.DeviceGuestEnabled }

func (c Phase6Config) DeviceAdminOn() bool { return c.MasterEnabled && c.DeviceAdminEnabled }

// AggregateTimeOn governs the runtime only, not the recorded semantics: an entitlement whose immutable snapshot
// says AGGREGATE_ONLINE_TIME keeps saying so; the flag decides whether this build acts on it.
func (c Phase6Config) AggregateTimeOn() bool { return c.MasterEnabled && c.AggregateTimeEnabled }

func (c Phase6Config) Enabled() bool {
	return c.DeviceGuestOn() || c.DeviceAdminOn() || c.AggregateTimeOn()
}

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

// LoadPhase6ConfigFromEnv defaults every flag OFF; an unparseable value is an error, never false.
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

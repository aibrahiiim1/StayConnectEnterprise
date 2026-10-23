package iamv2

import "testing"

func TestPhase6ChildFlagWithoutMasterIsAnError(t *testing.T) {
	for _, child := range []string{EnvPhase6DeviceGuest, EnvPhase6DeviceAdmin, EnvPhase6AggregateTime} {
		env := map[string]string{child: "true"}
		if _, err := LoadPhase6ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Fatalf("%s set with the master off was accepted", child)
		}
	}
}

func TestPhase6DefaultIsDark(t *testing.T) {
	c, err := LoadPhase6ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("an empty environment must load: %v", err)
	}
	if c.Enabled() || c.DeviceGuestOn() || c.DeviceAdminOn() || c.AggregateTimeOn() {
		t.Fatalf("the default configuration is not dark: %s", c.SafeFlagSummary())
	}
}

func TestPhase6UnparseableFlagIsAnErrorNotFalse(t *testing.T) {
	env := map[string]string{EnvPhase6Master: "yes-please"}
	if _, err := LoadPhase6ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatalf("an unparseable master flag was silently treated as off")
	}
}

func TestPhase6MasterAloneMountsNothing(t *testing.T) {
	env := map[string]string{EnvPhase6Master: "true"}
	c, err := LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("master alone must be legal: %v", err)
	}
	if c.Enabled() {
		t.Fatalf("the master flag alone enabled a surface: %s", c.SafeFlagSummary())
	}
}

// The deployment gate and the per-appliance `guest_device_self_service` setting are independent and a guest
// surface requires both: neither may stand in for the other.
func TestPhase6DeploymentGateIsNotTheProductSetting(t *testing.T) {
	env := map[string]string{EnvPhase6Master: "true", EnvPhase6DeviceGuest: "true"}
	c, err := LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("gate on must load: %v", err)
	}
	if !c.DeviceGuestOn() {
		t.Fatalf("the deployment gate did not open with master+child on: %s", c.SafeFlagSummary())
	}
	if c.AggregateTimeOn() {
		t.Fatalf("enabling the device surface also enabled aggregate time: %s", c.SafeFlagSummary())
	}
	if c.DeviceAdminOn() {
		t.Fatalf("enabling the guest surface also enabled the operator surface: %s", c.SafeFlagSummary())
	}
}

// The runtime gate says nothing about recorded package semantics.
func TestPhase6AggregateGateIsRuntimeOnly(t *testing.T) {
	off, err := LoadPhase6ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("empty environment must load: %v", err)
	}
	env := map[string]string{EnvPhase6Master: "true", EnvPhase6AggregateTime: "true"}
	on, err := LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("aggregate gate on must load: %v", err)
	}
	if off.AggregateTimeOn() || !on.AggregateTimeOn() {
		t.Fatalf("the aggregate gate does not follow its flags: off=%s on=%s",
			off.SafeFlagSummary(), on.SafeFlagSummary())
	}
	if on.DeviceGuestOn() || on.DeviceAdminOn() {
		t.Fatalf("the aggregate gate opened a device surface: %s", on.SafeFlagSummary())
	}
}

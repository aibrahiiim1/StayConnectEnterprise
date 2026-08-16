package iamv2

import "testing"

// A child flag without its master must be a STARTUP FAILURE, not a quiet no-op. A deployment mistake that
// leaves a surface silently off looks exactly like a correct dark deployment, and the difference only
// surfaces when somebody needs the feature.
func TestPhase6ChildFlagWithoutMasterIsAnError(t *testing.T) {
	for _, child := range []string{EnvPhase6DeviceGuest, EnvPhase6DeviceAdmin, EnvPhase6AggregateTime} {
		env := map[string]string{child: "true"}
		if _, err := LoadPhase6ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Fatalf("%s set with the master off was accepted", child)
		}
	}
}

// The delivered posture: everything unset is everything off, and nothing is mounted.
func TestPhase6DefaultIsDark(t *testing.T) {
	c, err := LoadPhase6ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("an empty environment must load: %v", err)
	}
	if c.Enabled() || c.DeviceGuestOn() || c.DeviceAdminOn() || c.AggregateTimeOn() {
		t.Fatalf("the default configuration is not dark: %s", c.SafeFlagSummary())
	}
}

// A misspelling must not decide whether a guest-facing surface exists.
func TestPhase6UnparseableFlagIsAnErrorNotFalse(t *testing.T) {
	env := map[string]string{EnvPhase6Master: "yes-please"}
	if _, err := LoadPhase6ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatalf("an unparseable master flag was silently treated as off")
	}
}

// Master on, children off is legal and still mounts nothing: the master alone is not a surface.
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

// THE TWO CONTROLS ARE INDEPENDENT, and this is the test that says so in code rather than in a comment.
//
// The deployment gate answers "are the Phase-6 routes mounted in this build". The per-appliance
// `guest_device_self_service` setting answers "does this hotel offer the feature". A guest surface requires
// BOTH, and neither can stand in for the other: if the gate alone could expose the surface, shipping the
// product control would have shipped the activation, and if the setting alone could, an unauthorized
// deployment could be switched on from the database.
func TestPhase6DeploymentGateIsNotTheProductSetting(t *testing.T) {
	env := map[string]string{EnvPhase6Master: "true", EnvPhase6DeviceGuest: "true"}
	c, err := LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("gate on must load: %v", err)
	}
	if !c.DeviceGuestOn() {
		t.Fatalf("the deployment gate did not open with master+child on: %s", c.SafeFlagSummary())
	}
	// The gate says only that the route may exist. It carries no opinion about any appliance, and there is
	// deliberately no field here that could be mistaken for one.
	if c.AggregateTimeOn() {
		t.Fatalf("enabling the device surface also enabled aggregate time: %s", c.SafeFlagSummary())
	}
	if c.DeviceAdminOn() {
		t.Fatalf("enabling the guest surface also enabled the operator surface: %s", c.SafeFlagSummary())
	}
}

// The runtime gate must not be readable as a statement about recorded package semantics. An entitlement
// whose immutable snapshot says AGGREGATE_ONLINE_TIME keeps saying so whether this build acts on it or not,
// which is what lets the mode ship dark without making already-stamped revisions ambiguous.
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

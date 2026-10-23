package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// These drive the wiring step main.go calls, with config loaded from the environment, to catch ordering
// bugs (the capability was once read before s.phase6 was assigned).

func adminFor(t *testing.T, env map[string]string) *iamv2.CommerceAdmin {
	t.Helper()
	p6, err := iamv2.LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("phase6 config: %v", err)
	}
	admin, err := iamv2.NewCommerceAdmin(iamv2.CommerceConfig{}, nil, iamv2.NopObserver{})
	if err != nil {
		t.Fatalf("commerce admin: %v", err)
	}
	applyAggregateTimeCapability(admin, p6)
	return admin
}

func TestStartupWiringLeavesAggregateCapabilityOff(t *testing.T) {
	if adminFor(t, map[string]string{}).AggregateOnlineTimeAllowed() {
		t.Fatal("a service started with no Phase-6 flags may publish AGGREGATE_ONLINE_TIME revisions")
	}
}

// Master alone does not grant the capability.
func TestStartupWiringNeedsBothFlags(t *testing.T) {
	master := map[string]string{iamv2.EnvPhase6Master: "true"}
	if adminFor(t, master).AggregateOnlineTimeAllowed() {
		t.Fatal("the master flag alone granted the aggregate publication capability")
	}
}

// Regression: with both flags on, the capability must reach the admin.
func TestStartupWiringGrantsTheCapabilityWhenBothFlagsAreOn(t *testing.T) {
	both := map[string]string{
		iamv2.EnvPhase6Master:        "true",
		iamv2.EnvPhase6AggregateTime: "true",
	}
	if !adminFor(t, both).AggregateOnlineTimeAllowed() {
		t.Fatal("the aggregate capability did not reach the commerce admin with master+aggregate ON; " +
			"this is the ordering defect: the config was consumed before it was loaded")
	}
}

// A child flag without its master fails startup rather than silently staying off.
func TestStartupWiringRefusesAnIncoherentFlagPair(t *testing.T) {
	env := map[string]string{iamv2.EnvPhase6AggregateTime: "true"}
	if _, err := iamv2.LoadPhase6ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("the aggregate flag without its master was accepted")
	}
}

func TestApplyAggregateCapabilityToleratesNoAdmin(t *testing.T) {
	applyAggregateTimeCapability(nil, iamv2.Phase6Config{})
}

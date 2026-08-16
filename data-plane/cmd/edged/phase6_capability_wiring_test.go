package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// THE COMPOSITION, NOT THE VALIDATOR.
//
// validatePlanSpec's own tests pass a capability flag the test chose, so they prove the rule and nothing
// about whether the running service ever reaches it with the right value. The defect this closes was
// invisible to every one of them: main.go handed the commerce admin s.phase6.AggregateTimeOn() BEFORE
// anything had assigned s.phase6, so the capability was pinned OFF whatever the environment said. The bug
// lived entirely in the ORDER of two statements.
//
// So these tests drive the wiring step main.go actually calls, with the config loaded from environment
// values the same way, and assert what the admin ended up with.

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

// The default: no flags, no capability. This is every real environment today.
func TestStartupWiringLeavesAggregateCapabilityOff(t *testing.T) {
	if adminFor(t, map[string]string{}).AggregateOnlineTimeAllowed() {
		t.Fatal("a service started with no Phase-6 flags may publish AGGREGATE_ONLINE_TIME revisions")
	}
}

// Master alone is not the capability: turning Phase 6 on is not asking for this mode.
func TestStartupWiringNeedsBothFlags(t *testing.T) {
	master := map[string]string{iamv2.EnvPhase6Master: "true"}
	if adminFor(t, master).AggregateOnlineTimeAllowed() {
		t.Fatal("the master flag alone granted the aggregate publication capability")
	}
}

// THE REGRESSION. With the coherent pair on, the capability must actually arrive -- which it did not before,
// because the config was read before it was loaded.
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

// A child flag without its master is a deployment mistake and must be loud, not silently off -- so the
// startup path fails rather than producing a half-configured admin.
func TestStartupWiringRefusesAnIncoherentFlagPair(t *testing.T) {
	env := map[string]string{iamv2.EnvPhase6AggregateTime: "true"}
	if _, err := iamv2.LoadPhase6ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("the aggregate flag without its master was accepted")
	}
}

// The wiring never panics on a nil admin: a dark build may not construct one at all.
func TestApplyAggregateCapabilityToleratesNoAdmin(t *testing.T) {
	applyAggregateTimeCapability(nil, iamv2.Phase6Config{})
}

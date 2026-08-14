package iamv2

import "testing"

// X-3: a child flag without its master must be a STARTUP FAILURE, not a quiet no-op. A deployment mistake
// that leaves a surface silently off looks exactly like a correct dark deployment, and the difference only
// surfaces when somebody needs the feature.
func TestPhase5ChildFlagWithoutMasterIsAnError(t *testing.T) {
	for _, child := range []string{EnvPhase5PostStayGuest, EnvPhase5PostStayAdmin, EnvPhase5Transfer} {
		env := map[string]string{child: "true"}
		if _, err := LoadPhase5ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Fatalf("%s set with the master off was accepted", child)
		}
	}
}

// The delivered posture: everything unset is everything off, and nothing is mounted.
func TestPhase5DefaultIsDark(t *testing.T) {
	c, err := LoadPhase5ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("an empty environment must load: %v", err)
	}
	if c.Enabled() || c.GuestOn() || c.AdminOn() || c.TransferOn() {
		t.Fatalf("the default configuration is not dark: %s", c.SafeFlagSummary())
	}
}

// A misspelling must not decide whether a guest-facing surface exists.
func TestPhase5UnparseableFlagIsAnErrorNotFalse(t *testing.T) {
	env := map[string]string{EnvPhase5Master: "yes-please"}
	if _, err := LoadPhase5ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatalf("an unparseable master flag was silently treated as off")
	}
}

// Master on, children off is legal and still mounts nothing: the master alone is not a surface.
func TestPhase5MasterAloneMountsNothing(t *testing.T) {
	env := map[string]string{EnvPhase5Master: "true"}
	c, err := LoadPhase5ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("master alone must be legal: %v", err)
	}
	if c.Enabled() {
		t.Fatalf("the master flag alone enabled a surface: %s", c.SafeFlagSummary())
	}
}

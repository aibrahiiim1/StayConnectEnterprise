package iamv2

import "testing"

func TestPhase5ChildFlagWithoutMasterIsAnError(t *testing.T) {
	for _, child := range []string{EnvPhase5PostStayGuest, EnvPhase5PostStayAdmin, EnvPhase5Transfer} {
		env := map[string]string{child: "true"}
		if _, err := LoadPhase5ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Fatalf("%s set with the master off was accepted", child)
		}
	}
}

func TestPhase5DefaultIsDark(t *testing.T) {
	c, err := LoadPhase5ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("an empty environment must load: %v", err)
	}
	if c.Enabled() || c.GuestOn() || c.AdminOn() || c.TransferOn() {
		t.Fatalf("the default configuration is not dark: %s", c.SafeFlagSummary())
	}
}

func TestPhase5UnparseableFlagIsAnErrorNotFalse(t *testing.T) {
	env := map[string]string{EnvPhase5Master: "yes-please"}
	if _, err := LoadPhase5ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatalf("an unparseable master flag was silently treated as off")
	}
}

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

package iamv2

import "testing"

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

// A production build must not be able to select the superseded guest-auth authority, and must say so rather
// than starting in a degraded state. "It isn't taken" is not the same as "it cannot be selected".
func TestProductionRefusesToDisableTheIAMv2GuestAuthority(t *testing.T) {
	for _, tc := range []struct{ name, key string }{
		{"voucher", EnvVoucher},
		{"account", EnvAccount},
	} {
		for _, off := range []string{"false", "0"} {
			cfg := map[string]string{EnvMaster: "true", EnvVoucher: "true", EnvAccount: "true",
				EnvOTP: "false", EnvSocial: "false"}
			cfg[tc.key] = off
			if _, err := LoadConfigFromEnv(env(cfg), true); err == nil {
				t.Fatalf("%s=%s was accepted on a production build: the superseded authority is selectable",
					tc.key, off)
			}
		}
	}
}

// The master switch cannot be used to reach the same end by another route: with it off every method reports
// disabled, which is exactly how the runtime behaved before IAM-v2 existed.
func TestProductionRefusesMasterOff(t *testing.T) {
	if _, err := LoadConfigFromEnv(env(map[string]string{EnvMaster: "false", EnvVoucher: "false",
		EnvAccount: "false", EnvOTP: "false", EnvSocial: "false"}), true); err == nil {
		t.Fatal("master=false was accepted on a production build, leaving the guest IAM authority unset")
	}
}

// Locked ON means locked ON: a production build reports IAM-v2 as the authority for the guest credential
// families even when the environment says nothing at all about them.
func TestProductionLocksTheGuestMethodsOn(t *testing.T) {
	c, err := LoadConfigFromEnv(env(map[string]string{EnvMaster: "true"}), true)
	if err != nil {
		t.Fatalf("a production build with no method flags must still come up: %v", err)
	}
	for _, m := range []Method{MethodVoucher, MethodAccount} {
		if !c.Enabled(m) {
			t.Errorf("%s is not IAM-v2 on a production build", m)
		}
		if !GuestAuthorityLocked(m) {
			t.Errorf("%s should be reported as a locked authority", m)
		}
	}
}

// External-effect methods stay configurable, because enabling them reaches an SMS/email/OAuth provider and
// that is gated by its own decision. They must never become selectable to the SUPERSEDED path either -- but
// "off" here means "not enabled", not "use the old implementation".
func TestExternalEffectMethodsRemainConfigurable(t *testing.T) {
	c, err := LoadConfigFromEnv(env(map[string]string{EnvMaster: "true", EnvOTP: "false", EnvSocial: "false"}), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled(MethodOTP) || c.Enabled(MethodSocial) {
		t.Fatal("OTP/social must not be forced on: they have external effects")
	}
	if GuestAuthorityLocked(MethodOTP) || GuestAuthorityLocked(MethodSocial) {
		t.Fatal("OTP/social are not part of the locked credential families")
	}
}

// The DEVELOPMENT appliance deliberately exercises both authorities and its accepted evidence depends on
// being able to. The lock is a production-build property, not a global one.
func TestNonProductionKeepsTheConfigurableBehaviour(t *testing.T) {
	c, err := LoadConfigFromEnv(env(map[string]string{EnvMaster: "true", EnvVoucher: "false",
		EnvAccount: "false", EnvOTP: "false", EnvSocial: "false"}), false)
	if err != nil {
		t.Fatalf("a development build must still accept a disabled method: %v", err)
	}
	if c.Enabled(MethodVoucher) {
		t.Fatal("development build should honour the flag it was given")
	}
}

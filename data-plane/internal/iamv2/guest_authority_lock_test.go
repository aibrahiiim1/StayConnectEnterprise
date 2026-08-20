package iamv2

import "testing"

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

// locked() drives enforceGuestAuthorityLock the way LoadConfigFromEnv does, but with the build constant
// supplied explicitly -- so a single `go test` run proves the behaviour of BOTH builds, and neither one can
// pass merely because the run happened to be compiled the other way.
func locked(t *testing.T, on bool, e map[string]string) (Config, error) {
	t.Helper()
	methods := map[Method]bool{}
	for m, k := range map[Method]string{MethodVoucher: EnvVoucher, MethodAccount: EnvAccount,
		MethodOTP: EnvOTP, MethodSocial: EnvSocial} {
		v, err := parseBoolStrict(k, e[k])
		if err != nil {
			return Config{}, err
		}
		methods[m] = v
	}
	master, err := parseBoolStrict(EnvMaster, e[EnvMaster])
	if err != nil {
		return Config{}, err
	}
	c := Config{MasterEnabled: master, Methods: methods}
	if err := enforceGuestAuthorityLock(&c, on, env(e)); err != nil {
		return Config{}, err
	}
	return c, c.Validate()
}

// A production build must not be able to select the superseded guest-auth authority, and must say so rather
// than starting in a degraded state. "It isn't taken" is not the same as "it cannot be selected".
func TestProductionRefusesToDisableTheIAMv2GuestAuthority(t *testing.T) {
	for _, key := range []string{EnvVoucher, EnvAccount} {
		for _, off := range []string{"false", "0"} {
			e := map[string]string{EnvMaster: "true", EnvVoucher: "true", EnvAccount: "true"}
			e[key] = off
			if _, err := locked(t, true, e); err == nil {
				t.Errorf("%s=%s was accepted on a production build: the superseded authority is selectable",
					key, off)
			}
		}
	}
}

// The master switch cannot be used to reach the same end by another route: with it off every method reports
// disabled, which is exactly how the runtime behaved before IAM-v2 existed.
func TestProductionRefusesMasterOff(t *testing.T) {
	if _, err := locked(t, true, map[string]string{EnvMaster: "false"}); err == nil {
		t.Fatal("master=false was accepted on a production build, leaving the guest IAM authority unset")
	}
}

// Locked ON means locked ON: a production build reports IAM-v2 as the authority for the guest credential
// families even when the environment says nothing at all about them.
func TestProductionLocksTheGuestMethodsOn(t *testing.T) {
	c, err := locked(t, true, map[string]string{EnvMaster: "true"})
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
// that is gated by its own decision. They are never redirected to the superseded path either -- "off" here
// means "not enabled", not "use the old implementation".
func TestExternalEffectMethodsRemainConfigurable(t *testing.T) {
	c, err := locked(t, true, map[string]string{EnvMaster: "true", EnvOTP: "false", EnvSocial: "false"})
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
// being able to. The lock is a production-BUILD property, not a global one.
func TestDevelopmentBuildKeepsTheConfigurableBehaviour(t *testing.T) {
	c, err := locked(t, false, map[string]string{EnvMaster: "true", EnvVoucher: "false", EnvAccount: "false"})
	if err != nil {
		t.Fatalf("a development build must still accept a disabled method: %v", err)
	}
	if c.Enabled(MethodVoucher) || c.Enabled(MethodAccount) {
		t.Fatal("development build should honour the flags it was given")
	}
	if _, err := locked(t, false, map[string]string{}); err != nil {
		t.Fatalf("a development build must still accept the all-off dark default: %v", err)
	}
}

// The lock must be reachable only through the build, never through the environment. If any STAYCONNECT_IAMV2_*
// value could turn it on or off, it would be exactly the configuration switch it exists to remove.
func TestTheLockItselfIsNotConfigurable(t *testing.T) {
	for _, k := range []string{EnvMaster, EnvVoucher, EnvAccount, EnvOTP, EnvSocial, EnvSocialStub} {
		for _, v := range []string{"true", "false", "1", "0"} {
			if _, err := locked(t, false, map[string]string{EnvMaster: "true", k: v}); err != nil && v != "0" && v != "false" {
				t.Errorf("%s=%s changed development behaviour unexpectedly: %v", k, v, err)
			}
		}
	}
	// And the profile the binary reports is the constant, not anything read from the environment.
	want := "development"
	if productionBuild {
		want = "production"
	}
	if BuildProfile() != want {
		t.Fatalf("BuildProfile() = %q, want %q", BuildProfile(), want)
	}
}

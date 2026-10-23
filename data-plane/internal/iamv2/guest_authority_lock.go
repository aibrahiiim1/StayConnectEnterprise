package iamv2

// The guest IAM authority is not configurable on a production build. This is a defensive assertion: a
// configuration such as STAYCONNECT_IAMV2_VOUCHER=false assumes another authority exists, and none does.
// Without the guard the appliance would start and silently refuse every guest; with it, it refuses to start
// and names the variable.
// Not locked: MethodOTP and MethodSocial (external providers gated by their own decisions) and operator
// authentication (public.operators).
// Keyed on the build tag stayconnect_production (see build_profile_*.go); the development build keeps the
// configurable behaviour.

// VOUCHER and ACCOUNT are the credential families whose authority is IAM-v2 and must never be selectable
// otherwise.
var lockedGuestMethods = []Method{MethodVoucher, MethodAccount}

func GuestAuthorityLocked(m Method) bool {
	for _, lm := range lockedGuestMethods {
		if lm == m {
			return true
		}
	}
	return false
}

// Reported in SafeFlagSummary at startup so the running profile is visible in a log.
func BuildProfile() string {
	if productionBuild {
		return "production"
	}
	return "development"
}

// Rejects configuration that selects the superseded authority and forces the locked methods on. locked is a
// parameter only so both builds can be tested from either one. Runs at startup: a disagreeing configuration
// does not come up degraded, it does not come up.
func enforceGuestAuthorityLock(c *Config, locked bool, get Getenv) error {
	if !locked {
		// Development and test builds stay configurable.
		return nil
	}
	if c.Methods == nil {
		c.Methods = map[Method]bool{}
	}
	for _, m := range lockedGuestMethods {
		// An explicit "false" is refused with the variable named, rather than silently overriding the
		// operator's intent.
		if raw := get(envForMethod(m)); raw == "false" || raw == "0" {
			return &Error{Code: ErrConfig, Msg: "refusing to start: " + envForMethod(m) + " is set to disable " +
				"IAM-v2 for " + string(m) + ", but the superseded guest-auth authority has been removed from " +
				"production. IAM-v2 is the only guest IAM authority and this setting cannot select another one"}
		}
		c.Methods[m] = true
	}
	// The master switch cannot be off either: Enabled() would be false for every method.
	if !c.MasterEnabled {
		return &Error{Code: ErrConfig, Msg: "refusing to start: " + EnvMaster + " is off, which would leave " +
			"the guest IAM authority unset. On a production build IAM-v2 is the only guest IAM authority"}
	}
	return nil
}

// Kept next to the lock so a refusal can name the exact setting to remove.
func envForMethod(m Method) string {
	switch m {
	case MethodVoucher:
		return EnvVoucher
	case MethodAccount:
		return EnvAccount
	case MethodOTP:
		return EnvOTP
	case MethodSocial:
		return EnvSocial
	}
	return string(m)
}

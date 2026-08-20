package iamv2

// THE GUEST IAM AUTHORITY IS NOT CONFIGURABLE ON A PRODUCTION BUILD.
//
// WHAT THIS GUARD IS NOW
// ----------------------
// It is a DEFENSIVE ASSERTION, and that is a downgrade in its importance which is worth stating plainly.
//
// It was written when the superseded guest-auth implementation was still compiled in and one environment
// variable away from being the authority. It is retained now that the implementation itself has been removed
// -- the public-schema credential, session, voucher and plan tables are gone, and so is the code that read
// them -- so there is no longer a second authority for a setting to select.
//
// WHY KEEP IT AT ALL
// ------------------
// Because the failure it catches did not go away with the code. A configuration that says
// STAYCONNECT_IAMV2_VOUCHER=false is a statement of intent by whoever wrote it: they believe there is
// another authority and they want it. There isn't. Without this guard the appliance would start, report
// VOUCHER disabled, and refuse every guest with no explanation of why the operator's setting had no effect.
// With it, the appliance refuses to start and names the exact variable.
//
// So it no longer guards a selectable implementation; it guards against a configuration that has become
// meaningless, which is a real and likely mistake during an upgrade from a pre-removal release.
//
// WHAT IS NOT LOCKED, AND WHY
// ---------------------------
//   * MethodOTP and MethodSocial reach EXTERNAL providers (SMS, email, an OAuth identity provider). Locking
//     them ON would enable outbound effects that are gated by their own Product-Owner decisions, so they stay
//     configurable -- and there is no longer any implementation for them to be redirected to.
//   * Operator authentication is untouched. public.operators is a live platform foundation, not superseded
//     guest IAM, and iam_v2.publish_checkout_grace_policy validates its actor against it.
//
// The lock is keyed on a BUILD TAG (`stayconnect_production`, see build_profile_*.go). The DEVELOPMENT
// appliance is built WITHOUT that tag and keeps the behaviour its accepted trial evidence depends on.

// lockedGuestMethods are the guest IAM methods whose authority is IAM-v2 and cannot be configured otherwise.
// VOUCHER and ACCOUNT are the two credential families that have a superseded public-schema implementation
// still present in the tree; these are exactly the paths that must never be selectable again.
var lockedGuestMethods = []Method{MethodVoucher, MethodAccount}

// GuestAuthorityLocked reports whether m is a guest method whose authority is fixed to IAM-v2.
func GuestAuthorityLocked(m Method) bool {
	for _, lm := range lockedGuestMethods {
		if lm == m {
			return true
		}
	}
	return false
}

// BuildProfile names the build the binary was produced from: "production" when built with
// `-tags stayconnect_production`, "development" otherwise. Reported in SafeFlagSummary at startup so the
// running profile is visible in a log rather than inferred.
func BuildProfile() string {
	if productionBuild {
		return "production"
	}
	return "development"
}

// enforceGuestAuthorityLock rejects any configuration that would select the superseded guest-auth authority,
// and forces the locked methods on. `locked` is the production-build constant; it is a parameter only so the
// behaviour of BOTH builds can be proven from either one.
//
// It runs at STARTUP, before anything is served. An appliance whose configuration disagrees with the locked
// authority does not come up degraded -- it does not come up.
func enforceGuestAuthorityLock(c *Config, locked bool, get Getenv) error {
	if !locked {
		// Development and test builds keep the configurable behaviour, because the DEVELOPMENT appliance
		// deliberately exercises both authorities and its accepted evidence depends on being able to.
		return nil
	}
	if c.Methods == nil {
		c.Methods = map[Method]bool{}
	}
	for _, m := range lockedGuestMethods {
		// An EXPLICIT "false" is the dangerous case: somebody has written down that they want the superseded
		// path. Refuse, and say which variable and why, rather than quietly overriding an operator's intent.
		if raw := get(envForMethod(m)); raw == "false" || raw == "0" {
			return &Error{Code: ErrConfig, Msg: "refusing to start: " + envForMethod(m) + " is set to disable " +
				"IAM-v2 for " + string(m) + ", but the superseded guest-auth authority has been removed from " +
				"production. IAM-v2 is the only guest IAM authority and this setting cannot select another one"}
		}
		c.Methods[m] = true
	}
	// The master switch cannot be off either: with it off, Enabled() is false for every method and the guest
	// paths would look to the runtime exactly as they did before IAM-v2 existed.
	if !c.MasterEnabled {
		return &Error{Code: ErrConfig, Msg: "refusing to start: " + EnvMaster + " is off, which would leave " +
			"the guest IAM authority unset. On a production build IAM-v2 is the only guest IAM authority"}
	}
	return nil
}

// envForMethod maps a method back to the environment variable that used to select its authority. Kept next to
// the lock so a refusal can name the exact setting an operator has to remove.
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

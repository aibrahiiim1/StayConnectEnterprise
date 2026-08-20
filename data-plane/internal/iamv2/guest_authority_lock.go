package iamv2

// THE GUEST IAM AUTHORITY IS NOT CONFIGURABLE ON A PRODUCTION BUILD.
//
// WHY THIS FILE EXISTS
// --------------------
// The Production objective is zero superseded ACTIVE runtime dependency. Until now the guest authority was
// chosen per method by environment variable: with STAYCONNECT_IAMV2_VOUCHER unset the voucher path fell back
// to the superseded public-schema implementation, and the same for accounts, OTP and social. Every legacy
// branch stayed compiled in and one environment variable away from being live again.
//
// A fallback that only "isn't taken" is not removed. It is a configuration mistake, a bad rollback, or a
// half-restored env file away from being the authority again -- and it would fail in the direction of the
// superseded system rather than refusing to run.
//
// So on a production build the guest IAM authority is LOCKED to IAM-v2, and any configuration that tries to
// select the superseded path is a STARTUP FAILURE rather than a silent downgrade. Fail closed: refuse to
// serve rather than serve from the wrong authority.
//
// WHAT IS NOT LOCKED, AND WHY
// ---------------------------
//   * MethodOTP and MethodSocial reach EXTERNAL providers (SMS, email, an OAuth identity provider). Locking
//     them ON would enable outbound effects that are gated by their own Product-Owner decisions, so they stay
//     configurable -- but they can only ever be configured ON, never redirected to the superseded path.
//   * Operator authentication is untouched. public.operators is a live platform foundation, not superseded
//     guest IAM, and iam_v2.publish_checkout_grace_policy validates its actor against it.

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

// enforceGuestAuthorityLock rejects any production configuration that would select the superseded guest-auth
// authority, and forces the locked methods on.
//
// It runs at STARTUP, before anything is served. An appliance whose configuration disagrees with the locked
// authority does not come up degraded -- it does not come up.
func enforceGuestAuthorityLock(c *Config, productionProfile bool, get Getenv) error {
	if !productionProfile {
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

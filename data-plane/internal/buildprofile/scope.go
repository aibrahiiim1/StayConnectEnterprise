package buildprofile

// LegacyEnvScopeAllowed reports whether tenant/site may come from legacy env variables when no signed assignment
// exists. Only a devlicense build without stayconnect_production may; every other binary uses the Central-signed
// assignment only. The "assignment file present" rule lives in assignment.EnvFallbackAllowed.
func LegacyEnvScopeAllowed() bool { return !Production && !stayconnectProduction }

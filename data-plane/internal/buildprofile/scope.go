package buildprofile

// LegacyEnvScopeAllowed reports whether a daemon may take its tenant/site from the legacy environment variables
// (SCD_TENANT_ID/SITE_ID, EDGED_TENANT_ID/SITE_ID, ACCTD_TENANT_ID/APPLIANCE_ID) when NO signed assignment
// exists at all.
//
// Only an explicit development build may: one compiled with `-tags devlicense` and WITHOUT
// `-tags stayconnect_production`. Every other binary -- the default build and any stayconnect_production build --
// resolves tenant and site from the verified, Central-signed assignment and nothing else
// (docs/CENTRAL_CONTROL_PLANE.md section 5). Even a development build never falls back when an assignment file
// is PRESENT; that rule lives with the resolver (assignment.EnvFallbackAllowed), not here.
func LegacyEnvScopeAllowed() bool { return !Production && !stayconnectProduction }

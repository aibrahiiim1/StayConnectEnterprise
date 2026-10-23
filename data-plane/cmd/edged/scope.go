package main

import (
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// resolveSiteScope returns the tenant and site from the VERIFIED assignment (signature, trust registry, binding), the same resolver scd uses.
// Anything that does not verify yields no scope ("awaiting assignment"): fail closed.
// EDGED_TENANT_ID/EDGED_SITE_ID is honoured only with no assignment and in an explicit development build (assignment.EnvFallbackAllowed).
func resolveSiteScope(getenv func(string) string) (tenantID, siteID string, r assignment.Resolution) {
	identityDir := getenv("EDGED_IDENTITY_DIR")
	if identityDir == "" {
		identityDir = "/etc/stayconnect/identity"
	}
	r, _ = assignment.ResolveLocal(assignment.PathsFromEnv(getenv, "EDGED"), identityDir, time.Now(), slog.Default())
	switch {
	case r.Assigned():
		return r.TenantID, r.SiteID, r
	case assignment.EnvFallbackAllowed(r) && getenv("EDGED_TENANT_ID") != "" && getenv("EDGED_SITE_ID") != "":
		slog.Warn("no signed assignment; development build using legacy env tenant/site")
		return getenv("EDGED_TENANT_ID"), getenv("EDGED_SITE_ID"), r
	}
	return "", "", r
}

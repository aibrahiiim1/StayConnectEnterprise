package main

import (
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// resolveSiteScope decides which tenant and site Hotel Admin operates on.
//
// It is the VERIFIED assignment -- signature, trust registry and binding to this appliance -- from the same
// resolver scd uses. edged used to read assignment.json raw through Store.Resolved(), so a document scd refused
// as unverifiable was still the tenant/site the admin surface operated under. Anything that does not verify
// yields no tenant and no site ("awaiting assignment"), which is fail-closed.
//
// The legacy EDGED_TENANT_ID/EDGED_SITE_ID pair is honoured only when NO assignment exists and only in an
// explicit development build (assignment.EnvFallbackAllowed); a production appliance never takes its scope
// from the environment.
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

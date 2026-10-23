package main

import (
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

type acctdScope struct {
	TenantID    string
	SiteID      string
	ApplianceID string
	Version     int64
	Outcome     assignment.Outcome
}

// resolveAcctdScope takes the customer from the VERIFIED assignment: anything that does not verify yields an
// empty scope and accounting pauses rather than billing a tenant it cannot stand behind. ACCTD_TENANT_ID /
// ACCTD_APPLIANCE_ID apply only with no assignment present, in an explicit development build.
func resolveAcctdScope(getenv func(string) string) acctdScope {
	identityDir := getenv("ACCTD_IDENTITY_DIR")
	if identityDir == "" {
		identityDir = "/etc/stayconnect/identity"
	}
	r, applianceID := assignment.ResolveLocal(assignment.PathsFromEnv(getenv, "ACCTD"), identityDir, time.Now(), slog.Default())
	switch {
	case r.Assigned():
		return acctdScope{TenantID: r.TenantID, SiteID: r.SiteID, ApplianceID: applianceID, Version: r.Version, Outcome: r.Outcome}
	case assignment.EnvFallbackAllowed(r) && getenv("ACCTD_TENANT_ID") != "" && getenv("ACCTD_APPLIANCE_ID") != "":
		slog.Warn("acctd: no signed assignment; development build using legacy env tenant/appliance")
		return acctdScope{TenantID: getenv("ACCTD_TENANT_ID"), ApplianceID: getenv("ACCTD_APPLIANCE_ID"), Outcome: r.Outcome}
	}
	return acctdScope{ApplianceID: applianceID, Outcome: r.Outcome}
}

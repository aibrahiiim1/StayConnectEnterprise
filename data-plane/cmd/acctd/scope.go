package main

import (
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// acctdScope is who accounting rows are attributed to.
type acctdScope struct {
	TenantID    string
	SiteID      string
	ApplianceID string
	Version     int64
	Outcome     assignment.Outcome
}

// resolveAcctdScope resolves the customer accounting is attributed to from the VERIFIED assignment -- the same
// resolver scd uses -- and the appliance id from the registered public identity.
//
// acctd used to read assignment.json raw (Store.Resolved), so a document that failed verification still decided
// which customer usage was billed to. Anything that does not verify now yields an empty scope, and acctd pauses
// accounting rather than attributing usage to a tenant it cannot stand behind.
//
// ACCTD_TENANT_ID / ACCTD_APPLIANCE_ID are honoured only with NO assignment present and only in an explicit
// development build (assignment.EnvFallbackAllowed).
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

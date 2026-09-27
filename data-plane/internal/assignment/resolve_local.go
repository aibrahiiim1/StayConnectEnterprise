package assignment

// THE VERIFIED SCOPE FOR A DAEMON THAT HOLDS ONLY THE PUBLIC IDENTITY.
//
// edged, acctd and netd used to read assignment.json through Store.Resolved(), which returns whatever the file
// says: no signature check, no trust registry, no binding to this appliance. scd and pmsd verified; the other
// three took the file's word. So a tenant/site that scd refused as unverifiable was still the tenant/site that
// Hotel Admin, accounting and the enforcement plane operated under. ResolveLocal gives those daemons the same
// answer scd gets, from the same function (Resolve), with the same fail-closed default.

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

// PathsFromEnv builds the assignment paths for a daemon from its own environment prefix (EDGED, ACCTD, NETD,
// ...), with the appliance-wide defaults every daemon shares.
func PathsFromEnv(getenv func(string) string, prefix string) Paths {
	or := func(k, d string) string {
		if v := getenv(prefix + "_" + k); v != "" {
			return v
		}
		return d
	}
	return Paths{
		Dir:              or("ASSIGNMENT_DIR", "/etc/stayconnect/assignment"),
		RegistryPath:     or("ASSIGNMENT_REGISTRY", "/etc/stayconnect/assignment/registry.json"),
		RegistryRootPath: or("ASSIGNMENT_REGISTRY_ROOT", "/etc/stayconnect/assignment-registry-root.pub"),
		TrustPath:        or("ASSIGNMENT_TRUST", "/etc/stayconnect/assignment-trust.json"),
	}
}

// ResolveLocal resolves this appliance's tenant/site exactly as scd does -- signature, trust registry and binding
// to THIS appliance -- using only the public identity (identity.json), so a daemon that must not hold the
// private key can still verify. It also returns the appliance id it verified against ("" when none).
//
// FAIL CLOSED. An identity that is present but unreadable, or an assignment that exists while no registered
// identity does, is Unverifiable: an assignment cannot be bound to an appliance that cannot say who it is.
func ResolveLocal(p Paths, identityDir string, now time.Time, log *slog.Logger) (Resolution, string) {
	ident, err := (&identity.Store{Dir: identityDir}).LoadPublic()
	if err != nil {
		if log != nil {
			log.Error("assignment: the appliance identity is present but unreadable — refusing any tenant/site",
				"err", err.Error())
		}
		return Resolution{Outcome: OutcomeUnverifiable}, ""
	}
	if ident == nil || ident.ApplianceID == "" {
		if assignmentFileExists(p.Dir) {
			if log != nil {
				log.Error("assignment: an assignment is present but this appliance has no registered identity — refusing it")
			}
			return Resolution{Outcome: OutcomeUnverifiable}, ""
		}
		return Resolution{Outcome: OutcomeAbsent}, ""
	}
	r := Resolve(p, ApplianceBinding{
		ApplianceID: ident.ApplianceID, Serial: ident.Serial, PublicKeyB64: ident.PublicKeyB64,
	}, now, log)
	return r, ident.ApplianceID
}

// EnvFallbackAllowed reports whether a daemon may use its legacy environment tenant/site for this resolution:
// only when NO assignment exists at all, and only in an explicit development build. A present assignment that
// failed verification, or one that verified and grants nothing, is never papered over by an env var.
func EnvFallbackAllowed(r Resolution) bool {
	return r.Outcome == OutcomeAbsent && buildprofile.LegacyEnvScopeAllowed()
}

func assignmentFileExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "assignment.json"))
	return err == nil || !os.IsNotExist(err)
}

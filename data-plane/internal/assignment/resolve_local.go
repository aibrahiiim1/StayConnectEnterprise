package assignment

// The verified scope for a daemon holding only the public identity (edged, acctd, netd): the same signature, registry and binding checks and fail-closed
// default as scd, via Resolve. Store.Resolved() takes the file's word and must not be used as authority.

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

// Builds the assignment paths from the daemon's environment prefix (EDGED, ACCTD, NETD, ...) with the shared appliance-wide defaults.
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

// Resolves tenant/site exactly as scd does (signature, registry, binding to THIS appliance) using only the public identity, so a daemon that must not
// hold the private key can still verify; also returns the appliance id verified against. FAIL CLOSED: an unreadable identity, or an assignment with
// no registered identity, is Unverifiable.
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

// Whether a daemon may use its legacy environment tenant/site: only when NO assignment exists and only in an explicit development build; a failed or
// non-granting assignment is never papered over by an env var.
func EnvFallbackAllowed(r Resolution) bool {
	return r.Outcome == OutcomeAbsent && buildprofile.LegacyEnvScopeAllowed()
}

func assignmentFileExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "assignment.json"))
	return err == nil || !os.IsNotExist(err)
}

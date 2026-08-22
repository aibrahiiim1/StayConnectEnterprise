package pmsd

// WHERE pmsd GETS ITS TENANT AND SITE, AND WHY IT IS NOT ALLOWED A SECOND SOURCE.
//
// This file used to define pmsd's OWN signed-assignment document: its own JSON schema, its own canonical
// signing bytes, its own Ed25519 verification, and a loader that read it from a path. Provisioning it meant
// generating a keypair on the appliance and signing a document that asserted an appliance, a tenant and a
// site.
//
// That was a second assignment authority. The key was local, nothing in the Central chain had blessed it, and
// nothing cross-checked the result against the canonical assignment — so anyone who could write that key and
// that file could scope the PMS connector to any tenant they chose, and the real, Central-signed assignment
// would never have been consulted. A connector reading a property's live guest roster is precisely the wrong
// component to give an independent way of deciding whose property it is.
//
// It is replaced by the canonical chain that every other daemon already uses: the Central-signed assignment
// document, re-verified on every scope load against the trust registry anchored by the manufacture-time
// registry root, and bound to THIS appliance's identity. pmsd verifies; it cannot issue.

import (
	"context"
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

// CentralAssignmentLoader builds a Deps.LoadAssignment that resolves tenant/site from the canonical
// Central-signed assignment via internal/assignment.Resolve.
//
// FAIL CLOSED, AND FACTORY-CLEAN IS NOT A FAILURE. The two are different answers and the caller treats them
// differently:
//
//   - assigned=false, err=nil — no assignment has been adopted, or the appliance has no identity yet. This
//     is a factory-clean box, and the correct behaviour is to do no PMS work at all. Run() logs it and stops.
//   - err != nil — an assignment IS present but the appliance cannot stand behind it: unverifiable signature,
//     a signer outside the registry, a document bound to a different appliance, or a state that grants
//     nothing. Refusing loudly is the point; silently degrading to "unassigned" would make a rejected
//     assignment indistinguishable from never having had one.
//
// identityDir and the assignment paths come from the daemon's environment, so a test or an offline tool can
// point them elsewhere without this package knowing any absolute path.
func CentralAssignmentLoader(paths assignment.Paths, identityDir string, log *slog.Logger) func(context.Context) (Assignment, bool, error) {
	return func(context.Context) (Assignment, bool, error) {
		// PUBLIC identity only. pmsd needs to know WHICH appliance this is so the assignment can be checked
		// against it; it signs nothing, so it has no business reading the appliance private key — and could
		// not anyway, since ed25519.key is 0600 root-only while pmsd runs under its own service account.
		//
		// No ApplianceID means enrolment has not completed. That is factory-clean, not a fault.
		ident, err := (&identity.Store{Dir: identityDir}).LoadPublic()
		if err != nil || ident == nil || ident.ApplianceID == "" {
			return Assignment{}, false, nil // awaiting enrolment → no PMS work
		}

		r := assignment.Resolve(paths, assignment.ApplianceBinding{
			ApplianceID:  ident.ApplianceID,
			Serial:       ident.Serial,
			PublicKeyB64: ident.PublicKeyB64,
		}, time.Now(), log)

		switch {
		case r.State == "":
			// Nothing adopted, or nothing readable. Factory-clean.
			return Assignment{}, false, nil
		case !r.Assigned():
			// A document exists but grants no scope — revoked, unassigned, decommissioned, or it failed
			// verification. Either way pmsd must not run, and must not pretend the appliance is merely new.
			return Assignment{}, false, ErrAssignmentNotGranting
		}
		return Assignment{
			ApplianceID: ident.ApplianceID,
			TenantID:    r.TenantID,
			SiteID:      r.SiteID,
		}, true, nil
	}
}

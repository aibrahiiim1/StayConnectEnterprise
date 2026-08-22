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
//   - assigned=false, err=nil — OutcomeAbsent, or no identity.json, or an identity awaiting enrolment. This
//     is a factory-clean box, and the correct behaviour is to do no PMS work at all. Run() logs it and stops.
//   - ErrIdentityUnreadable — identity.json EXISTS and cannot be trusted: unreadable, corrupt, or carrying
//     neither an appliance id nor a public key. Distinct from having no identity at all, for the same reason
//     OutcomeUnverifiable is distinct from OutcomeAbsent.
//   - err != nil — OutcomeUnverifiable or OutcomeNotGranting: an assignment IS present and the appliance
//     cannot stand behind it (bad signature, signer outside the registry, bound to a different appliance,
//     unreadable file) or it verified and grants nothing (unassigned, revoked, decommissioned). Refusing
//     loudly is the point; silently degrading to "unassigned" would make a rejected assignment
//     indistinguishable from never having had one.
//
// identityDir and the assignment paths come from the daemon's environment, so a test or an offline tool can
// point them elsewhere without this package knowing any absolute path.
func CentralAssignmentLoader(paths assignment.Paths, identityDir string, log *slog.Logger) func(context.Context) (Assignment, bool, error) {
	return func(context.Context) (Assignment, bool, error) {
		// PUBLIC identity only. pmsd needs to know WHICH appliance this is so the assignment can be checked
		// against it; it signs nothing, so it has no business reading the appliance private key — and could
		// not anyway, since ed25519.key is 0600 root-only while pmsd runs under its own service account.
		//
		// THE THREE IDENTITY STATES ARE NOT ONE STATE. This branch used to read
		//
		//	if err != nil || ident == nil || ident.ApplianceID == "" { return ..., false, nil }
		//
		// which folded a CORRUPT identity.json into the factory-clean case — the same collapse the assignment
		// Outcome was introduced to fix, one layer down. LoadPublic already reports them apart: (nil, nil) for
		// a file that does not exist, (nil, err) for one that does and cannot be read or parsed. Discarding
		// that distinction here meant an unreadable or tampered identity produced "no assignment", exit 0, and
		// a log line indistinguishable from a box that had simply never been enrolled.
		ident, err := (&identity.Store{Dir: identityDir}).LoadPublic()
		switch {
		case err != nil:
			// The file EXISTS and cannot be trusted. Fail loudly; never describe it as factory-clean.
			if log != nil {
				log.Error("pmsd: refusing to start — the appliance identity is present but unreadable",
					"err", err.Error())
			}
			return Assignment{}, false, ErrIdentityUnreadable
		case ident == nil:
			return Assignment{}, false, nil // no identity.json at all → factory-clean
		case ident.ApplianceID == "" && ident.PublicKeyB64 == "":
			// Parsed, but carries neither an appliance id nor a key. A pre-enrolment identity always has the
			// public half (EnsureLocalKeypair writes it); one with nothing in it is a damaged file wearing
			// valid JSON, and treating it as "not enrolled yet" would hide that.
			if log != nil {
				log.Error("pmsd: refusing to start — the appliance identity carries no appliance id and no public key")
			}
			return Assignment{}, false, ErrIdentityUnreadable
		case ident.ApplianceID == "":
			// Valid identity, enrolment not completed: a keypair exists, Central has not minted an appliance
			// id. Genuinely nothing to do, and genuinely not a fault.
			return Assignment{}, false, nil
		}

		r := assignment.Resolve(paths, assignment.ApplianceBinding{
			ApplianceID:  ident.ApplianceID,
			Serial:       ident.Serial,
			PublicKeyB64: ident.PublicKeyB64,
		}, time.Now(), log)

		// The DECISION IS THE OUTCOME, not the emptiness of the fields.
		//
		// This used to switch on `r.State == ""`, and State is empty for BOTH a factory-clean appliance and a
		// document that failed verification — so a bad signature, a signer outside the registry, a binding to
		// another appliance or an unreadable file all took the factory-clean branch and returned
		// assigned=false with no error. pmsd then logged "no assignment" and exited 0, which is the report an
		// unassigned box gives. A REJECTED appliance looked like a NEW one, and the loudest possible failure
		// became the quietest.
		switch r.Outcome {
		case assignment.OutcomeAbsent:
			return Assignment{}, false, nil // genuinely not assigned → do no PMS work, cleanly
		case assignment.OutcomeUnverifiable, assignment.OutcomeNotGranting:
			if log != nil {
				log.Error("pmsd: refusing to start — an appliance assignment is present but confers no scope",
					"outcome", r.Outcome.String(), "state", r.State, "version", r.Version)
			}
			return Assignment{}, false, ErrAssignmentNotGranting
		}
		return Assignment{
			ApplianceID: ident.ApplianceID,
			TenantID:    r.TenantID,
			SiteID:      r.SiteID,
		}, true, nil
	}
}

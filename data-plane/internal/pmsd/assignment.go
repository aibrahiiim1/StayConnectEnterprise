package pmsd

// pmsd takes its tenant and site only from the Central-signed assignment, re-verified on every scope load and
// bound to this appliance's identity. It must never have a second, locally issued source of scope.

import (
	"context"
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

// CentralAssignmentLoader resolves tenant/site from the Central-signed assignment and fails closed:
//
//   - assigned=false, err=nil: factory-clean (no identity, awaiting enrolment, or OutcomeAbsent); do no PMS work.
//   - ErrIdentityUnreadable: identity.json exists but is unreadable, corrupt or empty.
//   - err != nil: an assignment is present but unverifiable or grants nothing. Fail loudly so a rejected
//     appliance is never mistaken for a new one.
func CentralAssignmentLoader(paths assignment.Paths, identityDir string, log *slog.Logger) func(context.Context) (Assignment, bool, error) {
	return func(context.Context) (Assignment, bool, error) {
		// Public identity only: pmsd signs nothing and cannot read the root-only private key.
		// A missing, corrupt and not-yet-enrolled identity are three different states; a corrupt file must never be
		// reported as factory-clean. LoadPublic returns (nil, nil) for absent and (nil, err) for unreadable.
		ident, err := (&identity.Store{Dir: identityDir}).LoadPublic()
		switch {
		case err != nil:
			if log != nil {
				log.Error("pmsd: refusing to start — the appliance identity is present but unreadable",
					"err", err.Error())
			}
			return Assignment{}, false, ErrIdentityUnreadable
		case ident == nil:
			return Assignment{}, false, nil
		case ident.ApplianceID == "" && ident.PublicKeyB64 == "":
			// A pre-enrolment identity always has the public key, so an empty one is a damaged file, not "not enrolled yet".
			if log != nil {
				log.Error("pmsd: refusing to start — the appliance identity carries no appliance id and no public key")
			}
			return Assignment{}, false, ErrIdentityUnreadable
		case ident.ApplianceID == "":
			// Enrolment not completed: not a fault.
			return Assignment{}, false, nil
		}

		r := assignment.Resolve(paths, assignment.ApplianceBinding{
			ApplianceID:  ident.ApplianceID,
			Serial:       ident.Serial,
			PublicKeyB64: ident.PublicKeyB64,
		}, time.Now(), log)

		// Decide on the Outcome, not on State: State is empty for both a factory-clean appliance and a document
		// that failed verification, which would make a rejected appliance look new.
		switch r.Outcome {
		case assignment.OutcomeAbsent:
			return Assignment{}, false, nil
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

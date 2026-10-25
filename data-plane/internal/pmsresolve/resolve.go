// Package pmsresolve decides one fail-closed outcome from the complete vector of per-interface verdicts
// (from the trusted guest network, never client hints). It never short-circuits; the caller gathers the
// whole vector first. Reason codes are internal; the guest sees a uniform non-success response.
package pmsresolve

type CandidateOutcome string

const (
	Verified            CandidateOutcome = "VERIFIED"             // this interface matched exactly one Stay
	AmbiguousLocal      CandidateOutcome = "AMBIGUOUS_LOCAL"      // this interface matched >1 Stay (needs discriminator)
	NoMatch             CandidateOutcome = "NO_MATCH"             // determinate: this interface has no such Stay
	Unavailable         CandidateOutcome = "UNAVAILABLE"          // interface down/timed out → indeterminate
	Stale               CandidateOutcome = "STALE"                // interface data too stale to trust → indeterminate
	UnsupportedEvidence CandidateOutcome = "UNSUPPORTED_EVIDENCE" // interface can't evaluate this evidence → indeterminate
)

type Candidate struct {
	InterfaceID string
	Outcome     CandidateOutcome
	// NotActive: the interface is explicitly part of the network's routing but is not in service (auth disabled, draining, decommissioned). It is never probed.
	NotActive bool
}

// MaxSharedCandidates is the supported size of a network's explicit PMS set (contract section 8: a bounded fan-out; the hard ceiling is 5 and needs a Product
// Owner decision to raise).
const MaxSharedCandidates = 3

type Resolution string

const (
	ResVerified      Resolution = "VERIFIED"      // exactly one VERIFIED, every other determinate NO_MATCH
	ResNoMatch       Resolution = "NO_MATCH"      // determinate: no interface matched
	ResAmbiguous     Resolution = "AMBIGUOUS"     // ≥2 VERIFIED or any AMBIGUOUS_LOCAL → discriminator escalation
	ResIndeterminate Resolution = "INDETERMINATE" // any UNAVAILABLE/STALE/UNSUPPORTED, unmapped, or over-cap → fail closed
)

// InterfaceID is set only for ResVerified; Reason is a bounded internal code.
type Outcome struct {
	Resolution  Resolution
	InterfaceID string
	Reason      string
}

// Resolve is strict with no fallback, even for a single candidate. maxCandidates bounds the vector (0 =
// unbounded). Precedence:
//  1. empty vector or over the cap → INDETERMINATE (nothing to trust);
//  2. ≥2 VERIFIED or any AMBIGUOUS_LOCAL → AMBIGUOUS (discriminator escalation);
//  3. any UNAVAILABLE/STALE/UNSUPPORTED_EVIDENCE → INDETERMINATE (exactly-one can't be proven, never guess);
//  4. exactly one VERIFIED → VERIFIED;
//  5. otherwise → NO_MATCH.
func Resolve(candidates []Candidate, maxCandidates int) Outcome {
	if len(candidates) == 0 {
		return Outcome{Resolution: ResIndeterminate, Reason: "UNMAPPED"}
	}
	if maxCandidates > 0 && len(candidates) > maxCandidates {
		return Outcome{Resolution: ResIndeterminate, Reason: "CANDIDATE_CAP_EXCEEDED"}
	}

	var verified []string
	var ambiguousLocal, indeterminate, noMatch, notActive int
	for _, c := range candidates {
		switch c.Outcome {
		case Verified:
			verified = append(verified, c.InterfaceID)
		case AmbiguousLocal:
			ambiguousLocal++
		case Unavailable, Stale, UnsupportedEvidence:
			indeterminate++
			if c.NotActive {
				notActive++
			}
		case NoMatch:
			noMatch++
		default:
			// An unknown verdict is never determinate.
			indeterminate++
		}
	}

	switch {
	case len(verified) >= 2 || ambiguousLocal > 0:
		return Outcome{Resolution: ResAmbiguous, Reason: "DISCRIMINATOR_REQUIRED"}
	case indeterminate > 0 && notActive == indeterminate:
		// Nothing was unreachable: the operator's routing names an interface that is out of service. A configuration fact, not a feed one.
		return Outcome{Resolution: ResIndeterminate, Reason: "CANDIDATE_NOT_ACTIVE"}
	case indeterminate > 0:
		return Outcome{Resolution: ResIndeterminate, Reason: "INCOMPLETE_EVIDENCE"}
	case len(verified) == 1:
		return Outcome{Resolution: ResVerified, InterfaceID: verified[0], Reason: "SINGLE_VERIFIED"}
	default:
		return Outcome{Resolution: ResNoMatch, Reason: "NO_INTERFACE_MATCH"}
	}
}

// GuestVisibleSuccess: only a single-verified resolution is visible; every non-success must be uniform and
// detail-free (no PMS, property, interface or failure detail reaches the guest).
func (o Outcome) GuestVisibleSuccess() bool { return o.Resolution == ResVerified }

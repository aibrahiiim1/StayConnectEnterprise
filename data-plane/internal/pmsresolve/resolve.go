// Package pmsresolve is the Increment-5 STRICT multi-PMS resolver decision core. Given the COMPLETE vector of
// per-interface candidate verdicts (derived from the TRUSTED guest network, never client hints), it decides a
// single authoritative outcome under strict, fail-closed rules. It NEVER short-circuits on the first or
// fastest candidate — the caller must gather the whole vector first. Reason codes are internal (audit/metrics)
// only; the guest sees a uniform non-success response.
package pmsresolve

// CandidateOutcome is one interface's determinate verdict for the guest evidence.
type CandidateOutcome string

const (
	Verified            CandidateOutcome = "VERIFIED"             // this interface matched exactly one Stay
	AmbiguousLocal      CandidateOutcome = "AMBIGUOUS_LOCAL"      // this interface matched >1 Stay (needs discriminator)
	NoMatch             CandidateOutcome = "NO_MATCH"             // determinate: this interface has no such Stay
	Unavailable         CandidateOutcome = "UNAVAILABLE"          // interface down/timed out → indeterminate
	Stale               CandidateOutcome = "STALE"                // interface data too stale to trust → indeterminate
	UnsupportedEvidence CandidateOutcome = "UNSUPPORTED_EVIDENCE" // interface can't evaluate this evidence → indeterminate
)

// Candidate is one interface's verdict in the vector.
type Candidate struct {
	InterfaceID string
	Outcome     CandidateOutcome
}

// Resolution is the final strict outcome.
type Resolution string

const (
	ResVerified      Resolution = "VERIFIED"      // exactly one VERIFIED, every other determinate NO_MATCH
	ResNoMatch       Resolution = "NO_MATCH"      // determinate: no interface matched
	ResAmbiguous     Resolution = "AMBIGUOUS"     // ≥2 VERIFIED or any AMBIGUOUS_LOCAL → discriminator escalation
	ResIndeterminate Resolution = "INDETERMINATE" // any UNAVAILABLE/STALE/UNSUPPORTED, unmapped, or over-cap → fail closed
)

// Outcome is the resolver result. InterfaceID is set ONLY for ResVerified. Reason is a bounded internal code.
type Outcome struct {
	Resolution  Resolution
	InterfaceID string
	Reason      string
}

// Resolve evaluates the COMPLETE candidate vector under STRICT rules (the only mode; no fallback, even for a
// single candidate). maxCandidates bounds the vector (0 = unbounded). Precedence:
//  1. unmapped (empty vector) → INDETERMINATE (fail closed) — there is nothing to trust.
//  2. over the candidate cap → INDETERMINATE (fail closed).
//  3. ≥2 VERIFIED or any AMBIGUOUS_LOCAL → AMBIGUOUS (discriminator escalation).
//  4. any UNAVAILABLE/STALE/UNSUPPORTED_EVIDENCE → INDETERMINATE (fail closed) — we cannot prove
//     exactly-one, so we never guess.
//  5. exactly one VERIFIED (all others therefore NO_MATCH) → VERIFIED (success).
//  6. otherwise (zero VERIFIED, all NO_MATCH) → NO_MATCH.
//
// A slow VERIFIED still beats a fast NO_MATCH because the decision runs only on the full vector — timing is
// the caller's concern, not this function's.
func Resolve(candidates []Candidate, maxCandidates int) Outcome {
	if len(candidates) == 0 {
		return Outcome{Resolution: ResIndeterminate, Reason: "UNMAPPED"}
	}
	if maxCandidates > 0 && len(candidates) > maxCandidates {
		return Outcome{Resolution: ResIndeterminate, Reason: "CANDIDATE_CAP_EXCEEDED"}
	}

	var verified []string
	var ambiguousLocal, indeterminate, noMatch int
	for _, c := range candidates {
		switch c.Outcome {
		case Verified:
			verified = append(verified, c.InterfaceID)
		case AmbiguousLocal:
			ambiguousLocal++
		case Unavailable, Stale, UnsupportedEvidence:
			indeterminate++
		case NoMatch:
			noMatch++
		default:
			// an unknown verdict is never treated as determinate → fail closed.
			indeterminate++
		}
	}

	switch {
	case len(verified) >= 2 || ambiguousLocal > 0:
		return Outcome{Resolution: ResAmbiguous, Reason: "DISCRIMINATOR_REQUIRED"}
	case indeterminate > 0:
		return Outcome{Resolution: ResIndeterminate, Reason: "INCOMPLETE_EVIDENCE"}
	case len(verified) == 1:
		return Outcome{Resolution: ResVerified, InterfaceID: verified[0], Reason: "SINGLE_VERIFIED"}
	default:
		return Outcome{Resolution: ResNoMatch, Reason: "NO_INTERFACE_MATCH"}
	}
}

// GuestVisible reports whether the outcome may reveal the matched interface to the guest. ONLY a successful
// single-verified resolution does; every non-success is a uniform, detail-free response (no PMS / property /
// interface / candidate / failure detail leaks to the guest — reason codes stay in audit/metrics).
func (o Outcome) GuestVisibleSuccess() bool { return o.Resolution == ResVerified }

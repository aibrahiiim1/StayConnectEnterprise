// Package signinattempt is the vocabulary of a guest room sign-in attempt: what happened, what the operator
// is told, and what the guest is told.
//
// IT EXISTS BECAUSE ONE WORD WAS DOING THREE JOBS. Every non-success on the room sign-in path collapsed to a
// single internal outcome and a single sentence, so "the room is not in the mirror", "the room is there and
// the name is wrong", "the stay checked out this morning" and "the database is down" were one fact as far as
// anything outside scd could see. An operator asked to help a guest at the desk had nothing to look at, and
// the guest was told to check details that were, in several of those cases, correct.
//
// THE SEPARATION THIS PACKAGE DRAWS. A Result is the exact, structured, operator-visible reason. A GuestClass
// is the coarse, deliberately lossy thing the guest is told. The mapping between them is the whole security
// argument of this feature and lives in one function, GuestClass(), so it can be read and tested as a unit
// rather than being spread across handlers.
package signinattempt

import "strings"

// Result is the durable outcome of ONE deliberate Connect submission. It is written to
// iam_v2.sign_in_attempts and shown to authorised operators; it is never sent to a guest.
type Result string

const (
	// Verified — the evidence identified exactly one eligible Stay. MatchedField says which accepted value
	// did it.
	Verified Result = "VERIFIED"
	// CredentialMismatch — the room IS in the local mirror with at least one eligible stay, and the value the
	// guest typed equals none of that stay's accepted values.
	CredentialMismatch Result = "CREDENTIAL_MISMATCH"
	// RoomNotInMirror — no stay at all carries that normalized room number on any interface mapped to this
	// guest network. Distinct from CredentialMismatch on purpose: "you are in the wrong room number" and
	// "your name is spelled differently from the PMS" are different problems for the person at the desk.
	RoomNotInMirror Result = "ROOM_NOT_IN_MIRROR"
	// StayNotEligible — a stay for that room exists but is outside the state or time window that may
	// authenticate (checked out, no occupancy evidence, pinned to a superseded revision).
	StayNotEligible Result = "STAY_NOT_ELIGIBLE"
	// AmbiguousRoomCandidates — the evidence matched more than one live Stay. Choosing between two guests who
	// share a room number is the decision this system must never make on its own.
	AmbiguousRoomCandidates Result = "AMBIGUOUS_ROOM_CANDIDATES"
	// MirrorStaleOrMissingChange — the local mirror cannot authorise ANYBODY right now: the feed has never
	// completed a sync, has an unresolved continuity gap, or is past its freshness ceiling. It is a property
	// of the site, not of this guest — which is exactly why it is safe to tell the guest it is technical.
	MirrorStaleOrMissingChange Result = "MIRROR_STALE_OR_MISSING_CHANGE"
	// RateLimited — the durable throttle refused this attempt before any evidence was evaluated.
	RateLimited Result = "RATE_LIMITED"
	// RoutingOrInterfaceFailure — the request could not be routed to a PMS interface at all: the source
	// address is on no mapped guest network, the network maps to no ACTIVE interface, or the interface has no
	// published revision.
	RoutingOrInterfaceFailure Result = "ROUTING_OR_INTERFACE_FAILURE"
	// ServiceUnavailable — an internal fault: a database error, a probe that could not answer, an offer
	// engine failure, a grant that committed but could not be enforced.
	ServiceUnavailable Result = "SERVICE_UNAVAILABLE"
	// SpentRequestID — the submission re-used a resolution request id that already carried a refusal. No
	// conforming portal does this; it is the signature of a stale client.
	SpentRequestID Result = "SPENT_REQUEST_ID"
	// MalformedSubmission — the body, the room, the evidence or the request id was not usable. Recorded so an
	// operator can see that a client is sending something the server cannot read, rather than seeing nothing.
	MalformedSubmission Result = "MALFORMED_SUBMISSION"
	// VerifiedNoEligiblePackage — the guest PROVED WHO THEY ARE and the site had nothing to offer them. Their
	// details were right; the sign-in could not be completed. See GuestClass for the one place this feature
	// accepts a narrow, documented distinguishability rather than telling a correct guest they are wrong.
	VerifiedNoEligiblePackage Result = "VERIFIED_NO_ELIGIBLE_PACKAGE"
)

// AllResults is every Result, in the order an operator filter should offer them. Tests use it to prove no
// Result is missing a label or a guest class.
var AllResults = []Result{
	Verified, CredentialMismatch, RoomNotInMirror, StayNotEligible, AmbiguousRoomCandidates,
	MirrorStaleOrMissingChange, RateLimited, RoutingOrInterfaceFailure, ServiceUnavailable,
	SpentRequestID, MalformedSubmission, VerifiedNoEligiblePackage,
}

// MatchedField names the accepted value that verified the guest, for a VERIFIED attempt.
type MatchedField string

const (
	MatchedNone              MatchedField = ""
	MatchedFirstName         MatchedField = "FIRST_NAME"
	MatchedFamilyName        MatchedField = "FAMILY_NAME"
	MatchedReservationNumber MatchedField = "RESERVATION_NUMBER"
)

// GuestClass is the COARSE answer the guest receives. There are only four, and the coarseness is the point.
type GuestClass string

const (
	GuestSuccess     GuestClass = "SUCCESS"
	GuestCredential  GuestClass = "CREDENTIAL"
	GuestTechnical   GuestClass = "TECHNICAL"
	GuestRateLimited GuestClass = "RATE_LIMITED"
)

// GuestClass maps an exact Result to the class of message the guest may be shown.
//
// THE RULE, AND WHY IT IS THIS RULE.
//
// The Product Owner asked that a guest be able to tell "your details are wrong" from "we cannot check right
// now", because telling someone to re-check details that are correct is useless and telling someone to
// contact Reception when they merely mistyped is worse. That is a real improvement and it is also the exact
// point at which a portal can become an occupancy oracle, so the split is drawn on one principle:
//
//	CREDENTIAL  — the answer depends on WHAT THE GUEST TYPED, or on what the mirror holds ABOUT THEIR ROOM.
//	TECHNICAL   — the answer is true for EVERY guest on this site at this moment, whatever they typed.
//
// Read the consequences, because they are not obvious and they are the security property:
//
//   - ROOM_NOT_IN_MIRROR, CREDENTIAL_MISMATCH, STAY_NOT_ELIGIBLE and AMBIGUOUS_ROOM_CANDIDATES all return
//     CREDENTIAL. They must be indistinguishable. If "no such room" answered differently from "wrong name",
//     anyone in the lobby could enumerate which rooms are occupied by submitting room numbers; if
//     "checked out this morning" answered differently again, they could enumerate departures too.
//   - MIRROR_STALE_OR_MISSING_CHANGE returns TECHNICAL, and it is decided from FEED state BEFORE any room is
//     looked up. That ordering is load-bearing. A "the mirror is stale" answer produced only when a room was
//     NOT found would leak exactly the fact the previous bullet protects.
//   - SPENT_REQUEST_ID returns TECHNICAL: it is a fault in the client, not in what the guest typed, and
//     telling them to re-check a correct surname would be a lie.
//   - VERIFIED_NO_ELIGIBLE_PACKAGE returns TECHNICAL, and it is the ONE place this mapping accepts a narrow
//     distinguishability rather than a clean rule. The guest's details were RIGHT; the site simply had
//     nothing to offer that stay. Answering CREDENTIAL would tell someone who typed their own name
//     correctly that they got it wrong, which is exactly the uselessness this feature exists to remove.
//     What it costs: on a site where some eligible stays are offered nothing, a correct room+name pair
//     answers differently from an incorrect one. That is a real, if narrow, confirmation signal, it only
//     exists while a property's package configuration is incomplete, and it is recorded here rather than
//     discovered later.
//
// What a guest can still learn is site-wide and already public to anyone in the lobby: whether this
// property's sign-in is working at all. What they cannot learn is anything about a particular room.
func (r Result) GuestClass() GuestClass {
	switch r {
	case Verified:
		return GuestSuccess
	case RateLimited:
		return GuestRateLimited
	case CredentialMismatch, RoomNotInMirror, StayNotEligible, AmbiguousRoomCandidates, MalformedSubmission:
		return GuestCredential
	case MirrorStaleOrMissingChange, RoutingOrInterfaceFailure, ServiceUnavailable, SpentRequestID,
		VerifiedNoEligiblePackage:
		return GuestTechnical
	default:
		// An unrecognised Result is treated as a system condition rather than as the guest's fault. Telling
		// someone their details are wrong on the strength of a code we do not recognise is the one answer that
		// is certainly unjustified.
		return GuestTechnical
	}
}

// Succeeded reports whether the attempt admitted the guest.
func (r Result) Succeeded() bool { return r == Verified }

// labels are the SHORT PLAIN-LANGUAGE reasons an operator reads in the attempts list.
//
// They live here, in Go, beside the codes rather than in the browser, so there is one list. A second list in
// TypeScript would be a second thing to remember to update, and the failure mode of forgetting is a screen
// that shows a raw enum to someone helping a guest at the desk.
var labels = map[Result]string{
	Verified:                   "Verified",
	CredentialMismatch:         "Room found, submitted value did not match",
	RoomNotInMirror:            "Room not in the local mirror",
	StayNotEligible:            "Stay is not currently eligible",
	AmbiguousRoomCandidates:    "More than one stay matched",
	MirrorStaleOrMissingChange: "Mirror cannot authorise (stale or incomplete)",
	RateLimited:                "Rate limited",
	RoutingOrInterfaceFailure:  "Network or PMS interface routing failure",
	ServiceUnavailable:         "Internal service failure",
	SpentRequestID:             "Request ID already used by a refused attempt",
	MalformedSubmission:        "Submission could not be read",
	VerifiedNoEligiblePackage:  "Verified, but no package is available to this stay",
}

// Label returns the operator-facing plain-language reason. An unknown code returns itself rather than an
// empty string: a screen showing a raw code is ugly, a screen showing nothing is misleading.
func (r Result) Label() string {
	if s, ok := labels[r]; ok {
		return s
	}
	return string(r)
}

// VerifierKind is a DIAGNOSTIC classification of the single value the guest typed under the combined
// room_any mode.
//
// IT IS A LABEL ON THE RECORD AND NOTHING ELSE. It must never be used to decide which field to compare
// against: that is precisely the legacy "either" behaviour that submitted a surname containing a digit as a
// reservation number and locked out the guest who owned that name. The server compares the typed value
// against every accepted field regardless of what this function says, and this exists so an operator reading
// the attempts list can see at a glance what kind of thing the guest believed they were typing.
type VerifierKind string

const (
	VerifierFullName        VerifierKind = "FULL_NAME"
	VerifierReservationLike VerifierKind = "RESERVATION_NUMBER_LIKE"
	VerifierUnknown         VerifierKind = "UNKNOWN"
)

// ClassifyVerifier labels the submitted value. Empty is UNKNOWN, not a name.
func ClassifyVerifier(s string) VerifierKind {
	s = strings.TrimSpace(s)
	if s == "" {
		return VerifierUnknown
	}
	var letters, digits, other int
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == ' ' || r == '-' || r == '\'' || r == '.':
			// separators that occur in both a written name and a reservation identifier
		case isLetter(r):
			letters++
		default:
			other++
		}
	}
	if other > 0 {
		return VerifierUnknown
	}
	if digits > 0 {
		// Anything carrying a digit reads as an identifier. A surname containing a digit would be labelled
		// this way too, which is harmless HERE precisely because the label decides nothing.
		return VerifierReservationLike
	}
	if letters > 0 {
		return VerifierFullName
	}
	return VerifierUnknown
}

// isLetter accepts any Unicode letter, so a non-Latin name is classified as a name rather than as "other".
func isLetter(r rune) bool {
	return r == 'ß' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 0x7f
}

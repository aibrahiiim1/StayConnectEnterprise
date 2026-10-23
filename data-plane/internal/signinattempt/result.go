// Package signinattempt is the vocabulary of a guest room sign-in attempt. A Result is the exact reason
// shown to operators; a GuestClass is the coarse, deliberately lossy thing the guest is told. The mapping
// is the security argument of the feature and lives in one function, GuestClass().
package signinattempt

import "strings"

// Result is the durable outcome of one Connect submission, stored in iam_v2.sign_in_attempts and shown to
// operators; it is never sent to a guest.
type Result string

const (
	// Verified: exactly one eligible Stay was identified.
	Verified Result = "VERIFIED"
	// CredentialMismatch: the room is in the mirror with an eligible stay but the typed value matched none
	// of its accepted values.
	CredentialMismatch Result = "CREDENTIAL_MISMATCH"
	// RoomNotInMirror: no stay carries that room on any interface mapped to this guest network. Kept apart
	// from CredentialMismatch because the desk fixes them differently.
	RoomNotInMirror Result = "ROOM_NOT_IN_MIRROR"
	// StayNotEligible: a stay exists but is outside the state or time window that may authenticate.
	StayNotEligible Result = "STAY_NOT_ELIGIBLE"
	// AmbiguousRoomCandidates: more than one live Stay matched; the system must never choose between guests.
	AmbiguousRoomCandidates Result = "AMBIGUOUS_ROOM_CANDIDATES"
	// MirrorStaleOrMissingChange: the mirror cannot authorise anybody (never synced, continuity gap, or past
	// its freshness ceiling). A property of the site, not of this guest, so it is safe to call technical.
	MirrorStaleOrMissingChange Result = "MIRROR_STALE_OR_MISSING_CHANGE"
	// RateLimited: the durable throttle refused the attempt before any evidence was evaluated.
	RateLimited Result = "RATE_LIMITED"
	// RoutingOrInterfaceFailure: the request could not be routed to a PMS interface.
	RoutingOrInterfaceFailure Result = "ROUTING_OR_INTERFACE_FAILURE"
	// ServiceUnavailable: an internal fault.
	ServiceUnavailable Result = "SERVICE_UNAVAILABLE"
	// SpentRequestID: a resolution request id that already carried a refusal was re-used (a stale client).
	SpentRequestID Result = "SPENT_REQUEST_ID"
	// MalformedSubmission: the body, room, evidence or request id was unusable.
	MalformedSubmission Result = "MALFORMED_SUBMISSION"
	// VerifiedNoEligiblePackage: the guest proved who they are but the site had nothing to offer. See
	// GuestClass for the one place a narrow distinguishability is accepted.
	VerifiedNoEligiblePackage Result = "VERIFIED_NO_ELIGIBLE_PACKAGE"
	// LicenseRefused: the licence does not permit a new guest now; decided before anything about the guest
	// is looked at, as for every other sign-in method.
	LicenseRefused Result = "LICENSE_REFUSED"
	// LicenseCapacityReached: the guest is verified but admitting the device would exceed the licensed
	// concurrent-guest cap.
	LicenseCapacityReached Result = "LICENSE_CAPACITY_REACHED"
)

// AllResults is every Result in operator-filter order; tests use it to prove none lacks a label or class.
var AllResults = []Result{
	Verified, CredentialMismatch, RoomNotInMirror, StayNotEligible, AmbiguousRoomCandidates,
	MirrorStaleOrMissingChange, RateLimited, RoutingOrInterfaceFailure, ServiceUnavailable,
	SpentRequestID, MalformedSubmission, VerifiedNoEligiblePackage, LicenseRefused, LicenseCapacityReached,
}

type MatchedField string

const (
	MatchedNone              MatchedField = ""
	MatchedFirstName         MatchedField = "FIRST_NAME"
	MatchedFamilyName        MatchedField = "FAMILY_NAME"
	MatchedReservationNumber MatchedField = "RESERVATION_NUMBER"
)

// GuestClass is the coarse answer the guest receives; the coarseness is the point.
type GuestClass string

const (
	GuestSuccess     GuestClass = "SUCCESS"
	GuestCredential  GuestClass = "CREDENTIAL"
	GuestTechnical   GuestClass = "TECHNICAL"
	GuestRateLimited GuestClass = "RATE_LIMITED"
	// GuestLicense and GuestCapacity are appliance-wide facts, true for every guest whatever they typed, so
	// disclosing them reveals nothing about a room.
	GuestLicense  GuestClass = "LICENSE"
	GuestCapacity GuestClass = "CAPACITY"
)

// GuestClass maps a Result to the message class the guest may see. The split avoids an occupancy oracle:
// CREDENTIAL when the answer depends on what the guest typed or what the mirror holds about their room,
// TECHNICAL when it is true for every guest on the site at this moment.
//
//   - ROOM_NOT_IN_MIRROR, CREDENTIAL_MISMATCH, STAY_NOT_ELIGIBLE and AMBIGUOUS_ROOM_CANDIDATES must be
//     indistinguishable, or anyone in the lobby could enumerate occupied rooms and departures.
//   - MIRROR_STALE_OR_MISSING_CHANGE is TECHNICAL and decided from feed state BEFORE any room lookup;
//     deciding it only when a room is not found would leak what the previous bullet protects.
//   - SPENT_REQUEST_ID is a client fault, so TECHNICAL.
//   - VERIFIED_NO_ELIGIBLE_PACKAGE is TECHNICAL: the one accepted narrow distinguishability. Calling a
//     correct guest wrong would be useless, at the cost that while a property's package configuration is
//     incomplete a correct room+name answers differently from an incorrect one.
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
	case LicenseRefused:
		return GuestLicense
	case LicenseCapacityReached:
		return GuestCapacity
	default:
		// An unrecognised Result is never blamed on the guest.
		return GuestTechnical
	}
}

func (r Result) Succeeded() bool { return r == Verified }

// labels are the plain-language reasons operators read; kept here beside the codes so there is one list.
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
	LicenseRefused:             "The appliance licence does not permit new guests",
	LicenseCapacityReached:     "Verified, but the licensed guest capacity is full",
}

// Label returns the code itself for an unknown Result rather than an empty string.
func (r Result) Label() string {
	if s, ok := labels[r]; ok {
		return s
	}
	return string(r)
}

// VerifierKind is a diagnostic label only. It must never decide which field to compare against: the old
// "either" behaviour treated a surname containing a digit as a reservation number and locked out its owner.
// The typed value is always compared against every accepted field.
type VerifierKind string

const (
	VerifierFullName        VerifierKind = "FULL_NAME"
	VerifierReservationLike VerifierKind = "RESERVATION_NUMBER_LIKE"
	VerifierUnknown         VerifierKind = "UNKNOWN"
)

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
			// separators common to names and reservation identifiers
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
		return VerifierReservationLike
	}
	if letters > 0 {
		return VerifierFullName
	}
	return VerifierUnknown
}

// isLetter accepts any Unicode letter so a non-Latin name is classified as a name.
func isLetter(r rune) bool {
	return r == 'ß' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 0x7f
}

// CountsAsCredentialFailure is true only for CREDENTIAL_MISMATCH (a wrong guess) and ROOM_NOT_IN_MIRROR
// (walking room numbers is the other half of enumeration). Everything else must not count, because it is
// not the guest's mistake:
//
//   - STAY_NOT_ELIGIBLE (checked out) and AMBIGUOUS_ROOM_CANDIDATES (the value matched twice) are property data.
//   - MALFORMED_SUBMISSION carries no guess; counting it would let a broken portal restrict every guest.
//   - Technical failures (stale mirror, routing, service, spent request id, no package) must never be a wrong
//     credential, or an outage would restrict every guest within five taps.
//   - RATE_LIMITED must never feed the policy that refused it, or a short restriction becomes permanent.
//   - VERIFIED clears the counter.
func (r Result) CountsAsCredentialFailure() bool {
	switch r {
	case CredentialMismatch, RoomNotInMirror:
		return true
	default:
		return false
	}
}

// CountingResults is the same set as data; the database predicate must name the same codes.
var CountingResults = []Result{CredentialMismatch, RoomNotInMirror}

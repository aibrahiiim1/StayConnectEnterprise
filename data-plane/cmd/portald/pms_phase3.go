package main

// Phase 3 (DARK) guest-portal PMS authentication response contract.
//
// The single rule this file exists to enforce: a guest learns ONLY whether they are in. Every non-success —
// no match, ambiguous, indeterminate, an interface being down, a stale cache, a throttled attempt — produces
// the SAME response with the SAME shape and the SAME message. Anything else turns the portal into an oracle:
// a distinguishable "that room exists but the name is wrong" answer lets an attacker enumerate rooms, guests
// and even which PMS a property runs. The internal reason code is carried separately for audit/metrics and is
// never written into the guest-facing body.

import (
	"encoding/json"
	"net/http"
	"strings"
)

// guestAuthMessage is the ONE message every unsuccessful PMS authentication returns, whatever the cause.
const guestAuthMessage = "We could not verify your stay. Please check your details or contact reception."

// pmsOutcome is the resolver's internal outcome as portald receives it from scd.
type pmsOutcome string

const (
	outcomeVerified      pmsOutcome = "VERIFIED"
	outcomeNoMatch       pmsOutcome = "NO_MATCH"
	outcomeAmbiguous     pmsOutcome = "AMBIGUOUS"
	outcomeIndeterminate pmsOutcome = "INDETERMINATE"
	outcomeThrottled     pmsOutcome = "THROTTLED"
)

// guestPMSResponse is the guest-facing body. On success it carries the session the guest just obtained; on
// ANY failure it carries exactly the uniform message and nothing else — no outcome, no interface, no
// property, no candidate count, no reason code, no hint that a room or name was partially right.
type guestPMSResponse struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	RedirectTo string `json:"redirect_to,omitempty"`
}

// auditFields are what the SERVER records about the attempt. They never reach the guest.
type pmsAuditFields struct {
	Outcome    pmsOutcome
	ReasonCode string
}

// buildGuestPMSResponse maps an internal outcome to the guest-facing body and the audit fields. It is a pure
// function so the uniformity property can be tested exhaustively rather than argued about.
func buildGuestPMSResponse(outcome pmsOutcome, reasonCode, sessionID, redirectTo string) (int, guestPMSResponse, pmsAuditFields) {
	audit := pmsAuditFields{Outcome: outcome, ReasonCode: reasonCode}
	if outcome == outcomeVerified && sessionID != "" {
		return http.StatusOK, guestPMSResponse{OK: true, SessionID: sessionID, RedirectTo: redirectTo}, audit
	}
	// EVERY other case — including a VERIFIED outcome that somehow carries no session, which is a server
	// problem the guest must not be told about — is the identical uniform failure.
	return http.StatusOK, guestPMSResponse{OK: false, Message: guestAuthMessage}, audit
}

// writeGuestPMSResponse writes the body. The status code is deliberately 200 for a failed verification too:
// a distinct status is itself a signal, and network observers or scripted probes should not be able to
// separate "wrong details" from "PMS unreachable" by status alone.
func writeGuestPMSResponse(w http.ResponseWriter, status int, body guestPMSResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// leaksDetail reports whether a guest-facing body would disclose anything beyond the uniform message. It is
// used by the tests as an explicit contract, and by callers as a cheap last-line assertion.
func leaksDetail(body guestPMSResponse) bool {
	if body.OK {
		return false // a successful verification legitimately names the guest's own session
	}
	if body.SessionID != "" || body.RedirectTo != "" {
		return true
	}
	if body.Message != guestAuthMessage {
		return true
	}
	return false
}

// forbiddenGuestTerms are words that must never appear in a guest-facing failure body. They name the things
// an attacker would learn from a leaky message: the vendor, the mechanics, or how close the guess was.
var forbiddenGuestTerms = []string{
	"pms", "protel", "opera", "fias", "interface", "candidate", "ambiguous", "indeterminate",
	"stale", "unavailable", "no_match", "throttle", "reservation id", "folio", "room exists",
}

// bodyMentionsForbiddenTerm is the test-facing check that the uniform message itself stays clean.
func bodyMentionsForbiddenTerm(body guestPMSResponse) string {
	hay := strings.ToLower(body.Message)
	for _, t := range forbiddenGuestTerms {
		if strings.Contains(hay, t) {
			return t
		}
	}
	return ""
}

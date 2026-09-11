package main

// Phase 3 guest-portal PMS authentication response contract.
//
// THE RULE, AS IT NOW STANDS. A guest learns whether they are in, and — on a failure — which of THREE things
// to do about it: check what they typed, try again later, or wait out a rate limit. Nothing finer. Every
// distinction that could describe a particular ROOM still collapses to one answer: "no such room", "wrong
// name", "that stay checked out" and "two guests matched" are the same message, because anything else lets
// someone in the lobby submit room numbers and read off which are occupied.
//
// IT USED TO BE ONE MESSAGE FOR EVERYTHING, and that was wrong in both directions. A guest whose details were
// perfectly correct, on a property whose PMS mirror could not answer for anybody, was told to check their
// details and contact reception — advice that could not possibly help. A guest who had merely mistyped was
// told the same thing, and went to the desk. The Product Owner asked for the distinction; the mapping that
// keeps it safe lives in internal/signinattempt.GuestClass, not here, and this file only turns a class into
// words.
//
// The exact internal reason never reaches the guest. It is recorded in iam_v2.sign_in_attempts, where an
// authorised operator can read it beside what the guest actually typed.

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The three guest-facing failure messages. There are exactly three, they are the only ones this daemon may
// send, and guestAuthMessages below is what keeps that true.
const (
	// guestAuthMessage is the CREDENTIAL answer: something about what was typed did not match. It names the
	// three things a guest may enter, and it says "full" first name deliberately — a property whose PMS holds
	// a complete given name in one field accepts that whole value and not one word of it, and a guest who
	// types a single word otherwise has no way to know why it failed.
	guestAuthMessage = "The room number or guest detail you entered is incorrect. " +
		"Check the room number and enter the full first name, family name, or reservation number."
	// guestAuthTechnicalMessage is the TECHNICAL answer: the property could not check, whatever was typed. It
	// must never be shown for an ordinary wrong value — sending someone to Reception because they mistyped
	// their own surname is how a desk fills up with people who could have fixed it themselves.
	guestAuthTechnicalMessage = "We are unable to verify your stay right now. " +
		"Please try again or contact Reception."
	// guestAuthRateLimitedMessage is kept separate from both. Folding it into the technical message would
	// send a throttled attacker to Reception; folding it into the credential message would tell them their
	// guesses are still being evaluated.
	guestAuthRateLimitedMessage = "Too many sign-in attempts from this device. " +
		"Please wait a few minutes and try again."
	// guestPostStayMessage is the POST-STAY answer, and it is the wording this whole file used to carry for
	// everything. It is kept, unchanged, because post-stay is a different credential method: a departed guest
	// returning with a PIN has no room number, no family name and no reservation number to re-check, so the
	// room sign-in sentence above would be actively misleading advice. This delivery changed the ROOM sign-in
	// wording and deliberately left every other method's exactly as it was.
	guestPostStayMessage = "We could not verify your stay. Please check your details or contact reception."
)

// guestAuthMessages is the CLOSED SET. A test walks it to prove that every sentence a guest can receive
// discloses nothing about the property, and callers resolve through messageForClass rather than writing a
// string at a call site — a fourth sentence added somewhere else is the drift this exists to prevent.
var guestAuthMessages = map[string]string{
	"CREDENTIAL":   guestAuthMessage,
	"TECHNICAL":    guestAuthTechnicalMessage,
	"RATE_LIMITED": guestAuthRateLimitedMessage,
	"POST_STAY":    guestPostStayMessage,
}

// messageForClass turns scd's coarse failure class into the guest's sentence.
//
// AN UNKNOWN OR ABSENT CLASS FALLS TO THE CREDENTIAL MESSAGE, which is the safe default rather than the
// obvious one. Defaulting to "we cannot check right now" would hand an attacker a way to make every answer
// look technical, and would send a guest with a genuine typo to queue at the desk. The credential message is
// also the one that is never harmful to show: re-reading what you typed costs nothing on a technical
// failure, while contacting Reception about a typo costs a guest their evening.
func messageForClass(class string) string {
	if m, ok := guestAuthMessages[class]; ok {
		return m
	}
	return guestAuthMessage
}

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
// function so the disclosure property can be tested exhaustively rather than argued about.
//
// failureClass is scd's coarse class, carried through verbatim. This function deliberately does not compute
// it: exactly one mapping from an exact reason to a guest class exists, it lives in internal/signinattempt,
// and a second one here would be a second thing to keep correct.
func buildGuestPMSResponse(outcome pmsOutcome, reasonCode, failureClass, sessionID, redirectTo string) (int, guestPMSResponse, pmsAuditFields) {
	audit := pmsAuditFields{Outcome: outcome, ReasonCode: reasonCode}
	if outcome == outcomeVerified && sessionID != "" {
		return http.StatusOK, guestPMSResponse{OK: true, SessionID: sessionID, RedirectTo: redirectTo}, audit
	}
	// EVERY other case — including a VERIFIED outcome that somehow carries no session, which is a server
	// problem the guest must not be told about — is a failure carrying one of the three sentences.
	return http.StatusOK, guestPMSResponse{OK: false, Message: messageForClass(failureClass)}, audit
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
	// The message must be one of the three. Anything else is a sentence somebody wrote at a call site, which
	// is exactly how a message that names a room or a PMS eventually ships.
	for _, m := range guestAuthMessages {
		if body.Message == m {
			return false
		}
	}
	return true
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

package main

// Guest-facing PMS sign-in failures carry one of a few coarse classes (check what you typed, try later,
// wait out a rate limit). Anything describing a particular room ("no such room", "wrong name", "checked out",
// "two matched") collapses to one answer, or a lobby visitor could read off occupied rooms. The class->reason
// mapping lives in internal/signinattempt.GuestClass; this file only turns a class into words. The exact
// reason is recorded in iam_v2.sign_in_attempts, never sent to the guest.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// guestAuthMessages (below) is the closed set of sentences this daemon may send.
const (
	// CREDENTIAL: what was typed did not match. "full" first name is deliberate: a PMS holding a complete
	// given name in one field accepts the whole value, not one word of it.
	guestAuthMessage = "The room number or guest detail you entered is incorrect. " +
		"Check the room number and enter the full first name, family name, or reservation number."
	// TECHNICAL: the property could not check. Never shown for an ordinary wrong value, which would send
	// mistyped guests to Reception.
	guestAuthTechnicalMessage = "We are unable to verify your stay right now. " +
		"Please try again or contact the site team for assistance."
	// RESTRICTED when the server sent no remaining time; this daemon never prints a number it guessed.
	guestAuthRateLimitedUnknownMessage = "Too many attempts. Please wait and try again."
	// POST-STAY: a departed guest with a PIN has no room or name to re-check, so the room sentence would mislead.
	guestPostStayMessage = "We could not verify your stay. Please check your details or contact the site team for assistance."
)

const guestRoomDeviceLimitMessage = "Every device place for this room is in use. On a device that is online with it, remove one under “Your devices”, or contact the site team."

// The same sentences the voucher and account sign-in show (main.go, iamv2_commerce_session.go).
const (
	guestCapacityMessage       = "The Wi-Fi network is at capacity. Please try again shortly."
	guestLicenseRefusedMessage = "This sign-in method is not available. Please contact the site team for assistance."
)

// isLicenceRefusal: scd refused because of the appliance's licence (cmd/scd licenseRefusal), not the guest.
func isLicenceRefusal(code string) bool {
	switch code {
	case "unlicensed", "license_expired", "feature_not_licensed", "tenant_transition_pending", "removed_from_central":
		return true
	}
	return false
}

var guestAuthMessages = map[string]string{
	"CREDENTIAL": guestAuthMessage,
	"TECHNICAL":  guestAuthTechnicalMessage,
	"POST_STAY":  guestPostStayMessage,
	// DEVICE_LIMIT: access is fine but there is no free device slot.
	"DEVICE_LIMIT": guestRoomDeviceLimitMessage,
	// LICENSE and CAPACITY are property-wide facts, never about a room.
	"LICENSE":  guestLicenseRefusedMessage,
	"CAPACITY": guestCapacityMessage,
	// RATE_LIMITED is absent on purpose: its sentence carries a number, so guestAuthRateLimitedMessage produces it.
}

// guestAuthRateLimitedMessage stays separate from the other answers: folding it into TECHNICAL would send a
// restricted attacker to Reception, into CREDENTIAL would imply guesses are still evaluated. The number is
// the server's (from the restriction row), so the countdown matches when the gate actually opens.
func guestAuthRateLimitedMessage(remainingSeconds int) string {
	if remainingSeconds < 1 {
		return guestAuthRateLimitedUnknownMessage
	}
	return fmt.Sprintf("Too many attempts. Please wait %d seconds and try again.", remainingSeconds)
}

// An unknown or absent class falls to the credential message: defaulting to "cannot check right now" would
// let an attacker make every answer look technical and send typo-ers to the desk.
func messageForClass(class string, remainingSeconds int) string {
	if class == "RATE_LIMITED" {
		return guestAuthRateLimitedMessage(remainingSeconds)
	}
	if m, ok := guestAuthMessages[class]; ok {
		return m
	}
	return guestAuthMessage
}

type pmsOutcome string

const (
	outcomeVerified      pmsOutcome = "VERIFIED"
	outcomeNoMatch       pmsOutcome = "NO_MATCH"
	outcomeAmbiguous     pmsOutcome = "AMBIGUOUS"
	outcomeIndeterminate pmsOutcome = "INDETERMINATE"
	outcomeThrottled     pmsOutcome = "THROTTLED"
)

// On any failure the body carries only the uniform message: no outcome, interface, candidate count, reason
// code or hint that a room or name was partially right.
type guestPMSResponse struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	RedirectTo string `json:"redirect_to,omitempty"`
	// RetryAfterSeconds is the server's remaining time, on a rate-limited refusal only. The page counts it
	// down but never decides the wait is over; it re-asks.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}

// pmsAuditFields are recorded server-side and never reach the guest.
type pmsAuditFields struct {
	Outcome    pmsOutcome
	ReasonCode string
}

// buildGuestPMSResponse is pure so disclosure can be tested exhaustively. failureClass is scd's class carried
// verbatim; it is deliberately not computed here (the one mapping lives in internal/signinattempt).
func buildGuestPMSResponse(outcome pmsOutcome, reasonCode, failureClass, sessionID, redirectTo string,
	retryAfterSeconds int) (int, guestPMSResponse, pmsAuditFields) {
	audit := pmsAuditFields{Outcome: outcome, ReasonCode: reasonCode}
	if outcome == outcomeVerified && sessionID != "" {
		return http.StatusOK, guestPMSResponse{OK: true, SessionID: sessionID, RedirectTo: redirectTo}, audit
	}
	// A retry hint on any other answer would tell a guesser how long to wait between guesses.
	if failureClass != "RATE_LIMITED" {
		retryAfterSeconds = 0
	}
	// Every other case, including VERIFIED with no session (a server problem), is one permitted sentence.
	return http.StatusOK, guestPMSResponse{
		OK:                false,
		Message:           messageForClass(failureClass, retryAfterSeconds),
		RetryAfterSeconds: retryAfterSeconds,
	}, audit
}

// The status is 200 for a failed verification too: a distinct status would let probes tell "wrong details"
// from "PMS unreachable".
func writeGuestPMSResponse(w http.ResponseWriter, status int, body guestPMSResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// leaksDetail reports whether a body discloses anything beyond the uniform message.
func leaksDetail(body guestPMSResponse) bool {
	if body.OK {
		return false
	}
	if body.SessionID != "" || body.RedirectTo != "" {
		return true
	}
	// The rate-limited sentence is regenerated from the body's own number, which also catches a printed wait
	// that disagrees with the machine-readable one.
	if body.Message == guestAuthRateLimitedMessage(body.RetryAfterSeconds) {
		return false
	}
	for _, m := range guestAuthMessages {
		if body.Message == m {
			return false
		}
	}
	return true
}

// forbiddenGuestTerms must never appear in a guest-facing failure body.
var forbiddenGuestTerms = []string{
	"pms", "protel", "opera", "fias", "interface", "candidate", "ambiguous", "indeterminate",
	"stale", "unavailable", "no_match", "throttle", "reservation id", "folio", "room exists",
}

func bodyMentionsForbiddenTerm(body guestPMSResponse) string {
	hay := strings.ToLower(body.Message)
	for _, t := range forbiddenGuestTerms {
		if strings.Contains(hay, t) {
			return t
		}
	}
	return ""
}

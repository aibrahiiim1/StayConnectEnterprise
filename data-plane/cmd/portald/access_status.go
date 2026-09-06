package main

// TELLING A GUEST THEIR PACKAGE RAN OUT, INSTEAD OF SHOWING THEM A LOGIN PAGE AND LETTING THEM GUESS.
//
// When an Entitlement ends for DATA or TIME the enforcement plane drops the device's authorization, and the
// standing captive rules put it back in front of this portal automatically. The mechanism was already right.
// What was missing is the sentence: the guest arrives at the same sign-in page they saw on day one, so a
// package that ended is indistinguishable from Wi-Fi that broke.
//
// The portal asks scd one question — did this device's most recent access end, and was it data or time — and
// says so above the ordinary sign-in form. The form itself is untouched: after the notice the guest carries
// straight on into the normal login and package selection.
//
// SCOPE. This is presentation only. No accounting, termination, teardown, nft/tc or crossing logic is
// involved, and nothing here can grant, extend or refuse access.

import (
	"encoding/json"
	"net/http"
)

// accessStatusOut is what the page JavaScript receives. One bounded field, and no identity of any kind: not
// the guest, the room, the reservation, the stay, the entitlement or how much was used.
type accessStatusOut struct {
	// EndedReason is "DATA", "TIME" or absent.
	EndedReason string `json:"ended_reason,omitempty"`
	// Message is the guest-facing sentence for that reason, chosen server-side so the wording lives in one
	// place rather than being reassembled in the browser.
	Message string `json:"message,omitempty"`
}

// endedMessages is the complete guest-facing vocabulary of this surface. Two reasons, two sentences, and no
// branch that can produce anything else.
var endedMessages = map[string]string{
	"DATA": "Your Internet package has ended because the data allowance was used.",
	"TIME": "Your Internet package has ended because the access time expired.",
}

// accessStatus serves POST /access/status.
func (h *handler) accessStatus(w http.ResponseWriter, r *http.Request) {
	b := h.newPhase3Budget(r)
	defer b.cancel()

	// The device comes from the appliance's own neighbour table, exactly as every other guest-facing route
	// derives it. A guest cannot describe a device they are not.
	device, ok := h.originDevice(w, r, b, h.accessStatusQuiet)
	if !ok {
		return
	}
	body, _ := json.Marshal(map[string]any{"device": device})
	var out struct {
		EndedReason string `json:"ended_reason"`
	}
	if !h.scdPhase3Call(b, "http://unix/v1/phase3/access/status", body, &out) {
		// A status lookup that cannot complete is not an error the guest can act on: the page still renders
		// the ordinary sign-in form, which is what it did before this existed.
		h.accessStatusQuiet(w, r, b, "status_unavailable")
		return
	}
	msg, known := endedMessages[out.EndedReason]
	if !known {
		h.accessStatusQuiet(w, r, b, "no_recent_expiry")
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, accessStatusOut{EndedReason: out.EndedReason, Message: msg})
}

// accessStatusQuiet is this route's "nothing to say". It is deliberately a SUCCESS with an empty body rather
// than a failure: there is no notice to show, which is the ordinary case for almost every page load, and
// rendering an error for it would be wrong on the first connection of every guest's stay.
func (h *handler) accessStatusQuiet(w http.ResponseWriter, r *http.Request, b *phase3Budget, _ string) {
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, accessStatusOut{})
}

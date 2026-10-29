package main

// Tells a guest whose package ran out (DATA or TIME) why they are back at sign-in, above the unchanged form.
// Presentation only: nothing here can grant, extend or refuse access.

import (
	"encoding/json"
	"net/http"
)

// accessStatusOut carries one bounded field and no identity: not guest, room, reservation, stay, entitlement or usage.
type accessStatusOut struct {
	EndedReason string `json:"ended_reason,omitempty"`
	// Message is chosen server-side so the wording lives in one place.
	Message string `json:"message,omitempty"`
}

// endedMessages is the complete guest-facing vocabulary; no branch can produce anything else.
var endedMessages = map[string]string{
	"DATA": "Your Internet package has ended because the data allowance was used.",
	"TIME": "Your Internet package has ended because the access time expired.",
}

func (h *handler) accessStatus(w http.ResponseWriter, r *http.Request) {
	b := h.newPhase3Budget(r)
	defer b.cancel()

	// A device that was just told there was nothing to say is not asked about again for a few seconds (AD-57).
	quietKey := clientIPString(r)
	if h.accessQuiet.quiet(quietKey) {
		h.accessStatusQuiet(w, r, b, "recently_quiet")
		return
	}

	// The device comes from the appliance's neighbour table; a guest cannot describe a device they are not.
	device, ok := h.originDevice(w, r, b, h.accessStatusQuiet)
	if !ok {
		return
	}
	body, _ := json.Marshal(map[string]any{"device": device})
	var out struct {
		EndedReason string `json:"ended_reason"`
	}
	if !h.scdPhase3Call(b, "http://unix/v1/phase3/access/status", body, &out) {
		// A lookup that cannot complete is not actionable for the guest; the ordinary sign-in form still renders.
		h.accessStatusQuiet(w, r, b, "status_unavailable")
		return
	}
	msg, known := endedMessages[out.EndedReason]
	if !known {
		h.accessQuiet.remember(quietKey)
		h.accessStatusQuiet(w, r, b, "no_recent_expiry")
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, accessStatusOut{EndedReason: out.EndedReason, Message: msg})
}

// accessStatusQuiet is a success with an empty body, not an error: no notice is the normal case on every first page load.
func (h *handler) accessStatusQuiet(w http.ResponseWriter, r *http.Request, b *phase3Budget, _ string) {
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, accessStatusOut{})
}

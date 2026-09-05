package main

// THE GUEST-FACING POST-STAY FLOW.
//
// Three routes, and what is NOT in any of their request bodies is the design: no room, no last name, no
// reservation number, no stay, no interface and no profile. A post-stay guest submits a PIN and nothing else;
// everything about WHO they are is derived by scd from the device this connection came from.
//
// That is a stronger position than the Phase-3 flow, which still has to take a room number because it is
// proving an identity for the first time. Post-stay is proving a SECOND time, and the first proof left a
// durable record — so the second one needs no identifier at all.
//
// UNIFORM NON-SUCCESS, through the SAME builder and the SAME response-time budget the Phase-3 flow uses. A
// second implementation of "the uniform failure" is two things that can drift, and drift here is only ever
// discovered by an attacker noticing it. The budget matters as much as the body: a wrong PIN that answers
// faster than a locked-out device tells the attacker which one they hit.
//
// The routes are mounted UNCONDITIONALLY, exactly like /auth/pms/phase3. While Phase 5 is dark scd does not
// mount its endpoints, the hop fails, and the guest gets the same uniform answer a wrong PIN gets — so the
// portal never reveals whether post-stay exists on this appliance.

import (
	"encoding/json"
	"net/http"
	"strings"
)

// postStayIn is the guest's submission. The PIN is the only field, and the only one there is any argument
// for: everything else is derived.
type postStayIn struct {
	PIN string `json:"pin,omitempty"`
	// AuthContextID + PackageRevisionID appear only on the SECOND call, after a successful PIN, when the
	// guest has picked among server-offered packages.
	AuthContextID     string `json:"auth_context_id,omitempty"`
	PackageRevisionID string `json:"package_revision_id,omitempty"`
}

// postStayOut is the guest-facing body. On issuance it carries the one-time PIN; everywhere else it carries
// as little as the flow allows.
type postStayOut struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`

	// PIN is present on a successful ISSUE and nowhere else. It is shown once: the appliance does not store
	// it and cannot produce it again, so a guest who loses this response needs a new one from the front desk.
	PIN       string `json:"pin,omitempty"`
	ExpiresAt string `json:"pin_expires_at,omitempty"`

	AuthContextID string `json:"auth_context_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	RedirectTo    string `json:"redirect_to,omitempty"`
}

type scdPostStayResp struct {
	Outcome       string `json:"outcome"`
	PIN           string `json:"pin"`
	ExpiresAt     string `json:"pin_expires_at"`
	AuthContextID string `json:"auth_context_id"`
	ExpiresIn     int    `json:"expires_in_seconds"`
	SessionID     string `json:"session_id"`
}

// postStayIssue serves POST /poststay/issue — an authenticated guest asking for a PIN to use after checkout.
//
// The guest is authenticated by virtue of the DEVICE already holding access; scd checks that lineage and
// refuses if it is absent. The portal adds no identity of its own and takes none from the body.
func (h *handler) postStayIssue(w http.ResponseWriter, r *http.Request) {
	b := h.newPhase3Budget(r)
	defer b.cancel()
	var in postStayIn
	if !decodePostStayStrict(w, r, b, h, &in) {
		return
	}
	device, ok := h.postStayDevice(w, r, b)
	if !ok {
		return
	}
	body, _ := json.Marshal(map[string]any{"device": device})
	var out scdPostStayResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase5/poststay/issue", body, &out) || out.Outcome != "ISSUED" {
		h.phase3Fail(w, r, b, "poststay_issue_unavailable")
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, postStayOut{
		OK: true, PIN: out.PIN, ExpiresAt: out.ExpiresAt,
		Message: "Write this down now. It is shown once and cannot be shown again — if you lose it, ask the " +
			"front desk for a new one.",
	})
}

// postStayAuth serves POST /auth/post-stay-pin — the departing guest coming back with their PIN.
//
// On success it does exactly what every other credential method does: it obtains a one-time Auth Context,
// never a session. The session is created only after the conversion below reaches GRANTED.
func (h *handler) postStayAuth(w http.ResponseWriter, r *http.Request) {
	b := h.newPhase3Budget(r)
	defer b.cancel()
	var in postStayIn
	if !decodePostStayStrict(w, r, b, h, &in) {
		return
	}
	device, ok := h.postStayDevice(w, r, b)
	if !ok {
		return
	}
	// A second call carrying a context is the guest choosing a package after a successful PIN.
	if strings.TrimSpace(in.AuthContextID) != "" {
		h.postStayConvert(w, r, b, in.AuthContextID, in.PackageRevisionID, device)
		return
	}
	body, _ := json.Marshal(map[string]any{"device": device, "pin": in.PIN})
	var out scdPostStayResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase5/auth/post-stay-pin", body, &out) || out.Outcome != "VERIFIED" {
		// Wrong PIN, no PIN, expired, revoked, locked out, a stale episode, a device with no post-stay
		// identity, and Phase 5 being dark are ALL this answer.
		h.phase3Fail(w, r, b, "poststay_not_verified")
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, postStayOut{OK: true, AuthContextID: out.AuthContextID})
}

func (h *handler) postStayConvert(w http.ResponseWriter, r *http.Request, b *phase3Budget,
	contextID, packageRevision string, device map[string]string) {
	body, _ := json.Marshal(map[string]any{
		"device": device, "auth_context_id": contextID, "package_revision_id": packageRevision})
	var out scdPostStayResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase5/poststay/convert", body, &out) || out.Outcome != "GRANTED" {
		h.phase3Fail(w, r, b, "poststay_convert_unavailable")
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, postStayOut{OK: true, SessionID: out.SessionID, RedirectTo: "/success"})
}

// postStayDevice derives the guest's device from the connection and the appliance's own neighbour table. It
// is the only place this flow acquires an identity, and it consults nothing the guest typed.
func (h *handler) postStayDevice(w http.ResponseWriter, r *http.Request, b *phase3Budget) (map[string]string, bool) {
	return h.originDevice(w, r, b, h.poststayFail)
}

// poststayFail keeps this flow's existing audit vocabulary exactly as it was: the shared helpers now name the
// condition and the flow supplies its own prefix, so no recorded reason code changes.
func (h *handler) poststayFail(w http.ResponseWriter, r *http.Request, b *phase3Budget, reason string) {
	h.phase3Fail(w, r, b, "poststay_"+reason)
}

// uniformFail is how a flow says "this did not succeed" WITHOUT saying why. Every one of them writes 200,
// waits the same budget first and returns one fixed message; they differ only in which flow's message and
// audit vocabulary they use, which is why this is a parameter rather than a hardcoded call. The device flow
// spent its whole life calling the AUTHENTICATION one, telling guests their stay could not be verified when
// nothing had been authenticated at all.
type uniformFail func(http.ResponseWriter, *http.Request, *phase3Budget, string)

// originDevice is postStayDevice with the caller's own uniform failure.
func (h *handler) originDevice(w http.ResponseWriter, r *http.Request, b *phase3Budget, fail uniformFail) (map[string]string, bool) {
	ip := clientIP(r)
	if ip == nil {
		fail(w, r, b, "no_source_address")
		return nil, false
	}
	mac, ok := h.arpCache(ip)
	if !ok {
		fail(w, r, b, "device_not_on_guest_network")
		return nil, false
	}
	return map[string]string{"ip": ipString(ip), "mac": mac.String()}, true
}

// decodePostStayStrict is the PUBLIC guest surface's decoder, and it is strict for a reason that only shows
// up from outside.
//
// scd's internal handlers already refuse unknown fields, so a stray `stay` or `room` never reached the
// server's own logic. But portald is the process a guest's browser actually talks to, and a permissive
// decoder there SILENTLY DROPS such a field: the request succeeds, the guest is served from their real
// device-derived identity, and nothing anywhere says the parameter was ignored. That is the worst version of
// the behaviour -- not exploitable, but indistinguishable from a surface that honours it, which is exactly
// what invites someone to build against it and eventually to argue it should work.
//
// Refusing outright makes the absence of those parameters an OBSERVABLE fact rather than an internal one.
// The refusal is the ordinary uniform non-success: a guest cannot learn from it either.
func decodePostStayStrict(w http.ResponseWriter, r *http.Request, b *phase3Budget, h *handler, dst any) bool {
	return decodeStrict(w, r, b, dst, h.poststayFail)
}

// decodeStrict is decodePostStayStrict with the caller's own uniform failure, for the same reason
// originDevice takes one.
func decodeStrict(w http.ResponseWriter, r *http.Request, b *phase3Budget, dst any, fail uniformFail) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		// One reason code for every decode failure, including an identity-looking field. Logging WHICH field
		// was sent would be useful to an operator and is deliberately not returned to the guest.
		fail(w, r, b, "malformed_request: "+strings.TrimSpace(err.Error()))
		return false
	}
	return true
}

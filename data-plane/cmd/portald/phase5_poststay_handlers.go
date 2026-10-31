package main

// A guest who has checked out submits the room number and the reservation number of the stay they left, and nothing else
// (After Check-out Access, Product-Owner decisions AD-36 and 2026-10-30): no surname, first name, code or package choice. The
// appliance lets back only a device that was authorised on that stay before check-out. The stay itself is never named: scd
// derives the candidates from the connection's device, so no stay, profile or interface appears in any body.
//
// Non-success uses the same uniform builder and response-time budget as the Phase-3 flow (a faster wrong-answer
// reply would tell an attacker which case they hit). Routes are mounted unconditionally like
// /auth/pms/phase3: while Phase 5 is dark the scd hop fails with the same uniform answer, so the portal
// never reveals whether post-stay exists.

import (
	"encoding/json"
	"net/http"
	"strings"
)

type postStayIn struct {
	Room        string `json:"room,omitempty"`
	Reservation string `json:"reservation,omitempty"`
}

type postStayOut struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	RedirectTo string `json:"redirect_to,omitempty"`
}

type scdPostStayResp struct {
	Outcome       string `json:"outcome"`
	AuthContextID string `json:"auth_context_id"`
	ExpiresIn     int    `json:"expires_in_seconds"`
	SessionID     string `json:"session_id"`
	EntitlementID string `json:"entitlement_id"`
}

// postStayAuth serves POST /auth/post-stay. The check yields a one-time Auth Context, never a session, and the portal
// completes the reconnection in the same request; the session exists only after scd reports GRANTED.
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
	body, _ := json.Marshal(map[string]any{"device": device, "room": in.Room, "reservation": in.Reservation})
	var out scdPostStayResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase5/auth/post-stay", body, &out) || out.Outcome != "VERIFIED" || out.AuthContextID == "" {
		// Every non-success, including Phase 5 being dark, gets this one answer.
		h.phase3Fail(w, r, b, "poststay_not_verified", classPostStay)
		return
	}
	h.postStayConvert(w, r, b, out.AuthContextID, device)
}

// scd answers GRANTED only once the session is enforced, so a session id means the device is online. GRANTED
// without a session id (or with the entitlement id as the session id) is not access and is refused.
func (h *handler) postStayConvert(w http.ResponseWriter, r *http.Request, b *phase3Budget,
	contextID string, device map[string]string) {
	body, _ := json.Marshal(map[string]any{"device": device, "auth_context_id": contextID})
	var out scdPostStayResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase5/poststay/convert", body, &out) || out.Outcome != "GRANTED" ||
		out.SessionID == "" || out.SessionID == out.EntitlementID {
		h.phase3Fail(w, r, b, "poststay_convert_unavailable", classPostStay)
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, postStayOut{OK: true, SessionID: out.SessionID, RedirectTo: "/success"})
}

// postStayDevice is the only place this flow acquires an identity, from the connection and the neighbour
// table, never from anything the guest typed.
func (h *handler) postStayDevice(w http.ResponseWriter, r *http.Request, b *phase3Budget) (map[string]string, bool) {
	// Device refusals use the device answer, not "check your details", which the guest cannot act on.
	return h.originDevice(w, r, b, h.poststayDeviceFail)
}

// poststayFail supplies the flow's audit reason-code prefix.
func (h *handler) poststayFail(w http.ResponseWriter, r *http.Request, b *phase3Budget, reason string) {
	h.phase3Fail(w, r, b, "poststay_"+reason, classPostStay)
}

// poststayDeviceFail covers the one condition not about the guest (the device could not be established);
// the post-stay sentence says "check your details", which would be misleading advice, so it is TECHNICAL.
func (h *handler) poststayDeviceFail(w http.ResponseWriter, r *http.Request, b *phase3Budget, reason string) {
	h.phase3Fail(w, r, b, "poststay_"+reason, classTechnical)
}

// uniformFail says "this did not succeed" without saying why: 200, the same budget wait, one fixed message.
// Flows differ only in message and audit vocabulary, hence the parameter.
type uniformFail func(http.ResponseWriter, *http.Request, *phase3Budget, string)

func (h *handler) originDevice(w http.ResponseWriter, r *http.Request, b *phase3Budget, fail uniformFail) (map[string]string, bool) {
	ip := clientIP(r)
	if ip == nil {
		fail(w, r, b, "no_source_address")
		return nil, false
	}
	// The MAC comes from the kernel neighbour table, which is asked to resolve (arp_resolve.go): a cold cache
	// is not evidence that the device is off the guest network.
	mac, ok := h.deviceMAC(b.ctx, ip)
	if !ok {
		fail(w, r, b, "device_not_on_guest_network")
		return nil, false
	}
	return map[string]string{"ip": ipString(ip), "mac": mac.String()}, true
}

// decodePostStayStrict refuses unknown fields on the public surface: a permissive decoder would silently drop
// a stray `stay` or `room`, indistinguishable from a surface that honours it. The refusal is the uniform
// non-success.
func decodePostStayStrict(w http.ResponseWriter, r *http.Request, b *phase3Budget, h *handler, dst any) bool {
	return decodeStrict(w, r, b, dst, h.poststayFail)
}

func decodeStrict(w http.ResponseWriter, r *http.Request, b *phase3Budget, dst any, fail uniformFail) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		fail(w, r, b, "malformed_request: "+strings.TrimSpace(err.Error()))
		return false
	}
	return true
}

package main

// A post-stay guest submits only a PIN; identity is derived by scd from the connection's device, so no room,
// name or stay appears in any body.
//
// Non-success uses the same uniform builder and response-time budget as the Phase-3 flow (a faster wrong-PIN
// answer would tell an attacker which case they hit). Routes are mounted unconditionally like
// /auth/pms/phase3: while Phase 5 is dark the scd hop fails with the same uniform answer, so the portal
// never reveals whether post-stay exists.

import (
	"encoding/json"
	"net/http"
	"strings"
)

type postStayIn struct {
	PIN string `json:"pin,omitempty"`
	// AuthContextID and PackageRevisionID appear only on the second call, after a successful PIN.
	AuthContextID     string `json:"auth_context_id,omitempty"`
	PackageRevisionID string `json:"package_revision_id,omitempty"`
}

type postStayOut struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`

	// PIN is present on a successful issue only and shown once; the appliance cannot produce it again.
	PIN       string `json:"pin,omitempty"`
	ExpiresAt string `json:"pin_expires_at,omitempty"`

	// NeedsChoice and Choices: a verified PIN awaiting the package choice (more than one offered).
	NeedsChoice   bool             `json:"needs_choice,omitempty"`
	AuthContextID string           `json:"auth_context_id,omitempty"`
	Choices       []postStayChoice `json:"choices,omitempty"`
	SessionID     string           `json:"session_id,omitempty"`
	RedirectTo    string           `json:"redirect_to,omitempty"`
}

// Post-stay grants zero-price packages only, so there is no method or price.
type postStayChoice struct {
	PackageRevisionID string `json:"package_revision_id"`
	Code              string `json:"code"`
	DownKbps          int    `json:"down_kbps"`
	UpKbps            int    `json:"up_kbps"`
}

type scdPostStayResp struct {
	Outcome       string           `json:"outcome"`
	PIN           string           `json:"pin"`
	ExpiresAt     string           `json:"pin_expires_at"`
	AuthContextID string           `json:"auth_context_id"`
	ExpiresIn     int              `json:"expires_in_seconds"`
	Offers        []postStayChoice `json:"offers"`
	SessionID     string           `json:"session_id"`
	EntitlementID string           `json:"entitlement_id"`
}

// postStayIssue serves POST /poststay/issue. The device already holding access authenticates the guest; scd
// checks that lineage, and the portal takes no identity from the body.
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
		h.phase3Fail(w, r, b, "poststay_issue_unavailable", classPostStay)
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, postStayOut{
		OK: true, PIN: out.PIN, ExpiresAt: out.ExpiresAt,
		Message: "Write this down now. It is shown once and cannot be shown again — if you lose it, ask the " +
			"site team for a new one.",
	})
}

// postStayAuth serves POST /auth/post-stay-pin. Success yields a one-time Auth Context, never a session;
// the session exists only after the conversion reaches GRANTED.
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
	// A context on the call means the guest is choosing a package after a successful PIN.
	if strings.TrimSpace(in.AuthContextID) != "" {
		h.postStayConvert(w, r, b, in.AuthContextID, in.PackageRevisionID, device)
		return
	}
	body, _ := json.Marshal(map[string]any{"device": device, "pin": in.PIN})
	var out scdPostStayResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase5/auth/post-stay-pin", body, &out) || out.Outcome != "VERIFIED" {
		// Every non-success, including Phase 5 being dark, gets this one answer.
		h.phase3Fail(w, r, b, "poststay_not_verified", classPostStay)
		return
	}
	// One offer is converted at once, as the room path does; more than one is the guest's choice.
	switch len(out.Offers) {
	case 0:
		// scd issues no context without an offer, so this is a contract break, not a guest condition.
		h.phase3Fail(w, r, b, "poststay_verified_without_offers", classPostStay)
		return
	case 1:
		h.postStayConvert(w, r, b, out.AuthContextID, out.Offers[0].PackageRevisionID, device)
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, postStayOut{
		OK: true, NeedsChoice: true, AuthContextID: out.AuthContextID, Choices: out.Offers})
}

// scd answers GRANTED only once the session is enforced, so a session id means the device is online. GRANTED
// without a session id (or with the entitlement id as the session id) is not access and is refused.
func (h *handler) postStayConvert(w http.ResponseWriter, r *http.Request, b *phase3Budget,
	contextID, packageRevision string, device map[string]string) {
	body, _ := json.Marshal(map[string]any{
		"device": device, "auth_context_id": contextID, "package_revision_id": packageRevision})
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

package main

// THE GUEST-FACING DEVICE SELF-SERVICE FLOW.
//
// Two routes, and as with post-stay what is NOT in their request bodies is the design: no entitlement, stay,
// room, PMS interface, profile, tenant or site. The only identifier a guest may send is which of THEIR OWN
// devices to release, and that is resolved against an entitlement the server derived from the connection —
// so an id belonging to somebody else matches nothing and answers exactly like an id that never existed.
//
// THE ROUTES ARE MOUNTED UNCONDITIONALLY, for the same reason the Phase-3 and Phase-5 routes are. Whether the
// capability EXISTS is decided twice behind this layer: scd does not mount its endpoints while the Phase-6
// deployment gate is off, and scd refuses on every request while the per-appliance product setting is off.
// Either way the hop fails and the guest gets the same uniform answer — so the portal never reveals whether
// device management exists on this appliance, which is what "absent, not present-and-refusing" has to mean
// from outside.
//
// UNIFORM NON-SUCCESS through the SAME builder and the SAME response-time budget as Phase 3 and Phase 5. A
// second implementation of "the uniform failure" is two things that can drift, and drift is only ever
// discovered by somebody looking for it.

import (
	"encoding/json"
	"net/http"
	"strings"
)

// deviceListIn is the LIST submission: deliberately empty. There is no field a guest could fill in that would
// tell the server anything it does not already know better from the connection itself.
type deviceListIn struct{}

// deviceReleaseIn names which of the caller's own devices to release, and nothing else.
type deviceReleaseIn struct {
	DeviceID string `json:"device_id"`
}

// guestDevice is one device as the GUEST sees it. No MAC, and no internal identity of any kind.
type guestDevice struct {
	ID        string `json:"id"`
	LastSeen  string `json:"last_seen,omitempty"`
	Online    bool   `json:"online"`
	Removable bool   `json:"removable"`
}

type deviceOut struct {
	OK      bool          `json:"ok"`
	Message string        `json:"message,omitempty"`
	Devices []guestDevice `json:"devices,omitempty"`
}

type scdDeviceResp struct {
	Outcome string `json:"outcome"`
	Devices []struct {
		ID        string `json:"id"`
		LastSeen  string `json:"last_seen"`
		Online    bool   `json:"online"`
		Removable bool   `json:"removable"`
	} `json:"devices"`
}

// deviceList serves POST /devices/list — the guest asking which of their devices are using their allowance.
func (h *handler) deviceList(w http.ResponseWriter, r *http.Request) {
	b := h.newPhase3Budget(r)
	defer b.cancel()
	var in deviceListIn
	if !decodePostStayStrict(w, r, b, h, &in) {
		return
	}
	device, ok := h.postStayDevice(w, r, b)
	if !ok {
		return
	}
	body, _ := json.Marshal(map[string]any{"device": device})
	var out scdDeviceResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase6/devices/list", body, &out) || out.Outcome != "LISTED" {
		// Phase 6 dark, the setting off, no entitlement for this device, or a genuine failure. All one answer.
		h.phase3Fail(w, r, b, "device_list_unavailable")
		return
	}
	devices := make([]guestDevice, 0, len(out.Devices))
	for _, d := range out.Devices {
		devices = append(devices, guestDevice{
			ID: d.ID, LastSeen: d.LastSeen, Online: d.Online, Removable: d.Removable})
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, deviceOut{OK: true, Devices: devices})
}

// deviceRelease serves POST /devices/release — the guest freeing a slot held by one of their own devices.
//
// It never explains a refusal. "That device is online", "that device is not yours", "you have released too
// many recently" and "the feature is off here" are one answer, because the differences between them are
// exactly what a guest probing the surface would want to learn. The operator sees each one in the durable
// audit, which is where that information belongs.
func (h *handler) deviceRelease(w http.ResponseWriter, r *http.Request) {
	b := h.newPhase3Budget(r)
	defer b.cancel()
	var in deviceReleaseIn
	if !decodePostStayStrict(w, r, b, h, &in) {
		return
	}
	if strings.TrimSpace(in.DeviceID) == "" {
		h.phase3Fail(w, r, b, "device_release_no_target")
		return
	}
	device, ok := h.postStayDevice(w, r, b)
	if !ok {
		return
	}
	body, _ := json.Marshal(map[string]any{"device": device, "device_id": strings.TrimSpace(in.DeviceID)})
	var out scdDeviceResp
	if !h.scdPhase3Call(b, "http://unix/v1/phase6/devices/release", body, &out) || out.Outcome != "RELEASED" {
		h.phase3Fail(w, r, b, "device_release_unavailable")
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, deviceOut{
		OK:      true,
		Message: "That device has been removed and its place is free. It can connect again at any time.",
	})
}

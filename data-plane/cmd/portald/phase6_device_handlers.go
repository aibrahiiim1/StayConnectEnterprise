package main

// THE GUEST-FACING DEVICE FLOW ON THE ONLINE PAGE: "Your devices".
//
// Two routes, and what is NOT in their request bodies is the design: no entitlement, stay, room, profile, tenant
// or site. The only identifier a client may send is which of THEIR OWN devices to release, and that is resolved
// by scd against the package the requesting device is online on -- so an id belonging to somebody else matches
// nothing and answers exactly like an id that never existed.
//
// Since migration 0118 these are served by scd's client device routes (/v1/devices/list, /v1/devices/release,
// client_devices.go), which are always on: a client can see the devices using their package, how many places it
// has, and free the place of another device -- online or not; an online one is disconnected first. The dark
// Phase-6 routes (/v1/phase6/devices/*) are no longer called from the portal.
//
// One answer for every refusal except "today's limit for changing devices is reached", which is a rule the client
// needs to know about. The exact outcome is in iam_v2.device_slot_actions.

import (
	"encoding/json"
	"log/slog"
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
	This      bool   `json:"this,omitempty"`
}

type deviceOut struct {
	OK      bool          `json:"ok"`
	Code    string        `json:"code,omitempty"`
	Message string        `json:"message,omitempty"`
	Devices []guestDevice `json:"devices,omitempty"`
	// The package's places, and the changes the client may still make today.
	Limit       int `json:"limit,omitempty"`
	InUse       int `json:"in_use,omitempty"`
	ChangesLeft int `json:"changes_left"`
}

type scdDeviceResp struct {
	Outcome string `json:"outcome"`
	Devices []struct {
		ID         string `json:"id"`
		LastSeen   string `json:"last_seen"`
		Online     bool   `json:"online"`
		Removable  bool   `json:"removable"`
		ThisDevice bool   `json:"this_device"`
	} `json:"devices"`
	Limit         int    `json:"limit"`
	InUse         int    `json:"in_use"`
	ChangesLeft   int    `json:"changes_left"`
	EntitlementID string `json:"entitlement_id"`
	DeviceID      string `json:"device_id"`
}

// guestDevice.This marks the device the page is open on (it is listed, never removable from itself).

// deviceMessage is the ONE thing a guest is told when a device request is not served, whatever the cause:
// Phase 6 dark, the per-appliance setting off, no entitlement behind this connection, or a real failure. One
// message for all of them is what keeps "the capability does not exist here" indistinguishable from "it does
// and this request did not succeed".
const deviceMessage = "Your devices are not available right now."

// deviceFail is the uniform non-success for the DEVICE routes.
//
// IT EXISTS BECAUSE THESE ROUTES USED THE AUTHENTICATION ONE, and that was untrue twice over. The portal
// page fires /devices/list on load, so on this appliance — where Phase 6 is deliberately dark — every guest
// who reached the success page produced a body reading "We could not verify your stay. Please check your
// details or contact reception." and a log line reading "phase3 guest auth not verified". The guest's stay
// HAD been verified; that is how they got to the page. Nothing had been authenticated, nothing had failed to
// verify, and an operator reading the journal saw apparent authentication failures manufactured by a feature
// that is switched off.
//
// EVERY SECURITY PROPERTY OF THE OLD PATH IS KEPT: HTTP 200 (a distinct status is itself a signal), the same
// {ok:false} shape, the same response-time budget waited BEFORE the write, and one message for every cause.
// What changes is that the answer is about devices, and the audit line says what actually happened.
func (h *handler) deviceFail(w http.ResponseWriter, r *http.Request, b *phase3Budget, reason string) {
	slog.Info("phase6 device request not served", "reason", reason)
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, deviceOut{OK: false, Message: deviceMessage})
}

// deviceList serves POST /devices/list — the guest asking which of their devices are using their allowance.
func (h *handler) deviceList(w http.ResponseWriter, r *http.Request) {
	b := h.newPhase3Budget(r)
	defer b.cancel()
	var in deviceListIn
	if !decodeStrict(w, r, b, &in, h.deviceFail) {
		return
	}
	device, ok := h.originDevice(w, r, b, h.deviceFail)
	if !ok {
		return
	}
	body, _ := json.Marshal(device)
	var out scdDeviceResp
	if !h.scdPhase3Call(b, "http://unix/v1/devices/list", body, &out) || out.Outcome != "LISTED" {
		// Not online on a package, or a genuine failure. One answer.
		h.deviceFail(w, r, b, "device_list_unavailable")
		return
	}
	devices := make([]guestDevice, 0, len(out.Devices))
	for _, d := range out.Devices {
		devices = append(devices, guestDevice{
			ID: d.ID, LastSeen: d.LastSeen, Online: d.Online, Removable: d.Removable && out.ChangesLeft > 0,
			This: d.ThisDevice})
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, deviceOut{OK: true, Devices: devices, Limit: out.Limit, InUse: out.InUse,
		ChangesLeft: out.ChangesLeft})
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
	if !decodeStrict(w, r, b, &in, h.deviceFail) {
		return
	}
	if strings.TrimSpace(in.DeviceID) == "" {
		h.deviceFail(w, r, b, "device_release_no_target")
		return
	}
	device, ok := h.originDevice(w, r, b, h.deviceFail)
	if !ok {
		return
	}
	body, _ := json.Marshal(map[string]any{"ip": device["ip"], "mac": device["mac"], "device_id": strings.TrimSpace(in.DeviceID)})
	var out scdDeviceResp
	ok = h.scdPhase3Call(b, "http://unix/v1/devices/release", body, &out)
	if ok && out.Outcome == "LIMIT_TODAY" {
		slog.Info("device release refused", "reason", "changes_per_day")
		b.wait(r)
		writeJSONPortal(w, http.StatusOK, deviceOut{OK: false, Code: "limit_today",
			Message: "You've reached today's limit for changing devices. Please contact the site team for assistance."})
		return
	}
	if !ok || out.Outcome != "RELEASED" {
		h.deviceFail(w, r, b, "device_release_unavailable")
		return
	}
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, deviceOut{
		OK:      true,
		Message: "That device has been removed and its place is free. It can connect again at any time.",
	})
}

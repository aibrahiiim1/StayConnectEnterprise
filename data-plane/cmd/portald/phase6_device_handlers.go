package main

// "Your devices": the only identifier a client may send is which of their own devices to release, resolved by
// scd against the package the requesting device is online on, so someone else's id answers like an unknown one.
// Served by scd's always-on client device routes (/v1/devices/list, /v1/devices/release, client_devices.go).
// Every refusal gets one answer except "today's limit for changing devices", which the client needs to know.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// deviceListIn is deliberately empty: the connection already says everything.
type deviceListIn struct{}

type deviceReleaseIn struct {
	DeviceID string `json:"device_id"`
}

// guestDevice exposes no MAC and no internal identity.
type guestDevice struct {
	ID        string `json:"id"`
	LastSeen  string `json:"last_seen,omitempty"`
	Online    bool   `json:"online"`
	Removable bool   `json:"removable"`
	This      bool   `json:"this,omitempty"`
}

type deviceOut struct {
	OK          bool          `json:"ok"`
	Code        string        `json:"code,omitempty"`
	Message     string        `json:"message,omitempty"`
	Devices     []guestDevice `json:"devices,omitempty"`
	Limit       int           `json:"limit,omitempty"`
	InUse       int           `json:"in_use,omitempty"`
	ChangesLeft int           `json:"changes_left"`
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

// deviceMessage is the one answer for every unserved device request, so "not available here" is
// indistinguishable from "this request failed".
const deviceMessage = "Your devices are not available right now."

// deviceFail is the uniform non-success for the device routes (not the authentication one, whose "could not
// verify your stay" text and audit line would be false here). It keeps HTTP 200 (a distinct status is a
// signal), the {ok:false} shape, and the budget wait before the write.
func (h *handler) deviceFail(w http.ResponseWriter, r *http.Request, b *phase3Budget, reason string) {
	slog.Info("phase6 device request not served", "reason", reason)
	b.wait(r)
	writeJSONPortal(w, http.StatusOK, deviceOut{OK: false, Message: deviceMessage})
}

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

// deviceRelease never explains a refusal (not yours, too many releases, feature off): the differences are
// what a probing guest wants to learn. The durable audit records each one.
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

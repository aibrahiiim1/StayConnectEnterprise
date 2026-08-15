package main

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// THE GUEST-FACING PRESENTATION of device self-service.
//
// The durable rules are enforced in the database and the refusals are uniform at the HTTP layer; what is
// left for this page to get wrong is disclosure and comprehension, and both are real:
//
//   - a MAC address on a guest page is a stable identifier for somebody's phone on a shared network;
//   - a panel that appears on an appliance where the capability is dark tells the guest it exists;
//   - a "Remove" button on a device that cannot be removed produces a refusal the guest cannot interpret;
//   - a refusal that explains itself gives a probing guest the oracle the API refuses them.

func renderSuccess(t *testing.T) string {
	t.Helper()
	tpl, err := template.New("succ").Parse(successHTML)
	if err != nil {
		t.Fatalf("the success page does not parse: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{
		"SessionID": "sess-1", "DurationSeconds": 3600, "HumanRemaining": "1h",
		"CommerceEnabled": false,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// devicesPanel returns just the device panel and its script, so an assertion about "the page must not
// mention X" cannot be satisfied or broken by unrelated parts of the page.
func devicesPanel(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `<div id="devices"`)
	if i < 0 {
		t.Fatal("the success page has no device panel")
	}
	return page[i:]
}

// THE PANEL IS HIDDEN UNTIL THE APPLIANCE ANSWERS. On an appliance where Phase 6 is dark or the hotel has
// the setting off, the list call fails and the guest sees an ordinary success page.
func TestGuestDevicePanelStartsHidden(t *testing.T) {
	panel := devicesPanel(t, renderSuccess(t))
	head := panel[:strings.Index(panel, ">")+1]
	if !strings.Contains(head, "hidden") {
		t.Fatalf("the device panel is not hidden by default: %s", head)
	}
	// ...and it is only ever revealed on a successful, non-empty answer.
	if !strings.Contains(panel, "panel.hidden = false") {
		t.Fatal("nothing ever reveals the panel")
	}
	for _, guard := range []string{"!res.ok", "res.devices.length===0"} {
		if !strings.Contains(panel, guard) {
			t.Fatalf("the panel is revealed without checking %q", guard)
		}
	}
}

// NO MAC, AND NO INTERNAL IDENTITY, ANYWHERE THE GUEST CAN READ.
func TestGuestDevicePanelExposesNoMACOrInternalIdentity(t *testing.T) {
	panel := strings.ToLower(devicesPanel(t, renderSuccess(t)))
	for _, forbidden := range []string{"mac", "entitlement", "stay_id", "room", "pms", "profile", "tenant", "site_id"} {
		if strings.Contains(panel, forbidden) {
			t.Fatalf("the guest device panel mentions %q", forbidden)
		}
	}
	// The opaque id is needed as a release target, and must travel in the request rather than be displayed.
	if !strings.Contains(panel, "device_id: id") {
		t.Fatal("the release call does not carry the device id it was given")
	}
	if strings.Contains(panel, "textContent = d.id") || strings.Contains(panel, "textContent=d.id") {
		t.Fatal("the device id is rendered as visible text")
	}
}

// An ONLINE device is visibly non-removable, and the guest is told what to do instead of being handed a
// button that will refuse.
func TestGuestDevicePanelMarksOnlineDevicesNonRemovable(t *testing.T) {
	panel := devicesPanel(t, renderSuccess(t))
	if !strings.Contains(panel, "d.removable") {
		t.Fatal("the panel does not consult the removable flag at all")
	}
	if !strings.Contains(panel, "can’t be removed") {
		t.Fatal("an online device is not explained to the guest")
	}
	if !strings.Contains(panel, "Disconnect it from the Wi‑Fi first") {
		t.Fatal("the guest is not told how to make the device removable")
	}
	if !strings.Contains(panel, "Connected now") || !strings.Contains(panel, "Not connected") {
		t.Fatal("the panel does not show whether a device is connected")
	}
}

// The removal is confirmed, states its consequence, and reports a clear result.
func TestGuestDeviceRemovalIsConfirmedAndItsResultIsClear(t *testing.T) {
	panel := devicesPanel(t, renderSuccess(t))
	if !strings.Contains(panel, "window.confirm(") {
		t.Fatal("a device is removed without confirmation")
	}
	if !strings.Contains(panel, "can connect again at any time") {
		t.Fatal("the confirmation does not say the device can come back")
	}
	if !strings.Contains(panel, "dv-done") || !strings.Contains(panel, "its place is free") {
		t.Fatal("a successful removal has no clear result")
	}
	// The list is refreshed afterwards, so the screen and the appliance agree.
	if !strings.Contains(panel, "load(false)") {
		t.Fatal("the list is not reloaded after a removal")
	}
}

// EVERY REFUSAL IS THE SAME SENTENCE. The API collapses "online", "not yours", "already gone", "throttled"
// and "switched off" into one answer; a page that guessed between them would undo that.
func TestGuestDeviceRefusalSaysNothing(t *testing.T) {
	panel := devicesPanel(t, renderSuccess(t))
	if !strings.Contains(panel, "That didn’t work. Please try again in a moment.") {
		t.Fatal("the refusal message is missing")
	}
	if strings.Count(panel, "note.className='dv-err'") != 1 {
		t.Fatal("there is more than one refusal message, so refusals are distinguishable")
	}
	// Scanned over what the guest can READ -- the lines that put words on the screen -- rather than over the
	// whole script, so that `btn.disabled = true` is not mistaken for the word "disabled" being shown.
	for _, line := range strings.Split(panel, "\n") {
		low := strings.ToLower(line)
		if !strings.Contains(low, "textcontent") && !strings.Contains(low, "confirm(") {
			continue
		}
		for _, leak := range []string{"too many", "throttl", "not yours", "already", "disabled", "switched off",
			"turned off", "online elsewhere"} {
			if strings.Contains(low, leak) {
				t.Fatalf("the panel shows the guest a refusal reason (%q): %s", leak, strings.TrimSpace(line))
			}
		}
	}
}

// The panel talks to the two public routes and nothing else.
func TestGuestDevicePanelUsesOnlyThePublicRoutes(t *testing.T) {
	panel := devicesPanel(t, renderSuccess(t))
	if !strings.Contains(panel, "'/devices/list'") || !strings.Contains(panel, "'/devices/release'") {
		t.Fatal("the panel does not call the public device routes")
	}
	// No Central, no cloud, no absolute URL of any kind: this page works on an appliance with no uplink.
	for _, off := range []string{"http://", "https://", "//stayconnect", "cloud", "central"} {
		if strings.Contains(strings.ToLower(panel), off) {
			t.Fatalf("the guest device panel reaches outside the appliance: %q", off)
		}
	}
}

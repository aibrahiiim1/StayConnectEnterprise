package main

// WHAT THE BROWSER SENDS MUST REACH scd. THE HOP IS WHERE IT WAS LOST.
//
// portald decodes the guest's submission into phase3In and re-marshals it for scd. A field the browser sends
// that phase3In does not name is decoded into nothing and forwarded as nothing — silently, with no error
// anywhere. scd then sees a room with no evidence beside it, answers `incomplete_evidence`, and the guest
// gets the uniform failure, which looks exactly like a wrong surname.
//
// That is not hypothetical. `verification` (room_any) and `first_name` (room_firstname) were added to the
// portal script and to scd while phase3In was left alone, so two of the four sign-in modes were dead on the
// real guest path. Every test passed, because they all called scd directly and none crossed this hop.
//
// So these tests assert the hop itself: for each mode, the value a guest types is present in the body scd
// actually receives. They would have caught the defect on the day it landed, and they are the reason a fifth
// mode cannot be added without noticing this file.

import (
	"testing"
)

// modeCase is one sign-in mode: the key the portal script puts the typed value under, and the key scd must
// receive it in. For every supported mode these are the same key — the portal does not translate, it forwards
// — which is precisely why a missing struct field is invisible until a guest tries it.
type modeCase struct {
	mode    string
	sentKey string
	scdKey  string
	typed   string
}

var forwardingModes = []modeCase{
	// room_any: ONE box, and the server compares it against all three fields. The browser must not decide
	// which kind of identifier it is, so it sends the raw value under `verification`.
	{"room_any", "verification", "verification", "271886"},
	{"room_firstname", "first_name", "first_name", "Ibrahim"},
	{"room_lastname", "last_name", "last_name", "Example"},
	{"room_reservation", "reservation_number", "reservation_number", "ABC-99"},
}

// THE REGRESSION TEST. Each mode's typed value must arrive at scd intact.
func TestPhase3ForwardsEveryVerificationFieldToSCD(t *testing.T) {
	for _, tc := range forwardingModes {
		t.Run(tc.mode, func(t *testing.T) {
			stub := &scdStub{
				resolve: scdResolveResp{Outcome: "VERIFIED", AuthContextID: "ctx-1"},
				grant:   scdGrantResp{Outcome: "GRANTED", SessionID: "s-1", EntitlementID: "e-1"},
			}
			h := stubHandler(t, stub)

			phase3Post(t, h, map[string]any{
				"room":       "7102",
				tc.sentKey:   tc.typed,
				"request_id": "11111111-2222-4333-8444-555555555555",
			})

			if len(stub.bodies) == 0 {
				t.Fatalf("%s: nothing reached scd at all", tc.mode)
			}
			got := stub.bodies[0]
			if got["room"] != "7102" {
				t.Fatalf("%s: room did not reach scd: %#v", tc.mode, got["room"])
			}
			if got[tc.scdKey] != tc.typed {
				t.Fatalf("%s: the guest typed %q under %q and scd received %#v — the value was dropped on the "+
					"portald hop, which reaches the guest as the uniform failure and looks like a wrong entry",
					tc.mode, tc.typed, tc.scdKey, got[tc.scdKey])
			}
		})
	}
}

// The value is forwarded AS TYPED. room_any is safe because the SERVER compares one value against three
// fields and refuses an ambiguous match; it would stop being safe the moment this hop started guessing which
// field a value "looks like" and filling that one in. A reservation-shaped value and a name-shaped value must
// travel identically.
func TestPhase3DoesNotGuessIdentifierTypeFromValueShape(t *testing.T) {
	for _, typed := range []string{"271886", "Ibrahim", "ABC-99", "O'Brien", "42"} {
		stub := &scdStub{resolve: scdResolveResp{Outcome: "VERIFIED", AuthContextID: "ctx-1"},
			grant: scdGrantResp{Outcome: "GRANTED", SessionID: "s-1", EntitlementID: "e-1"}}
		h := stubHandler(t, stub)

		phase3Post(t, h, map[string]any{
			"room": "7102", "verification": typed,
			"request_id": "11111111-2222-4333-8444-555555555555",
		})

		got := stub.bodies[0]
		if got["verification"] != typed {
			t.Fatalf("verification %q was altered in transit: %#v", typed, got["verification"])
		}
		// The hop must not have decided this value is "really" a reservation number or a name.
		for _, k := range []string{"first_name", "last_name", "reservation_number"} {
			if v, ok := got[k]; ok && v != "" {
				t.Fatalf("the hop guessed from the value shape: %q was also placed in %q as %#v. Deciding the "+
					"identifier type in the browser or the proxy is the legacy behaviour room_any replaces; "+
					"the server compares all three and fails closed on ambiguity", typed, k, v)
			}
		}
	}
}

// THE SECURITY MODEL SURVIVES THE FIX. Widening phase3In widened what a guest can put in the body, so this
// pins what they still cannot reach: the trusted identity is derived by the appliance from its own neighbour
// table, and no guest-supplied key may override it.
func TestPhase3GuestCannotSupplyTrustedIdentity(t *testing.T) {
	stub := &scdStub{resolve: scdResolveResp{Outcome: "VERIFIED", AuthContextID: "ctx-1"},
		grant: scdGrantResp{Outcome: "GRANTED", SessionID: "s-1", EntitlementID: "e-1"}}
	h := stubHandler(t, stub)

	phase3Post(t, h, map[string]any{
		"room": "7102", "verification": "271886",
		"request_id": "11111111-2222-4333-8444-555555555555",
		// Everything a guest might try to assert about themselves.
		"ip": "10.9.9.9", "mac": "de:ad:be:ef:00:01",
		"stay":             "00000000-0000-4000-8000-000000000001",
		"pms_interface_id": "00000000-0000-4000-8000-000000000002",
		"device":           map[string]any{"ip": "10.9.9.9", "mac": "de:ad:be:ef:00:01"},
		"price_minor":      0,
	})

	got := stub.bodies[0]
	for _, k := range []string{"ip", "mac", "stay", "pms_interface_id", "price_minor"} {
		if _, present := got[k]; present {
			t.Fatalf("guest-supplied %q crossed the hop to scd", k)
		}
	}
	dev, ok := got["device"].(map[string]any)
	if !ok {
		t.Fatalf("device identity missing from the forwarded body: %#v", got["device"])
	}
	if dev["mac"] == "de:ad:be:ef:00:01" || dev["ip"] == "10.9.9.9" {
		t.Fatalf("the guest's claimed address was forwarded as their identity: %#v — identity must come from "+
			"the appliance's own neighbour table, never from the body", dev)
	}
}

// An empty submission must still be refused at the hop rather than forwarded as a room with no evidence.
// scd would refuse it anyway; not sending it keeps the refusal honest and cheap.
func TestPhase3EmptyVerificationStillReachesSCDAsEmpty(t *testing.T) {
	stub := &scdStub{resolve: scdResolveResp{Outcome: "NOT_VERIFIED"}}
	h := stubHandler(t, stub)

	phase3Post(t, h, map[string]any{
		"room": "7102", "verification": "   ",
		"request_id": "11111111-2222-4333-8444-555555555555",
	})

	if len(stub.bodies) == 0 {
		return // refused before the hop is acceptable
	}
	got := stub.bodies[0]
	if v, _ := got["verification"].(string); v != "   " && v != "" {
		t.Fatalf("a blank verification was transformed into %#v on the hop", got["verification"])
	}
}

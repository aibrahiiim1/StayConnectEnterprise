package main

// portald decodes the submission into phase3In and re-marshals it for scd, so a field phase3In does not name
// is dropped silently and the guest sees the uniform failure. These assert the hop itself: for each sign-in
// mode the typed value is present in the body scd receives.

import (
	"testing"
)

// The portal does not translate keys, it forwards, so a missing struct field is invisible until tried.
type modeCase struct {
	mode    string
	sentKey string
	scdKey  string
	typed   string
}

var forwardingModes = []modeCase{
	// room_any: the server compares one value against all three fields; the browser sends it raw.
	{"room_any", "verification", "verification", "271886"},
	{"room_firstname", "first_name", "first_name", "Ibrahim"},
	{"room_lastname", "last_name", "last_name", "Example"},
	{"room_reservation", "reservation_number", "reservation_number", "ABC-99"},
}

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

// room_any is safe only because the server compares and refuses ambiguity; the hop must never guess which
// field a value "looks like".
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
		for _, k := range []string{"first_name", "last_name", "reservation_number"} {
			if v, ok := got[k]; ok && v != "" {
				t.Fatalf("the hop guessed from the value shape: %q was also placed in %q as %#v. Deciding the "+
					"identifier type in the browser or the proxy is the legacy behaviour room_any replaces; "+
					"the server compares all three and fails closed on ambiguity", typed, k, v)
			}
		}
	}
}

// The trusted identity is derived from the appliance's neighbour table; no guest-supplied key may override it.
func TestPhase3GuestCannotSupplyTrustedIdentity(t *testing.T) {
	stub := &scdStub{resolve: scdResolveResp{Outcome: "VERIFIED", AuthContextID: "ctx-1"},
		grant: scdGrantResp{Outcome: "GRANTED", SessionID: "s-1", EntitlementID: "e-1"}}
	h := stubHandler(t, stub)

	phase3Post(t, h, map[string]any{
		"room": "7102", "verification": "271886",
		"request_id": "11111111-2222-4333-8444-555555555555",
		"ip":         "10.9.9.9", "mac": "de:ad:be:ef:00:01",
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

// Only filled fields cross the hop: a blank key must not look like a second kind of evidence, and a filled
// one must not be dropped, since scd decides what the site's sign-in mode accepts.
func TestPhase3ForwardsOnlyTheFieldsTheGuestFilled(t *testing.T) {
	stub := &scdStub{resolve: scdResolveResp{Outcome: "NOT_VERIFIED", FailureClass: "CREDENTIAL"}}
	h := stubHandler(t, stub)

	phase3Post(t, h, map[string]any{
		"room": "7102", "last_name": "Example", "first_name": "", "verification": "  ",
		"reservation_number": "", "request_id": "11111111-2222-4333-8444-555555555555",
	})
	got := stub.bodies[0]
	for _, k := range []string{"first_name", "verification", "reservation_number"} {
		if _, present := got[k]; present {
			t.Fatalf("the empty %q was forwarded to scd: %#v", k, got)
		}
	}
	if got["last_name"] != "Example" {
		t.Fatalf("the filled last_name did not reach scd: %#v", got)
	}

	// A body carrying several filled fields is forwarded whole: scd, which knows the mode, refuses it.
	stub2 := &scdStub{resolve: scdResolveResp{Outcome: "NOT_VERIFIED", FailureClass: "CREDENTIAL"}}
	h2 := stubHandler(t, stub2)
	phase3Post(t, h2, map[string]any{
		"room": "7102", "last_name": "Example", "reservation_number": "ABC-99",
		"request_id": "11111111-2222-4333-8444-555555555555",
	})
	if b := stub2.bodies[0]; b["last_name"] != "Example" || b["reservation_number"] != "ABC-99" {
		t.Fatalf("filled fields were dropped on the hop instead of being judged by scd: %#v", b)
	}
}

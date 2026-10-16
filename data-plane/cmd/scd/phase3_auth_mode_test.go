package main

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

func TestRoomSignInModeReadsAsThePortalDoes(t *testing.T) {
	for stored, want := range map[string]string{
		"room_any": roomModeAny, "room_lastname": roomModeLastName, "room_firstname": roomModeFirstName,
		"room_reservation": roomModeReservation, "either": roomModeEither, "": roomModeEither,
		"something_new": roomModeEither, " room_lastname ": roomModeLastName,
	} {
		got := roomSignInMode(&tenantcfg.AuthMethods{PMS: &tenantcfg.PMSConfig{Enabled: true, Mode: stored}})
		if got != want {
			t.Fatalf("stored %q read as %q, want %q", stored, got, want)
		}
	}
	if roomSignInMode(nil) != roomModeEither || roomSignInMode(&tenantcfg.AuthMethods{}) != roomModeEither {
		t.Fatal("an absent pms block must read as the legacy setting, as the portal draws it")
	}
}

func TestApplyRoomSignInMode(t *testing.T) {
	type in struct{ v, l, f, r string }
	cases := []struct {
		mode      string
		in        in
		want      roomEvidence
		violation bool
	}{
		// room_any: unchanged combined behaviour.
		{roomModeAny, in{v: "Smith"}, roomEvidence{Last: "SMITH", First: "SMITH", Res: "Smith"}, false},
		{roomModeAny, in{l: "SMITH", r: "R1"}, roomEvidence{Last: "SMITH", Res: "R1"}, false},
		{roomModeAny, in{v: "x", l: "SMITH"}, roomEvidence{Last: "SMITH"}, false},
		{roomModeAny, in{}, roomEvidence{}, false},

		// single-field modes: only that field.
		{roomModeLastName, in{l: "SMITH"}, roomEvidence{Last: "SMITH"}, false},
		{roomModeLastName, in{r: "R1"}, roomEvidence{}, true},
		{roomModeLastName, in{l: "SMITH", r: "R1"}, roomEvidence{}, true},
		{roomModeLastName, in{v: "SMITH"}, roomEvidence{}, true},
		{roomModeLastName, in{l: "SMITH", f: "JO"}, roomEvidence{}, true},
		{roomModeLastName, in{}, roomEvidence{}, false}, // blank is incomplete evidence, not a violation
		{roomModeFirstName, in{f: "JO"}, roomEvidence{First: "JO"}, false},
		{roomModeFirstName, in{l: "SMITH"}, roomEvidence{}, true},
		{roomModeReservation, in{r: "R1"}, roomEvidence{Res: "R1"}, false},
		{roomModeReservation, in{l: "SMITH"}, roomEvidence{}, true},
		{roomModeReservation, in{v: "R1"}, roomEvidence{}, true},

		// legacy either: last name OR reservation number, exactly one.
		{roomModeEither, in{l: "SMITH"}, roomEvidence{Last: "SMITH"}, false},
		{roomModeEither, in{r: "R1"}, roomEvidence{Res: "R1"}, false},
		{roomModeEither, in{l: "SMITH", r: "R1"}, roomEvidence{}, true},
		{roomModeEither, in{f: "JO"}, roomEvidence{}, true},
		{roomModeEither, in{v: "SMITH"}, roomEvidence{}, true},
		// whitespace is not a field
		{roomModeLastName, in{l: "SMITH", r: "  "}, roomEvidence{Last: "SMITH"}, false},
	}
	for _, c := range cases {
		got := applyRoomSignInMode(c.mode, c.in.v, c.in.l, c.in.f, c.in.r)
		c.want.Violation = c.violation
		if got != c.want {
			t.Fatalf("%s %+v: got %+v, want %+v", c.mode, c.in, got, c.want)
		}
	}
}

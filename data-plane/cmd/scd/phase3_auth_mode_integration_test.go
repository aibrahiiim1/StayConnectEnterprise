//go:build integration

package main

// THE CONFIGURED ROOM SIGN-IN MODE, ENFORCED BY scd, through the real resolve handler against a real schema.
//
// The fixture's stay in room 412 has surname OKONKWO and reservation RES-4001; both are correct evidence for
// that stay. Under room_lastname only the surname may be used, so the correct reservation number -- alone, or
// sent beside the correct surname -- must be refused. NO REAL GUEST DATA APPEARS IN THIS FILE.

import (
	"context"
	"testing"
)

// setRoomMode stores the site's room sign-in mode exactly where Hotel Admin stores it.
func setRoomMode(t *testing.T, f *authFixture, mode string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE public.tenants SET auth_methods = jsonb_set(auth_methods, '{pms,mode}', to_jsonb($2::text)) WHERE id=$1`,
		f.tenant, mode); err != nil {
		t.Fatalf("set room sign-in mode: %v", err)
	}
}

func (f *authFixture) roomBody(room string, fields map[string]string, reqID string) map[string]any {
	b := map[string]any{"room": room, "request_id": reqID,
		"device": map[string]string{"ip": f.net.guestIP, "mac": f.net.mac}}
	for k, v := range fields {
		b[k] = v
	}
	return b
}

// failureCount is the device's rolling count of counted wrong credentials, read the way the policy reads it.
func failureCount(t *testing.T, f *authFixture) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.sign_in_attempts
		  WHERE tenant_id=$1 AND site_id=$2 AND result IN ('CREDENTIAL_MISMATCH','ROOM_NOT_IN_MIRROR')`,
		f.tenant, f.site).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntegration_RoomMode_LastNameSiteAcceptsOnlyTheSurname(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)
	setRoomMode(t, f, roomModeLastName)

	refused := []map[string]string{
		{"reservation_number": "RES-4001"},                         // correct reservation number, wrong kind
		{"last_name": "Okonkwo", "reservation_number": "RES-4001"}, // both correct, but two fields
		{"verification": "Okonkwo"},                                // the room_any field
		{"first_name": "Okonkwo"},                                  // another name field
	}
	for i, fields := range refused {
		before := attemptCount(t, f)
		_, res := post(t, f.p3.resolveHandler, f.roomBody("412", fields, f.reqID(t, i)))
		if res.Outcome == outcomeVerified {
			t.Fatalf("room_lastname site verified a request carrying %v; the mode is not enforced", fields)
		}
		if res.FailureClass != "CREDENTIAL" {
			t.Fatalf("%v: failure_class=%q, want the uniform CREDENTIAL answer", fields, res.FailureClass)
		}
		if got := attemptCount(t, f) - before; got != 1 {
			t.Fatalf("%v: %d attempt rows for one request, want 1", fields, got)
		}
		a := lastAttempt(t, f)
		if a.Result != "CREDENTIAL_MISMATCH" || a.MatchedField != "" {
			t.Fatalf("%v: recorded %+v", fields, a)
		}
		if a.Eligible == nil || *a.Eligible != 0 {
			t.Fatalf("%v: the mirror was searched for a request the mode refuses: %+v", fields, a)
		}
	}
	if n := failureCount(t, f); n != len(refused) {
		t.Fatalf("%d counted failures for %d mode-violating requests, want exactly one each", n, len(refused))
	}

	_, ok := post(t, f.p3.resolveHandler, f.roomBody("412", map[string]string{"last_name": "Okonkwo"}, f.reqID(t, 9)))
	if ok.Outcome != outcomeVerified || ok.AuthContextID == "" {
		t.Fatalf("room_lastname site refused the correct surname: %+v", ok)
	}
}

func TestIntegration_RoomMode_ReservationSiteAcceptsOnlyTheReservation(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)
	setRoomMode(t, f, roomModeReservation)

	_, res := post(t, f.p3.resolveHandler, f.roomBody("412", map[string]string{"last_name": "Okonkwo"}, f.reqID(t, 0)))
	if res.Outcome == outcomeVerified {
		t.Fatal("room_reservation site verified a surname")
	}
	_, ok := post(t, f.p3.resolveHandler, f.roomBody("412", map[string]string{"reservation_number": "RES-4001"}, f.reqID(t, 1)))
	if ok.Outcome != outcomeVerified {
		t.Fatalf("room_reservation site refused the correct reservation number: %+v", ok)
	}
}

// room_any IS UNCHANGED: the one combined value, and explicit fields, are compared as before.
func TestIntegration_RoomMode_AnyIsUnchanged(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)
	setRoomMode(t, f, roomModeAny)

	for i, fields := range []map[string]string{
		{"verification": "Okonkwo"},
		{"verification": "RES-4001"},
		{"last_name": "Okonkwo"},
		{"reservation_number": "RES-4001"},
		// The audit's T34 shape: right reservation number, wrong names -- "any one of" admits it.
		{"last_name": "Wrong", "first_name": "Wrong", "reservation_number": "RES-4001"},
	} {
		_, res := post(t, f.p3.resolveHandler, f.roomBody("412", fields, f.reqID(t, i)))
		if res.Outcome != outcomeVerified {
			t.Fatalf("room_any refused %v: %+v", fields, res)
		}
	}
}

// THE LEGACY "either" SETTING: last name OR reservation number, exactly one, as the portal sends it.
func TestIntegration_RoomMode_LegacyEither(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)
	setRoomMode(t, f, roomModeEither)

	for i, fields := range []map[string]string{{"last_name": "Okonkwo"}, {"reservation_number": "RES-4001"}} {
		if _, res := post(t, f.p3.resolveHandler, f.roomBody("412", fields, f.reqID(t, i))); res.Outcome != outcomeVerified {
			t.Fatalf("either refused %v: %+v", fields, res)
		}
	}
	for i, fields := range []map[string]string{
		{"last_name": "Okonkwo", "reservation_number": "RES-4001"},
		{"verification": "Okonkwo"},
		{"first_name": "Okonkwo"},
	} {
		if _, res := post(t, f.p3.resolveHandler, f.roomBody("412", fields, f.reqID(t, 10+i))); res.Outcome == outcomeVerified {
			t.Fatalf("either verified %v", fields)
		}
	}
}

// ONE REQUEST IS ONE ATTEMPT, however many fields it carries: a room_any request with three wrong values is
// one row and one count, never three -- and a correct field among wrong ones is one success, not a success
// and two failures.
func TestIntegration_RoomMode_OneRequestWithManyFieldsIsOneAttempt(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	soleOccupant(t, f)
	setRoomMode(t, f, roomModeAny)

	before := attemptCount(t, f)
	_, res := post(t, f.p3.resolveHandler, f.roomBody("412", map[string]string{
		"last_name": "WrongA", "first_name": "WrongB", "reservation_number": "WRONG-C"}, f.reqID(t, 0)))
	if res.Outcome == outcomeVerified {
		t.Fatal("three wrong values verified")
	}
	if got := attemptCount(t, f) - before; got != 1 {
		t.Fatalf("one request with three fields wrote %d attempt rows", got)
	}
	if n := failureCount(t, f); n != 1 {
		t.Fatalf("one request with three wrong fields counted %d failures, want 1", n)
	}
	var restrictionCount int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.guest_signin_restrictions WHERE tenant_id=$1 AND site_id=$2 AND expires_at > now()`,
		f.tenant, f.site).Scan(&restrictionCount); err != nil {
		t.Fatal(err)
	}
	if restrictionCount != 0 {
		t.Fatal("one request restricted the device")
	}

	before = attemptCount(t, f)
	_, ok := post(t, f.p3.resolveHandler, f.roomBody("412", map[string]string{
		"last_name": "WrongA", "first_name": "WrongB", "reservation_number": "RES-4001"}, f.reqID(t, 1)))
	if ok.Outcome != outcomeVerified {
		t.Fatalf("room_any refused a request carrying the correct reservation number: %+v", ok)
	}
	if got := attemptCount(t, f) - before; got != 1 {
		t.Fatalf("one successful request wrote %d attempt rows", got)
	}
	if n := failureCount(t, f); n != 1 {
		t.Fatalf("a successful request added counted failures: %d", n)
	}
}

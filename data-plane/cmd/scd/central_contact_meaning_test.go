package main

import (
	"errors"
	"testing"
	"time"
)

// last_contact_at means "the last successful exchange with Central since scd started". A restart with the network to
// Central cut must read "never", not the restart time (the certificate load from disk used to be recorded as contact).
func TestLastContactIsNullUntilCentralActuallyAnswers(t *testing.T) {
	c := &centralContact{}
	ok, fail, _ := c.snapshot()
	if !ok.IsZero() || !fail.IsZero() {
		t.Fatal("a fresh contact record must hold neither an answer nor a failure")
	}
	in := centralInputs{Now: now0, CentralConfigured: true, ApplianceID: "appl-1", CertRequired: true, CertReady: true}
	in.LastOK, in.LastFail, in.LastErr = c.snapshot()
	link := centralLinkState(in)
	if link.LastContactAt != nil {
		t.Fatalf("last_contact_at = %v with no exchange since start, want null", link.LastContactAt)
	}
	if link.State == centralConnected {
		t.Fatal("a link nobody has measured must not read connected")
	}
}

// Identity or certificate not loaded and nothing exchanged yet: its own state, neither connected nor unreachable.
func TestIdentityNotLoadedIsItsOwnState(t *testing.T) {
	cases := map[string]centralInputs{
		"no identity yet":      {Now: now0, CentralConfigured: true},
		"identity but no cert": {Now: now0, CentralConfigured: true, ApplianceID: "a", CertRequired: true, CertReady: false},
	}
	for name, in := range cases {
		got := centralLinkState(in)
		if got.State != centralIdentityNotLoaded || got.LastContactAt != nil || got.LastError != nil {
			t.Errorf("%s: %+v, want identity_not_loaded with no contact and no error", name, got)
		}
	}
	// Once an exchange has been attempted the real result is reported: a failed attempt is unreachable, not "not loaded".
	failed := centralInputs{Now: now0, CentralConfigured: true, ApplianceID: "a", CertRequired: true, CertReady: false,
		LastFail: now0.Add(-time.Second), LastErr: "OneGate Central did not answer in time."}
	if got := centralLinkState(failed); got.State != centralUnreachable || got.LastContactAt != nil {
		t.Fatalf("a failed attempt must read unreachable with no contact time, got %+v", got)
	}
	// And a configured-nothing appliance is still not_configured.
	if got := centralLinkState(centralInputs{Now: now0}); got.State != centralNotConfigured {
		t.Fatalf("no Central address: %s", got.State)
	}
}

// A failure must never move last_contact_at, and a real answer must.
func TestFailureDoesNotMoveLastContact(t *testing.T) {
	c := &centralContact{}
	c.record(errors.New("dial tcp: connection refused"), now0)
	ok, _, _ := c.snapshot()
	if !ok.IsZero() {
		t.Fatal("a refused connection was recorded as contact")
	}
	c.record(nil, now0.Add(time.Minute))
	ok, _, _ = c.snapshot()
	if !ok.Equal(now0.Add(time.Minute)) {
		t.Fatal("an answered call must be recorded")
	}
}

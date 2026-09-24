package pmsd

// PROTEL FIAS IS THE PROTECTED, LIVE-ACCEPTED BASELINE.
//
// The provider registry replaced four hand-kept lists. These tests pin that the protel-fias path came out of
// that refactor unchanged: it is still a supported kind, its revisions validate exactly as before, its durable
// payload is byte-identical, and the registry dial hands it to the very FIAS dial function it always used.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func validProtelRevision() Revision {
	return Revision{
		ID: "11111111-1111-4111-8111-111111111111", ConnectorKind: "protel-fias", Endpoint: "150.0.0.18:5003",
		SourceTimezone: "Africa/Cairo", ReadOnly: true, NormalizationVersion: 1,
		DialTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second,
		HeartbeatInterval: 30 * time.Second, HeartbeatTimeout: 90 * time.Second,
		FeedFreshnessBound: 120 * time.Second, CompleteSyncBound: 600 * time.Second,
		ResyncSupported: true, Published: true, CredentialMode: CredentialNone,
	}
}

func TestProtelRegression_StillASupportedKind(t *testing.T) {
	if _, ok := supportedConnectorKinds["protel-fias"]; !ok {
		t.Fatal("protel-fias dropped out of pmsd's supported kinds")
	}
	for _, legacy := range []string{"opera-fias", "fidelio-fias", "stub"} {
		if _, ok := supportedConnectorKinds[legacy]; ok {
			t.Fatalf("%q became supported without a pmsd adapter", legacy)
		}
	}
}

func TestProtelRegression_RevisionValidateOutcomesUnchanged(t *testing.T) {
	if err := validProtelRevision().Validate(); err != nil {
		t.Fatalf("a valid protel-fias revision is now refused: %v", err)
	}
	cases := map[string]func(*Revision){
		"unpublished":        func(r *Revision) { r.Published = false },
		"not read-only":      func(r *Revision) { r.ReadOnly = false },
		"no endpoint":        func(r *Revision) { r.Endpoint = "" },
		"NONE with a secret": func(r *Revision) { r.ActiveSecretGenerationID = "22222222-2222-4222-8222-222222222222" },
		"unknown kind":       func(r *Revision) { r.ConnectorKind = "opera-fias" },
		"zero timeout":       func(r *Revision) { r.HeartbeatTimeout = 0 },
		"bad timezone":       func(r *Revision) { r.SourceTimezone = "Mars/Olympus" },
	}
	for name, mut := range cases {
		r := validProtelRevision()
		mut(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted; it was refused before the registry existed", name)
		}
	}
}

// The exact bytes the FIAS path persisted before occupants could be carried. An Event with no sharers (every
// FIAS event) must marshal to precisely this.
func TestProtelRegression_PayloadBytesUnchanged(t *testing.T) {
	ev := Event{
		ReservationRef: "RES1", RoomNumber: "101", GuestLastName: "Doe", GuestFirstName: "Jane",
		FolioRef: "F9", ArrivalRaw: "260901", DepartureRaw: "260905", StayResolutionCandidate: "abc",
	}
	want := `{"reservation":"RES1","room":"101","last_name":"Doe","first_name":"Jane","folio":"F9",` +
		`"arrival_raw":"260901","departure_raw":"260905","stay_resolution_candidate":"abc"}`
	if got := string(eventPayloadJSON(ev)); got != want {
		t.Fatalf("FIAS payload changed:\n got %s\nwant %s", got, want)
	}
	ev.FolioRef, ev.ArrivalRaw = "", ""
	want = `{"reservation":"RES1","room":"101","last_name":"Doe","first_name":"Jane",` +
		`"departure_raw":"260905","stay_resolution_candidate":"abc"}`
	if got := string(eventPayloadJSON(ev)); got != want {
		t.Fatalf("FIAS payload omitempty behaviour changed:\n got %s\nwant %s", got, want)
	}
}

type spyConn struct{}

func (spyConn) Serve(context.Context, AxisSink) error { return nil }
func (spyConn) Close() error                          { return nil }

func TestProtelRegression_RegistryDialHandsProtelToTheFIASDial(t *testing.T) {
	var fiasCalls, restCalls int
	var got DialParams
	want := &spyConn{}
	fias := func(_ context.Context, p DialParams) (Conn, error) { fiasCalls++; got = p; return want, nil }
	rest := func(context.Context, DialParams) (Conn, error) {
		restCalls++
		return nil, errors.New("must not be called")
	}
	dial := NewRegistryDial(fias, rest)
	p := DialParams{Iface: Interface{ID: "x", ConnectorKind: "protel-fias"}, Rev: validProtelRevision()}
	c, err := dial(context.Background(), p)
	if err != nil || c != Conn(want) {
		t.Fatalf("protel-fias did not get the FIAS dial's own connection: %v %v", c, err)
	}
	if fiasCalls != 1 || restCalls != 0 {
		t.Fatalf("fias=%d rest=%d, want 1/0", fiasCalls, restCalls)
	}
	if got.Rev.Endpoint != p.Rev.Endpoint || got.Iface.ID != "x" {
		t.Fatal("the FIAS dial received altered parameters")
	}
}

func TestRegistryDial_RefusesAnUnregisteredKindBeforeAnyIO(t *testing.T) {
	called := false
	d := func(context.Context, DialParams) (Conn, error) { called = true; return nil, nil }
	rev := validProtelRevision()
	rev.ConnectorKind = "opera-fias"
	_, err := NewRegistryDial(d, d)(context.Background(), DialParams{Rev: rev})
	if err == nil || called {
		t.Fatal("an unregistered kind reached an adapter")
	}
	if Classify(err) != CodeRevisionInvalid {
		t.Fatalf("code = %s, want REVISION_INVALID", Classify(err))
	}
}

func TestProviderCodes_AreInTheBoundedVocabulary(t *testing.T) {
	for _, c := range []Code{CodeProviderAuth, CodeProviderRateLimited, CodeProviderTimeout,
		CodeProviderUnavailable, CodeProviderResponse, CodeProviderRejected} {
		if !c.Valid() {
			t.Fatalf("%s is not in codeSet", c)
		}
	}
}

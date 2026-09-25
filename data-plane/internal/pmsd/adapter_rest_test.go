package pmsd

// THE POLLED REST ADAPTER AGAINST THE SINK CONTRACT.
//
// A scripted provider client and a recording sink prove the adapter drives the SAME barrier / resync /
// publish / live-admission sequence the FIAS adapter drives: nothing LIVE before a published roster, a full
// roster staged under its own generation, GI for a check-in, GC for a change (room move), GO for a check-out,
// nothing for an unchanged re-read, and bounded failure behaviour.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsrest"
)

// ---------- recording sink ----------

type sinkEvent struct {
	staged bool
	ev     Event
}

type recSink struct {
	mu            sync.Mutex
	connected     int
	heartbeats    int
	required      int
	fullRequested int
	resyncStarts  int
	resyncDone    int
	resyncing     bool
	events        []sinkEvent
	coverage      [][]string
	covRoster     []int
	covVacant     []int
	skipped       []int64
	disconnected  []Code
	claims        []*ResyncCommand
	order         []string
}

func (s *recSink) log(x string) { s.order = append(s.order, x) }
func (s *recSink) OnConnected(time.Time) error {
	s.connected++
	s.log("connected")
	return nil
}
func (s *recSink) OnHeartbeat(time.Time) error { s.heartbeats++; return nil }
func (s *recSink) RequireInitialResync(time.Time) error {
	s.required++
	s.log("barrier")
	return nil
}
func (s *recSink) OnResyncStart(time.Time) error {
	s.resyncStarts++
	s.resyncing = true
	s.log("resync-start")
	return nil
}
func (s *recSink) OnResyncComplete(time.Time, string) error {
	s.resyncDone++
	s.resyncing = false
	s.log("resync-publish")
	return nil
}
func (s *recSink) OnDisconnected(_ time.Time, c Code) error {
	s.disconnected = append(s.disconnected, c)
	return nil
}
func (s *recSink) OnDomainEvent(_ context.Context, ev Event) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	s.events = append(s.events, sinkEvent{staged: s.resyncing, ev: ev})
	return nil
}
func (s *recSink) OnContinuityFault(context.Context, Code) error { return nil }
func (s *recSink) ClaimOperatorResync() *ResyncCommand {
	if len(s.claims) == 0 {
		return nil
	}
	c := s.claims[0]
	s.claims = s.claims[1:]
	return c
}
func (s *recSink) RecordSkipped(n int64) { s.skipped = append(s.skipped, n) }
func (s *recSink) RecordCoverage(rooms []string, roster, vacant, _ int) {
	s.coverage = append(s.coverage, rooms)
	s.covRoster = append(s.covRoster, roster)
	s.covVacant = append(s.covVacant, vacant)
}
func (s *recSink) OnFullSyncRequested() { s.fullRequested++ }

func (s *recSink) live() []Event {
	var out []Event
	for _, e := range s.events {
		if !e.staged {
			out = append(out, e.ev)
		}
	}
	return out
}

// ---------- scripted provider client ----------

type fakeClient struct {
	probeErr      error
	snapshots     []pmsrest.Snapshot
	snapErr       error
	changes       [][]pmsrest.Reservation
	changeErrs    []error
	lookups       map[string]pmsrest.Reservation
	noChangeFeed  bool
	changeWindows [][2]time.Time
	lookedUp      [][]string
}

func (f *fakeClient) Probe(context.Context) (pmsrest.Probe, error) {
	return pmsrest.Probe{}, f.probeErr
}
func (f *fakeClient) Snapshot(context.Context) (pmsrest.Snapshot, error) {
	if f.snapErr != nil {
		return pmsrest.Snapshot{}, f.snapErr
	}
	s := f.snapshots[0]
	if len(f.snapshots) > 1 {
		f.snapshots = f.snapshots[1:]
	}
	return s, nil
}
func (f *fakeClient) SupportsChanges() bool { return !f.noChangeFeed }
func (f *fakeClient) Changes(_ context.Context, since, until time.Time) ([]pmsrest.Reservation, error) {
	f.changeWindows = append(f.changeWindows, [2]time.Time{since, until})
	if len(f.changeErrs) > 0 {
		e := f.changeErrs[0]
		f.changeErrs = f.changeErrs[1:]
		if e != nil {
			return nil, e
		}
	}
	if len(f.changes) == 0 {
		return nil, nil
	}
	c := f.changes[0]
	f.changes = f.changes[1:]
	return c, nil
}
func (f *fakeClient) Lookup(_ context.Context, ids []string) ([]pmsrest.Reservation, error) {
	f.lookedUp = append(f.lookedUp, ids)
	var out []pmsrest.Reservation
	for _, id := range ids {
		if r, ok := f.lookups[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---------- harness ----------

const (
	tIface = "6d5f0000-0000-4000-8000-00000000a001"
	tTen   = "6d5f0000-0000-4000-8000-00000000a002"
	tSite  = "6d5f0000-0000-4000-8000-00000000a003"
	tRev   = "6d5f0000-0000-4000-8000-00000000a004"
	tSG    = "6d5f0000-0000-4000-8000-00000000a005"
)

func restRevision(kind string) Revision {
	return Revision{
		ID: tRev, ConnectorKind: kind, Endpoint: "https://api.mews.com", SourceTimezone: "Europe/Prague",
		ReadOnly: true, NormalizationVersion: 1, DialTimeout: 30 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, HeartbeatInterval: time.Minute, HeartbeatTimeout: 3 * time.Minute,
		FeedFreshnessBound: 3 * time.Minute, CompleteSyncBound: time.Hour, ResyncSupported: true, Published: true,
		CredentialMode: CredentialAuthKey, ActiveSecretGenerationID: tSG,
		ProviderConfig: []byte(map[string]string{
			"mews": `{"platform_url":"https://api.mews.com","poll_interval_seconds":60,` +
				`"request_timeout_seconds":30,"full_resync_minutes":60}`,
			"opera-cloud": `{"gateway_url":"https://gw.example.com","hotel_id":"HOTEL1","scope":"s",` +
				`"poll_interval_seconds":60,"request_timeout_seconds":30,"full_resync_minutes":60}`,
			"apaleo": `{"api_url":"https://api.apaleo.com","identity_url":"https://identity.apaleo.com/connect/token",` +
				`"property_id":"MUC","poll_interval_seconds":60,"request_timeout_seconds":30,"full_resync_minutes":60}`,
		}[kind]),
	}
}

// runREST dials a REST conn with the scripted client and serves it. steps[i] runs before poll i+1; when the
// steps are exhausted the context is cancelled.
func runREST(t *testing.T, fc *fakeClient, sink *recSink, rev Revision, steps ...func(now time.Time)) error {
	t.Helper()
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poll := 0
	dial := NewRESTDial(RESTDialDeps{
		Keys: AdapterKeys{IdentityKey: []byte("identity-key-32-bytes-long-xxxxx"), IdentityKeyVersion: 1,
			EvidenceKey: []byte("evidence-key-32-bytes-long-xxxxx"), EvidenceKeyVersion: 1},
		Now: func() time.Time { return now },
		NewClient: func(kind string, provider map[string]any, secret []byte, _ pmsrest.Options) (pmsrest.Client, error) {
			if provider["platform_url"] == nil && kind == "mews" {
				t.Errorf("provider config not parsed: %v", provider)
			}
			return fc, nil
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			if poll >= len(steps) {
				cancel()
				return ctx.Err()
			}
			now = now.Add(d)
			steps[poll](now)
			poll++
			return nil
		},
	})
	conn, err := dial(ctx, DialParams{
		Iface:  Interface{TenantID: tTen, SiteID: tSite, ID: tIface, ConnectorKind: rev.ConnectorKind, LifecycleState: "ACTIVE"},
		Rev:    rev,
		Secret: NewSecretMaterial([]byte(`{"client_token":"x","access_token":"y"}`)),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn.Serve(ctx, sink)
}

func res(id, room, last string, st pmsrest.State, stamp string) pmsrest.Reservation {
	return pmsrest.Reservation{ID: id, Room: room, LastName: last, FirstName: "F", Arrival: "260923",
		Departure: "260927", State: st, Stamp: stamp}
}

// ---------- tests ----------

func TestRESTAdapter_RefusedCredentialNeverReportsConnected(t *testing.T) {
	sink := &recSink{}
	err := runREST(t, &fakeClient{probeErr: &pmsrest.Error{Kind: pmsrest.KindAuth, Status: 401}}, sink, restRevision("mews"))
	if Classify(err) != CodeProviderAuth {
		t.Fatalf("code %s, want PROVIDER_AUTH_FAILED", Classify(err))
	}
	if sink.connected != 0 || sink.required != 0 || len(sink.events) != 0 {
		t.Fatalf("a refused credential reached CONNECTED/the barrier: %+v", sink)
	}
}

func TestRESTAdapter_InitialFullSyncStagesThenPublishes(t *testing.T) {
	fc := &fakeClient{snapshots: []pmsrest.Snapshot{{
		Reservations: []pmsrest.Reservation{
			res("A", "101", "Doe", pmsrest.StateInHouse, "s1"),
			res("B", "", "NoRoom", pmsrest.StateInHouse, "s1"), // no room assigned: skipped, counted
			res("A", "101", "Doe", pmsrest.StateInHouse, "s1"), // duplicate across pages: one row
		},
		Rooms: []string{"101", "102", "103"}, RoomsKnown: true,
	}}}
	sink := &recSink{}
	err := runREST(t, fc, sink, restRevision("mews"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("serve ended with %v", err)
	}
	wantOrder := []string{"connected", "barrier", "resync-start", "resync-publish"}
	if !reflect.DeepEqual(sink.order, wantOrder) {
		t.Fatalf("order %v, want %v", sink.order, wantOrder)
	}
	if len(sink.events) != 1 || !sink.events[0].staged || sink.events[0].ev.RecordType != RecGI {
		t.Fatalf("want exactly one STAGED GI, got %+v", sink.events)
	}
	ev := sink.events[0].ev
	if ev.ReservationRef != "A" || ev.RoomNumber != "101" || ev.GuestLastName != "Doe" || ev.ArrivalRaw != "260923" {
		t.Fatalf("event %+v", ev)
	}
	if !reflect.DeepEqual(sink.coverage, [][]string{{"101", "102", "103"}}) || sink.covRoster[0] != 1 || sink.covVacant[0] != 2 {
		t.Fatalf("coverage %v roster %v vacant %v", sink.coverage, sink.covRoster, sink.covVacant)
	}
	if !reflect.DeepEqual(sink.skipped, []int64{1}) {
		t.Fatalf("skipped %v", sink.skipped)
	}
	if sink.heartbeats != 1 || sink.fullRequested != 1 {
		t.Fatalf("heartbeats=%d fullRequested=%d", sink.heartbeats, sink.fullRequested)
	}
}

func TestRESTAdapter_NoCoverageWithoutRoomEnumeration(t *testing.T) {
	fc := &fakeClient{snapshots: []pmsrest.Snapshot{{Reservations: []pmsrest.Reservation{
		res("A", "101", "Doe", pmsrest.StateInHouse, "s1")}}}}
	sink := &recSink{}
	_ = runREST(t, fc, sink, restRevision("opera-cloud"))
	if len(sink.coverage) != 0 {
		t.Fatal("a provider that cannot enumerate rooms must record no coverage (reconciliation then refuses)")
	}
}

func TestRESTAdapter_LiveCheckInRoomMoveCheckOut(t *testing.T) {
	fc := &fakeClient{
		snapshots: []pmsrest.Snapshot{{Reservations: []pmsrest.Reservation{
			res("A", "101", "Doe", pmsrest.StateInHouse, "s1"),
			res("B", "102", "Roe", pmsrest.StateInHouse, "s1"),
		}, RoomsKnown: true, Rooms: []string{"101", "102", "103", "104"}}},
		changes: [][]pmsrest.Reservation{
			{ // poll 1: a new check-in, a room move, an unchanged re-read
				res("C", "103", "Poe", pmsrest.StateInHouse, "s2"),
				res("A", "104", "Doe", pmsrest.StateInHouse, "s2"),
				res("B", "102", "Roe", pmsrest.StateInHouse, "s1"),
			},
			{ // poll 2: A checks out between polls; B leaves the house without a check-out
				res("A", "", "Doe", pmsrest.StateCheckedOut, "s3"),
				res("B", "102", "Roe", pmsrest.StateOther, "s3"),
				res("Z", "105", "Never", pmsrest.StateCheckedOut, "s3"), // never known: nothing to close
			},
			{ // poll 3: A's check-out re-read in the overlap window: already closed
				res("A", "", "Doe", pmsrest.StateCheckedOut, "s3"),
			},
		},
	}
	sink := &recSink{}
	_ = runREST(t, fc, sink, restRevision("mews"), func(time.Time) {}, func(time.Time) {}, func(time.Time) {})
	live := sink.live()
	got := []string{}
	for _, e := range live {
		got = append(got, string(e.RecordType)+":"+e.ReservationRef+":"+e.RoomNumber)
	}
	want := []string{"GI:C:103", "GC:A:104", "GO:A:104"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("live events %v, want %v (GO must name the room the guest last occupied)", got, want)
	}
	if sink.heartbeats != 4 {
		t.Fatalf("every successful poll is a keep-alive: heartbeats=%d", sink.heartbeats)
	}
	// the change window overlaps the previous one, never leaves a gap
	for i := 1; i < len(fc.changeWindows); i++ {
		if !fc.changeWindows[i][0].Before(fc.changeWindows[i-1][1]) {
			t.Fatalf("change windows leave a gap: %v", fc.changeWindows)
		}
	}
}

func TestRESTAdapter_DiffModeConfirmsCheckOutByLookup(t *testing.T) {
	fc := &fakeClient{
		noChangeFeed: true,
		snapshots: []pmsrest.Snapshot{
			{Reservations: []pmsrest.Reservation{res("A", "101", "Doe", pmsrest.StateInHouse, "s1"), res("B", "102", "Roe", pmsrest.StateInHouse, "s1")}},
			{Reservations: []pmsrest.Reservation{res("C", "103", "Poe", pmsrest.StateInHouse, "s2")}},
		},
		lookups: map[string]pmsrest.Reservation{"A": res("A", "101", "Doe", pmsrest.StateCheckedOut, "s3")},
	}
	sink := &recSink{}
	_ = runREST(t, fc, sink, restRevision("opera-cloud"), func(time.Time) {})
	got := []string{}
	for _, e := range sink.live() {
		got = append(got, string(e.RecordType)+":"+e.ReservationRef)
	}
	if !reflect.DeepEqual(got, []string{"GI:C", "GO:A"}) {
		t.Fatalf("live %v: A confirmed checked out -> GO; B unconfirmed -> nothing invented", got)
	}
	if len(fc.lookedUp) != 1 || !reflect.DeepEqual(fc.lookedUp[0], []string{"A", "B"}) {
		t.Fatalf("lookups %v", fc.lookedUp)
	}
}

func TestRESTAdapter_TransientFailuresToleratedUntilHeartbeatTimeout(t *testing.T) {
	unavailable := &pmsrest.Error{Kind: pmsrest.KindUnavailable, Status: 503}
	fc := &fakeClient{
		snapshots:  []pmsrest.Snapshot{{Reservations: []pmsrest.Reservation{res("A", "101", "Doe", pmsrest.StateInHouse, "s1")}}},
		changeErrs: []error{unavailable, unavailable, unavailable, unavailable},
	}
	sink := &recSink{}
	nop := func(time.Time) {}
	err := runREST(t, fc, sink, restRevision("mews"), nop, nop, nop, nop, nop)
	if Classify(err) != CodeProviderUnavailable {
		t.Fatalf("ended with %v (%s)", err, Classify(err))
	}
	if len(fc.changeWindows) != 4 {
		t.Fatalf("polls attempted %d: three failures fit inside a 3-minute keep-alive timeout, the fourth ends the cycle", len(fc.changeWindows))
	}
	if !reflect.DeepEqual(sink.disconnected, []Code{CodeProviderUnavailable}) {
		t.Fatalf("disconnect codes %v", sink.disconnected)
	}
}

func TestRESTAdapter_RevokedCredentialMidFeedEndsAtOnce(t *testing.T) {
	fc := &fakeClient{
		snapshots:  []pmsrest.Snapshot{{}},
		changeErrs: []error{&pmsrest.Error{Kind: pmsrest.KindAuth, Status: 401}},
	}
	sink := &recSink{}
	err := runREST(t, fc, sink, restRevision("mews"), func(time.Time) {}, func(time.Time) {})
	if Classify(err) != CodeProviderAuth || len(fc.changeWindows) != 1 {
		t.Fatalf("err %v polls %d", err, len(fc.changeWindows))
	}
}

func TestRESTAdapter_PeriodicAndOperatorFullResync(t *testing.T) {
	snap := pmsrest.Snapshot{Reservations: []pmsrest.Reservation{res("A", "101", "Doe", pmsrest.StateInHouse, "s1")}}
	fc := &fakeClient{snapshots: []pmsrest.Snapshot{snap}}
	sink := &recSink{}
	rev := restRevision("mews")
	rev.CompleteSyncBound = 2 * time.Minute
	_ = runREST(t, fc, sink, rev,
		func(time.Time) {}, // +1m: incremental
		func(time.Time) {}, // +2m: complete-sync bound reached -> full sync
		func(time.Time) { sink.claims = append(sink.claims, &ResyncCommand{ID: "cmd"}) }, // operator
	)
	if sink.resyncStarts != 3 || sink.resyncDone != 3 {
		t.Fatalf("resyncs started=%d published=%d, want 3/3 (initial, periodic, operator)", sink.resyncStarts, sink.resyncDone)
	}
	if len(sink.live()) != 0 {
		t.Fatal("an unchanged roster re-read must not admit LIVE events")
	}
	staged := 0
	for _, e := range sink.events {
		if e.staged {
			staged++
		}
	}
	if staged != 3 {
		t.Fatalf("each full sync stages the complete roster under its own generation: staged=%d", staged)
	}
}

func TestRESTAdapter_EventIdentityAndSharersPayload(t *testing.T) {
	r := res("A", "101", "Doe", pmsrest.StateInHouse, "s1")
	r.Sharers = []pmsrest.Guest{{ExternalID: "g1", FirstName: "Jane", LastName: "Doe", Primary: true}, {ExternalID: "g2", FirstName: "Jon", LastName: "Doe"}}
	fc := &fakeClient{snapshots: []pmsrest.Snapshot{{Reservations: []pmsrest.Reservation{r}}}}
	sink := &recSink{}
	_ = runREST(t, fc, sink, restRevision("mews"))
	ev := sink.events[0].ev
	if ev.SecretGenerationID != tSG || ev.RevisionID != tRev || ev.Cursor != "A:101" || ev.PMSEventAt != nil {
		t.Fatalf("provenance %+v", ev)
	}
	var p map[string]any
	if err := json.Unmarshal(eventPayloadJSON(ev), &p); err != nil {
		t.Fatal(err)
	}
	sh, _ := p["sharers"].([]any)
	if len(sh) != 2 || sh[0].(map[string]any)["is_primary"] != true || sh[1].(map[string]any)["external_guest_id"] != "g2" {
		t.Fatalf("sharers payload %v", p["sharers"])
	}
	// a later version of the same reservation is a different source event
	c := &restConn{iface: Interface{ID: tIface}, rev: restRevision("mews"), identKey: []byte("k"), identKeyN: 1,
		evKey: []byte("e"), evKeyN: 1, profile: "mews/rest-v1", now: time.Now}
	e1, _ := c.event(RecGI, res("A", "101", "Doe", pmsrest.StateInHouse, "s1"), "101")
	e2, _ := c.event(RecGI, res("A", "101", "Doe", pmsrest.StateInHouse, "s1"), "101")
	e3, _ := c.event(RecGI, res("A", "101", "Doe", pmsrest.StateInHouse, "s2"), "101")
	if e1.ExternalEventIdentity != e2.ExternalEventIdentity || e1.ExternalEventIdentity == e3.ExternalEventIdentity {
		t.Fatal("identity must be stable for the same version and differ for a new version")
	}
}

func TestRESTAdapter_OverlongValueSkippedNotFaulted(t *testing.T) {
	fc := &fakeClient{snapshots: []pmsrest.Snapshot{{Reservations: []pmsrest.Reservation{
		res("A", strings.Repeat("9", 40), "Doe", pmsrest.StateInHouse, "s1"),
	}}}}
	sink := &recSink{}
	_ = runREST(t, fc, sink, restRevision("mews"))
	if len(sink.events) != 0 || !reflect.DeepEqual(sink.skipped, []int64{1}) || sink.resyncDone != 1 {
		t.Fatalf("events=%d skipped=%v published=%d", len(sink.events), sink.skipped, sink.resyncDone)
	}
}

func TestRESTDial_FailsClosed(t *testing.T) {
	d := NewRESTDial(RESTDialDeps{})
	if _, err := d(context.Background(), DialParams{Rev: restRevision("mews")}); Classify(err) != CodeConfigInvalid {
		t.Fatalf("missing keys: %v", err)
	}
	keys := AdapterKeys{IdentityKey: []byte("i"), IdentityKeyVersion: 1, EvidenceKey: []byte("e"), EvidenceKeyVersion: 1}
	d = NewRESTDial(RESTDialDeps{Keys: keys})
	rev := restRevision("mews")
	rev.ProviderConfig = []byte(`{"platform_url":"http://insecure","poll_interval_seconds":60,"request_timeout_seconds":30,"full_resync_minutes":60}`)
	if _, err := d(context.Background(), DialParams{Rev: rev}); Classify(err) != CodeConfigInvalid {
		t.Fatalf("plain-http provider config must be refused at dial: %v", err)
	}
	rev = restRevision("protel-fias")
	if _, err := d(context.Background(), DialParams{Rev: rev}); Classify(err) != CodeRevisionInvalid {
		t.Fatalf("the REST dial must refuse a socket kind: %v", err)
	}
}

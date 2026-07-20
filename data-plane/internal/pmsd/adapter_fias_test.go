package pmsd

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// recordingSink captures axis calls + domain events for protocol assertions.
type recordingSink struct {
	mu             sync.Mutex
	connected      int
	heartbeats     int
	resyncStart    int
	resyncComplete int
	events         []Event
	overflow       bool
	continuityFlt  int
	initialResync  int
	q              *BoundedQueue
}

func (s *recordingSink) OnConnected(time.Time) error {
	s.mu.Lock()
	s.connected++
	s.mu.Unlock()
	return nil
}
func (s *recordingSink) OnHeartbeat(time.Time) error {
	s.mu.Lock()
	s.heartbeats++
	s.mu.Unlock()
	return nil
}
func (s *recordingSink) RequireInitialResync(time.Time) error {
	s.mu.Lock()
	s.initialResync++
	s.mu.Unlock()
	return nil
}
func (s *recordingSink) OnResyncStart(time.Time) error {
	s.mu.Lock()
	s.resyncStart++
	s.mu.Unlock()
	return nil
}
func (s *recordingSink) OnResyncComplete(time.Time, string) error {
	s.mu.Lock()
	s.resyncComplete++
	s.mu.Unlock()
	return nil
}
func (s *recordingSink) OnDisconnected(time.Time, Code) error { return nil }
func (s *recordingSink) OnDomainEvent(ctx context.Context, ev Event) error {
	if s.q != nil {
		if err := s.q.Enqueue(ctx, ev); err != nil {
			if Classify(err) == CodeQueueOverflow {
				s.mu.Lock()
				s.overflow = true
				s.mu.Unlock()
			}
			return err
		}
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	return nil
}
func (s *recordingSink) OnContinuityFault(ctx context.Context, code Code) error {
	s.mu.Lock()
	s.continuityFlt++
	s.mu.Unlock()
	return nil
}

func testRev() Revision {
	r := validRevision()
	r.HeartbeatTimeout = 2 * time.Second
	r.WriteTimeout = time.Second
	return r
}

func testKeys() AdapterKeys {
	return AdapterKeys{
		IdentityKey: []byte("identity-key-0123456789abcdef01"), IdentityKeyVersion: 3,
		EvidenceKey: []byte("evidence-key-0123456789abcdef01"), EvidenceKeyVersion: 2,
	}
}

func newAdapterOverPipe(t *testing.T) (*fiasAdapter, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	dialer := func(ctx context.Context, network, address string) (net.Conn, error) { return client, nil }
	dial := NewFIASDial(dialer, testKeys(), time.Now)
	conn, err := dial(context.Background(), DialParams{Iface: iface("i1"), Rev: testRev()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn.(*fiasAdapter), server
}

func testAdapter() *fiasAdapter {
	k := testKeys()
	return &fiasAdapter{iface: iface("i1"), rev: validRevision(), evKey: k.EvidenceKey, evKeyNo: k.EvidenceKeyVersion,
		identKey: k.IdentityKey, identKeyN: k.IdentityKeyVersion, profile: "protel-fias/v1", now: time.Now}
}

// mustEvent parses a record that is expected to be strictly well-formed, failing the test on a parse error.
// (Validation of the typed Event contract is asserted separately by callers.)
func mustEvent(t *testing.T, a *fiasAdapter, rec string) Event {
	t.Helper()
	ev, err := a.toEvent(rec)
	if err != nil {
		t.Fatalf("toEvent(%q) unexpected strict-parse error: %v", rec, err)
	}
	return ev
}

// TestFIASFieldMap_Authoritative pins the binding Protel FIAS field map + timestamp semantics without any
// network: RN=room, G#=reservation, GN/GF=last/first, GA/GD=arrival/departure.
func TestFIASFieldMap_Authoritative(t *testing.T) {
	a := testAdapter()
	gi := mustEvent(t, a, "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|")
	if gi.RoomNumber != "1408" || gi.ReservationRef != "12345" || gi.GuestLastName != "Smith" ||
		gi.GuestFirstName != "John" || gi.ArrivalRaw != "260101" || gi.DepartureRaw != "260105" {
		t.Fatalf("authoritative map wrong: %+v", gi)
	}
	if err := gi.Validate(); err != nil {
		t.Fatalf("valid GI must validate: %v", err)
	}
	// §3 timestamp semantics: Arrival Date must NEVER be reported as PMS event time; ReceivedAt is separate.
	if gi.PMSEventTimestampRaw != "" || gi.PMSEventAt != nil {
		t.Errorf("GI has no verified event timestamp; PMSEvent* must be unavailable, got %q/%v", gi.PMSEventTimestampRaw, gi.PMSEventAt)
	}
	if gi.ArrivalRaw == gi.PMSEventTimestampRaw && gi.ArrivalRaw != "" {
		t.Error("arrival date must not be substituted as PMS event time")
	}
	if gi.ReceivedAt.IsZero() {
		t.Error("ReceivedAt (local receipt clock) must be set")
	}
}

// TestSourceFingerprint_DuplicateAndDistinct proves exact retransmission is idempotent and any meaningful
// change is distinct (§1/§2).
func TestSourceFingerprint_DuplicateAndDistinct(t *testing.T) {
	a := testAdapter()
	base := "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|"
	fp := func(rec string) string { return mustEvent(t, a, rec).SourceEventFingerprint }
	baseFP := fp(base)
	if !isHex64(baseFP) {
		t.Fatalf("fingerprint must be 64-hex, got %q", baseFP)
	}
	// exact retransmission -> SAME (idempotent)
	if fp(base) != baseFP {
		t.Error("exact retransmission must produce the same fingerprint")
	}
	// reservation alone is not the identity
	if baseFP == mustEvent(t, a, base).ReservationRef {
		t.Error("reservation number alone must not be the fingerprint")
	}
	distinct := map[string]string{
		"GI->GC (record type)":         "GC|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|",
		"guest last-name-only change":  "GI|RN1408|G#12345|GNSmithe|GFJohn|GA260101|GD260105|",
		"guest first-name-only change": "GI|RN1408|G#12345|GNSmith|GFJon|GA260101|GD260105|",
		"folio-only change":            "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|FO9001|",
		"room move (RN change)":        "GI|RN1500|G#12345|GNSmith|GFJohn|GA260101|GD260105|",
		"arrival change (episode)":     "GI|RN1408|G#12345|GNSmith|GFJohn|GA260601|GD260605|",
		"departure change":             "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260106|",
	}
	for label, rec := range distinct {
		if fp(rec) == baseFP {
			t.Errorf("%s must produce a DIFFERENT fingerprint", label)
		}
	}
	// interface change -> different
	a2 := testAdapter()
	a2.iface = iface("i2")
	if mustEvent(t, a2, base).SourceEventFingerprint == baseFP {
		t.Error("interface change must produce a different fingerprint")
	}
	// normalization-version change -> different
	a3 := testAdapter()
	a3.rev.NormalizationVersion = 2
	if mustEvent(t, a3, base).SourceEventFingerprint == baseFP {
		t.Error("normalization-version change must produce a different fingerprint")
	}
	// present-but-empty vs absent must not collide (field-boundary ambiguity)
	emptyGN := mustEvent(t, a, "GI|RN1408|G#12345|GN|GFJohn|GA260101|GD260105|")
	absentGN := mustEvent(t, a, "GI|RN1408|G#12345|GFJohn|GA260101|GD260105|")
	if emptyGN.SourceEventFingerprint == absentGN.SourceEventFingerprint {
		t.Error("present-but-empty field must not collide with an absent field")
	}
}

func TestSourceFingerprint_EmptyKeyFailsClosed(t *testing.T) {
	fp, ver := ComputeSourceFingerprint(nil, 1, newSourceEvent("i", RecGI, 1, "p", "", nil))
	if fp != "" || ver != 0 {
		t.Error("empty identity key must fail closed (empty fingerprint)")
	}
	fp, ver = ComputeSourceFingerprint([]byte("k"), 0, newSourceEvent("i", RecGI, 1, "p", "", nil))
	if fp != "" || ver != 0 {
		t.Error("zero key version must fail closed")
	}
	// an event built with no identity key is rejected by Validate (domain event needs a 64-hex fingerprint)
	a := testAdapter()
	a.identKey = nil
	if mustEvent(t, a, "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|").Validate() == nil {
		t.Error("domain event without a fingerprint must be rejected")
	}
}

// TestStayResolutionCandidate_NonAuthoritative proves the connector's stay-resolution HINT is scoped by
// reservation ONLY (room and arrival excluded), is distinct from the event fingerprint, and is NOT required
// by Validate — the authoritative Stay/lifecycle resolution is Increment 4's job.
func TestStayResolutionCandidate_NonAuthoritative(t *testing.T) {
	a := testAdapter()
	in := mustEvent(t, a, "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|")
	move := mustEvent(t, a, "GC|RN1500|G#12345|GNSmith|GFJohn|GA260101|GD260105|")   // same reservation, new room
	arrFix := mustEvent(t, a, "GC|RN1408|G#12345|GNSmith|GFJohn|GA260102|GD260105|") // same reservation, corrected arrival
	if in.StayResolutionCandidate != move.StayResolutionCandidate {
		t.Error("a Room Move (room excluded) must keep the same candidate")
	}
	if in.StayResolutionCandidate != arrFix.StayResolutionCandidate {
		t.Error("an arrival correction (arrival excluded) must keep the same candidate")
	}
	if in.SourceEventFingerprint == move.SourceEventFingerprint {
		t.Error("a Room Move must still be a DISTINCT event fingerprint")
	}
	if in.StayResolutionCandidate == in.SourceEventFingerprint {
		t.Error("the candidate must be distinct from the event fingerprint")
	}
	// different reservation -> different candidate
	other := mustEvent(t, a, "GI|RN1408|G#99999|GNSmith|GFJohn|GA260101|GD260105|")
	if other.StayResolutionCandidate == in.StayResolutionCandidate {
		t.Error("a different reservation must be a different candidate")
	}
	// the candidate is NOT a mandatory identity: an otherwise-valid event with no candidate still validates
	ev := in
	ev.StayResolutionCandidate = ""
	if err := ev.Validate(); err != nil {
		t.Errorf("StayResolutionCandidate must not be a mandatory identity: %v", err)
	}
}

func TestFIAS_MalformedAndOverlong(t *testing.T) {
	a := testAdapter()
	// missing G# (reservation) -> strict typed projection rejects (no Event produced)
	if _, err := a.toEvent("GI|RN1408|GNSmith|GA260101|"); err == nil {
		t.Error("GI without reservation (G#) must be rejected")
	}
	// missing RN (room) -> rejected
	if _, err := a.toEvent("GI|G#12345|GNSmith|GA260101|"); err == nil {
		t.Error("GI without room (RN) must be rejected")
	}
}

// TestFIAS_OverlongFieldsRejectedNoTruncation proves §B: EVERY identity/evidence-bearing FIAS field, when
// overlong, causes the record to be REJECTED (never silently truncated into a possibly-colliding value), and
// that the full untruncated value the adapter carried is what the validator sees — so no shortened value can
// slip through. It also drives the rejected event through the real BoundedQueue to prove no truncated (or
// any) value reaches the queue/inbox: Enqueue returns EVENT_INVALID and capacity is untouched.
func TestFIAS_OverlongFieldsRejectedNoTruncation(t *testing.T) {
	a := testAdapter()
	// base valid record (all required fields present, within bounds)
	base := func() map[string]string {
		return map[string]string{"RN": "1408", "G#": "12345", "GN": "Smith", "GF": "John", "FO": "F900", "GA": "260101", "GD": "260105"}
	}
	build := func(f map[string]string) string {
		return "GI|RN" + f["RN"] + "|G#" + f["G#"] + "|GN" + f["GN"] + "|GF" + f["GF"] + "|FO" + f["FO"] + "|GA" + f["GA"] + "|GD" + f["GD"] + "|"
	}
	// sanity: the base record is a VALID event (so each failure below is attributable to the one overlong field)
	if err := mustEvent(t, a, build(base())).Validate(); err != nil {
		t.Fatalf("base record must be valid, got %v", err)
	}

	cases := []struct {
		field string
		limit int
		get   func(Event) string
	}{
		{"RN", maxRoomLen, func(e Event) string { return e.RoomNumber }},
		{"G#", maxReservationLen, func(e Event) string { return e.ReservationRef }},
		{"GN", maxGuestLen, func(e Event) string { return e.GuestLastName }},
		{"GF", maxGuestLen, func(e Event) string { return e.GuestFirstName }},
		{"FO", maxFolioLen, func(e Event) string { return e.FolioRef }},
		{"GA", maxRawTimestampLen, func(e Event) string { return e.ArrivalRaw }},
		{"GD", maxRawTimestampLen, func(e Event) string { return e.DepartureRaw }},
	}
	for _, tc := range cases {
		f := base()
		overlong := strings.Repeat("9", tc.limit+7)
		f[tc.field] = overlong
		ev := mustEvent(t, a, build(f))

		// 1) the adapter carried the FULL value untruncated (no silent clip inside toEvent)
		if got := tc.get(ev); got != overlong {
			t.Errorf("%s: adapter truncated the value (len %d != %d) — silent truncation", tc.field, len(got), len(overlong))
		}
		// 2) validation REJECTS it
		if ev.Validate() == nil {
			t.Errorf("%s: overlong value must be rejected, not accepted", tc.field)
		}
		// 3) it cannot reach the queue/inbox: Enqueue returns EVENT_INVALID and consumes zero capacity
		q := NewBoundedQueue(4, time.Second)
		if err := q.Enqueue(context.Background(), ev); Classify(err) != CodeEventInvalid {
			t.Errorf("%s: enqueue of overlong record = %q, want EVENT_INVALID", tc.field, Classify(err))
		}
		if q.Len() != 0 {
			t.Errorf("%s: a truncated/overlong record reached the queue (len=%d)", tc.field, q.Len())
		}
	}
}

// TestWriteChokepoint_BlocksForbiddenAllowsReadOnly proves the guarded writer rejects PS/PA (and unknown
// records) BEFORE any byte is written, and passes the verified read-only records.
func TestWriteChokepoint_BlocksForbiddenAllowsReadOnly(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	g := &guardedConn{c: client, writeTimeout: 200 * time.Millisecond}

	for _, bad := range []string{"PS|amount|100", "PA|ack|1", "ZZ|unknown|1"} {
		got := make(chan int, 1)
		go func() { // any byte written would unblock this read
			buf := make([]byte, 64)
			_ = server.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			n, _ := server.Read(buf)
			got <- n
		}()
		if err := g.writeFrame(bad); err == nil {
			t.Fatalf("writeFrame(%q) must be blocked", bad)
		} else if Classify(err) != CodeOutboundBlocked {
			t.Fatalf("blocked write should be OUTBOUND_FRAME_BLOCKED, got %q", Classify(err))
		}
		select {
		case n := <-got:
			if n > 0 {
				t.Fatalf("blocked record %q wrote %d bytes to the peer (must be zero)", bad, n)
			}
		case <-time.After(300 * time.Millisecond):
			// timed out with no bytes → zero bytes written (correct)
		}
	}

	// an allowed record reaches the peer intact
	done := make(chan string, 1)
	go func() {
		_ = server.SetReadDeadline(time.Time{}) // clear the expired deadline left by the blocked-write loop
		br := bufio.NewReader(server)
		body, _ := pms.ReadFramedRecord(br)
		done <- body
	}()
	if err := g.writeFrame(pms.BuildLA()); err != nil {
		t.Fatalf("allowed LA must write: %v", err)
	}
	select {
	case body := <-done:
		if pms.RecordID(body) != "LA" {
			t.Fatalf("peer received %q, want LA", body)
		}
	case <-time.After(time.Second):
		t.Fatal("allowed LA never reached peer")
	}
}

// TestAdapter_StartupDomainAndResync drives a fake PMS peer over net.Pipe and asserts the read-only
// handshake, typed domain events (no raw frame bytes), resync transitions, and heartbeat.
func TestAdapter_StartupDomainAndResync(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	sink := &recordingSink{q: NewBoundedQueue(16, time.Second)}

	var wrote []string
	var mu sync.Mutex
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		br := bufio.NewReader(server)
		// read the 5 startup frames (LS, LD, LR x3)
		for i := 0; i < 5; i++ {
			body, err := pms.ReadFramedRecord(br)
			if err != nil {
				return
			}
			mu.Lock()
			wrote = append(wrote, pms.RecordID(body))
			mu.Unlock()
		}
		// send a guest-in (AUTHORITATIVE map: RN=room, G#=reservation, GN/GF=last/first, GA/GD dates),
		// a resync window, and a heartbeat
		for _, rec := range []string{
			"GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|",
			"DS|",
			"DE|",
			"LA|",
		} {
			if err := pms.WriteFramedRecord(server, rec); err != nil {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
		_ = server.Close() // link end → adapter returns
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = adapter.Serve(ctx, sink)
	<-serverDone

	mu.Lock()
	startup := append([]string(nil), wrote...)
	mu.Unlock()
	if len(startup) < 5 || startup[0] != "LS" || startup[1] != "LD" || startup[2] != "LR" {
		t.Fatalf("startup handshake wrong: %v", startup)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.connected != 1 {
		t.Errorf("OnConnected = %d, want 1", sink.connected)
	}
	if sink.resyncStart != 1 || sink.resyncComplete != 1 {
		t.Errorf("resync start/complete = %d/%d, want 1/1", sink.resyncStart, sink.resyncComplete)
	}
	if sink.heartbeats < 1 {
		t.Errorf("expected >=1 heartbeat, got %d", sink.heartbeats)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 domain event, got %d", len(sink.events))
	}
	e := sink.events[0]
	// AUTHORITATIVE Protel map: RN=room(1408), G#=reservation(12345), GN/GF=last/first, GA/GD dates
	if e.RecordType != RecGI || e.RoomNumber != "1408" || e.ReservationRef != "12345" ||
		e.GuestLastName != "Smith" || e.GuestFirstName != "John" || e.ArrivalRaw != "260101" || e.DepartureRaw != "260105" {
		t.Errorf("typed event fields wrong (authoritative RN/G# map): %+v", e)
	}
	if !isHex64(e.ExternalEventIdentity) {
		t.Errorf("external identity must be a 64-hex content hash, got %q", e.ExternalEventIdentity)
	}
	// the typed event must carry NO raw STX/ETX/control bytes
	for _, f := range []string{e.ReservationRef, e.RoomNumber, e.GuestLastName, e.GuestFirstName, e.ExternalEventIdentity} {
		if strings.ContainsAny(f, "\x02\x03") {
			t.Errorf("typed field carried raw frame bytes: %q", f)
		}
	}
	if !isHex64(e.SourceEvidenceHash) || e.EvidenceKeyVersion <= 0 || !isHex64(e.SourceEventFingerprint) || e.FingerprintKeyVersion <= 0 {
		t.Errorf("event missing keyed evidence/fingerprint: evHash=%q evVer=%d fp=%q fpVer=%d", e.SourceEvidenceHash, e.EvidenceKeyVersion, e.SourceEventFingerprint, e.FingerprintKeyVersion)
	}
}

// TestAdapter_InitialResyncRequestsDR proves §G/§H: after the startup handshake the adapter raises the
// barrier (RequireInitialResync) and sends the initial DR resync request through the single writer, before
// any live admission.
func TestAdapter_InitialResyncRequestsDR(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	adapter.rev.HeartbeatInterval = time.Hour
	adapter.rev.HeartbeatTimeout = 5 * time.Second
	peer := newPipePeer(server)
	sink := &recordingSink{q: NewBoundedQueue(16, time.Second)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = adapter.Serve(ctx, sink) }()

	// consume the 5 startup frames, then the initial DR must arrive
	for i := 0; i < 5; i++ {
		<-peer.recv
	}
	if peer.waitFor("DR", 1, 2*time.Second) < 1 {
		t.Fatal("adapter must send an initial DR resync request after connect")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.initialResync != 1 {
		t.Errorf("RequireInitialResync calls = %d, want 1", sink.initialResync)
	}
	if sink.connected != 1 {
		t.Errorf("OnConnected = %d, want 1", sink.connected)
	}
}

// TestAdapter_MalformedRecordFaultsNotSilentDrop proves §B at the Serve boundary: a GI record with an
// identity-truncating overlong reservation is NOT silently skipped. The adapter drives a continuity fault
// through the sink (OnContinuityFault) AND requests a verified resync (DR) — and admits ZERO events, so no
// truncated value can reach the queue/inbox.
func TestAdapter_MalformedRecordFaultsNotSilentDrop(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	sink := &recordingSink{q: NewBoundedQueue(16, time.Second)}

	sawDR := make(chan struct{}, 1)
	go func() {
		br := bufio.NewReader(server)
		for i := 0; i < 5; i++ {
			if _, err := pms.ReadFramedRecord(br); err != nil {
				return
			}
		}
		// a GI whose reservation (G#) is grossly overlong — must be rejected, never truncated
		overlong := "GI|RN10|G#" + strings.Repeat("9", maxReservationLen+16) + "|GNX|GFY|GA260101|GD260105|"
		if err := pms.WriteFramedRecord(server, overlong); err != nil {
			return
		}
		for {
			body, err := pms.ReadFramedRecord(br)
			if err != nil {
				return
			}
			if pms.RecordID(body) == "DR" {
				select {
				case sawDR <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = adapter.Serve(ctx, sink) }()

	select {
	case <-sawDR:
		// adapter requested a verified resync on the malformed record (no silent skip)
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not request a resync (DR) on a malformed/overlong record")
	}
	// give the fault write a beat to land, then assert: fault recorded, zero events admitted
	time.Sleep(20 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.continuityFlt < 1 {
		t.Errorf("malformed record must drive a continuity fault, got %d", sink.continuityFlt)
	}
	if len(sink.events) != 0 {
		t.Errorf("malformed record admitted %d events (must be zero — no truncated value reaches the inbox)", len(sink.events))
	}
}

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

func testRev() Revision {
	r := validRevision()
	r.HeartbeatTimeout = 2 * time.Second
	r.WriteTimeout = time.Second
	return r
}

func newAdapterOverPipe(t *testing.T) (*fiasAdapter, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	dialer := func(ctx context.Context, network, address string) (net.Conn, error) { return client, nil }
	dial := NewFIASDial(dialer, []byte("0123456789abcdef0123456789abcdef"), 1, time.Now)
	conn, err := dial(context.Background(), DialParams{Iface: iface("i1"), Rev: testRev()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn.(*fiasAdapter), server
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
		// send a guest-in, a resync window, and a heartbeat
		for _, rec := range []string{
			"GI|RN12345|FL1408|GNSmith|GA260101|",
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
	if e.RecordType != RecGI || e.ReservationRef != "12345" || e.RoomNumber != "1408" || e.GuestName != "Smith" {
		t.Errorf("typed event fields wrong: %+v", e)
	}
	// the typed event must carry NO raw STX/ETX/control bytes
	for _, f := range []string{e.ReservationRef, e.RoomNumber, e.GuestName, e.ExternalEventIdentity} {
		if strings.ContainsAny(f, "\x02\x03") {
			t.Errorf("typed field carried raw frame bytes: %q", f)
		}
	}
	if e.SourceEvidenceHash == "" || e.EvidenceKeyVersion != 1 {
		t.Errorf("event missing keyed evidence: hash=%q ver=%d", e.SourceEvidenceHash, e.EvidenceKeyVersion)
	}
}

// TestAdapter_QueueOverflowRequestsResync floods domain events past a tiny queue and asserts the adapter
// issues a DR resync request rather than silently dropping.
func TestAdapter_QueueOverflowRequestsResync(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	sink := &recordingSink{q: NewBoundedQueue(1, 20*time.Millisecond)} // tiny + no consumer → overflow

	sawDR := make(chan struct{}, 1)
	go func() {
		br := bufio.NewReader(server)
		for i := 0; i < 5; i++ {
			if _, err := pms.ReadFramedRecord(br); err != nil {
				return
			}
		}
		// send just enough guest-in records to overflow the cap-1 queue (no consumer draining it); more than
		// that would deadlock net.Pipe since the adapter blocks writing DR while we block writing GI.
		for i := 0; i < 2; i++ {
			if err := pms.WriteFramedRecord(server, "GI|RN"+string(rune('A'+i))+"1|FL10|GNX|GA260101|"); err != nil {
				return
			}
		}
		// watch for the DR resync request the adapter must send
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
		// adapter requested a verified resync on overflow (no silent drop)
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not request a resync (DR) on queue overflow")
	}
	sink.mu.Lock()
	ov := sink.overflow
	sink.mu.Unlock()
	if !ov {
		t.Error("overflow was not observed by the sink")
	}
}

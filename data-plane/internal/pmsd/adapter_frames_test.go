package pmsd

import (
	"bufio"
	"context"
	"errors"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// gateConn wraps a net.Conn and can be armed to fail all subsequent Writes, so a test can inject a socket
// write failure at a precise point AFTER a successful startup. Reads/Close/deadlines delegate unchanged.
type gateConn struct {
	net.Conn
	fail atomic.Bool
}

func (c *gateConn) Write(b []byte) (int, error) {
	if c.fail.Load() {
		return 0, errors.New("gated write failure")
	}
	return c.Conn.Write(b)
}

// newAdapterOverGatedPipe builds an adapter whose socket is a gateConn over a net.Pipe; returns the adapter,
// the gate (to arm write failures) and the peer side.
func newAdapterOverGatedPipe(t *testing.T) (*fiasAdapter, *gateConn, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	gc := &gateConn{Conn: client}
	k := testKeys()
	a := &fiasAdapter{
		g:     &guardedConn{c: gc, writeTimeout: 200 * time.Millisecond},
		br:    bufio.NewReader(gc),
		iface: iface("i1"), rev: testRev(),
		evKey: k.EvidenceKey, evKeyNo: k.EvidenceKeyVersion,
		identKey: k.IdentityKey, identKeyN: k.IdentityKeyVersion, profile: "protel-fias/v1", now: time.Now,
	}
	return a, gc, server
}

// ---- item 1: strict-parse EVERY inbound frame ---------------------------------------------------------

// TestFrames_MalformedControlTerminates proves a malformed/unsupported CONTROL record (or unknown record id)
// terminates the ownership cycle with the bounded PROTOCOL_FRAMING_ERROR code, recording NO heartbeat and
// sending NO ack for the invalid record.
func TestFrames_MalformedControlTerminates(t *testing.T) {
	cases := []string{
		"LAjunk|", // junk right after a control record id
		"LSjunk|", // junk after LS
		"DSx|",    // malformed DS
		"DE!|",    // malformed DE
		"LS|R|",   // malformed control field (one-char token)
		"ZZ|RN1|", // unsupported record id
		"QQ",      // unknown, no delimiter
	}
	for _, bad := range cases {
		adapter, server := newAdapterOverPipe(t)
		adapter.rev.HeartbeatInterval = time.Hour
		adapter.rev.HeartbeatTimeout = 5 * time.Second
		peer := newPipePeer(server)
		sink := &recordingSink{q: NewBoundedQueue(16, time.Second)}
		done := make(chan error, 1)
		go func() { done <- adapter.Serve(context.Background(), sink) }()
		for i := 0; i < 5; i++ {
			<-peer.recv // startup
		}
		if err := peer.send(bad); err != nil {
			t.Fatalf("%q: send: %v", bad, err)
		}
		select {
		case err := <-done:
			if Classify(err) != CodeProtocolFraming {
				t.Errorf("%q: terminate code = %q, want PROTOCOL_FRAMING_ERROR", bad, Classify(err))
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%q: Serve did not terminate on a malformed control record", bad)
		}
		sink.mu.Lock()
		hb := sink.heartbeats
		sink.mu.Unlock()
		if hb != 0 {
			t.Errorf("%q: malformed control must create NO heartbeat evidence, got %d", bad, hb)
		}
		_ = server.Close()
	}
}

// TestFrames_MalformedDomainGapAndZeroAdmission proves a malformed DOMAIN record drives the atomic gap/resync
// transition, admits zero events, requests DR, and keeps the link (does NOT terminate).
func TestFrames_MalformedDomainGapAndZeroAdmission(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	adapter.rev.HeartbeatInterval = time.Hour
	adapter.rev.HeartbeatTimeout = 5 * time.Second
	peer := newPipePeer(server)
	sink := &recordingSink{q: NewBoundedQueue(16, time.Second)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = adapter.Serve(ctx, sink); close(done) }()
	for i := 0; i < 5; i++ {
		<-peer.recv
	}
	// consume the initial DR (barrier already requested a resync at connect)
	if peer.waitFor("DR", 1, 2*time.Second) < 1 {
		t.Fatal("adapter must send an initial DR after connect")
	}
	if err := peer.send("GIjunk|"); err != nil { // malformed domain
		t.Fatal(err)
	}
	// the malformed domain must drive a continuity fault (a resync is already outstanding, so no NEW DR)
	deadline := time.Now().Add(2 * time.Second)
	for {
		sink.mu.Lock()
		cf := sink.continuityFlt
		sink.mu.Unlock()
		if cf >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("malformed domain must drive a continuity fault")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("malformed domain must NOT terminate the link")
	default:
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 0 {
		t.Errorf("malformed domain admitted %d events (must be zero)", len(sink.events))
	}
}

// ---- item 2: prompt bounded shutdown ------------------------------------------------------------------

// TestShutdown_PromptOnCancel proves cancellation releases a reader parked behind a multi-minute heartbeat
// timeout: a controlled LE is emitted, the transport is closed, and Serve returns within a short bound.
func TestShutdown_PromptOnCancel(t *testing.T) {
	before := runtime.NumGoroutine()
	adapter, server := newAdapterOverPipe(t)
	adapter.rev.HeartbeatTimeout = 5 * time.Minute // reader would otherwise park for minutes
	adapter.rev.HeartbeatInterval = time.Hour
	peer := newPipePeer(server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = adapter.Serve(ctx, &recordingSink{q: NewBoundedQueue(16, time.Second)}); close(done) }()
	for i := 0; i < 5; i++ {
		<-peer.recv // startup; peer then sends NOTHING
	}
	start := time.Now()
	cancel()
	if peer.waitFor("LE", 1, 2*time.Second) < 1 {
		t.Fatal("cancellation must emit a controlled LE")
	}
	select {
	case <-done:
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("shutdown took %v — must be independent of the 5m heartbeat timeout", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return promptly after cancellation")
	}
	_ = server.Close()
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutine leak: before=%d after=%d", before, after)
	}
}

// ---- item 4: post-start writer-failure per frame ------------------------------------------------------

// TestFrames_PostStartWriteFailures injects a socket write failure AFTER a successful startup, separately for
// each critical outbound frame, and proves each ends the ownership cycle (Serve returns, transport closed,
// reader released, no goroutine leak).
func TestFrames_PostStartWriteFailures(t *testing.T) {
	// trigger is a function that, after startup + arming the gate, causes the specific outbound frame.
	cases := []struct {
		name    string
		setup   func(a *fiasAdapter)
		trigger func(peer *pipePeer)
	}{
		{"LS-ack LA", func(a *fiasAdapter) { a.rev.HeartbeatInterval = time.Hour },
			func(p *pipePeer) { _ = p.send("LS|DA260101|TI120000|") }},
		{"LA-ack LA", func(a *fiasAdapter) { a.rev.HeartbeatInterval = time.Hour },
			func(p *pipePeer) { _ = p.send(pms.BuildLA()) }},
		{"idle keepalive LA", func(a *fiasAdapter) { a.rev.HeartbeatInterval = 20 * time.Millisecond },
			func(p *pipePeer) {}}, // no send: the idle ticker fires the failing write
		{"DR after malformed domain (post-sync)", func(a *fiasAdapter) { a.rev.HeartbeatInterval = time.Hour },
			// a fresh DR is only emitted for a malformed domain when NOT already resyncing, i.e. after a
			// completed DS→DE lowers the barrier; then a malformed domain requests a new (failing) DR.
			func(p *pipePeer) { _ = p.send("DS|"); _ = p.send("DE|"); _ = p.send("GIjunk|") }},
	}
	for _, tc := range cases {
		before := runtime.NumGoroutine()
		adapter, gc, server := newAdapterOverGatedPipe(t)
		adapter.rev.HeartbeatTimeout = 5 * time.Second
		tc.setup(adapter)
		peer := newPipePeer(server)
		done := make(chan error, 1)
		go func() {
			done <- adapter.Serve(context.Background(), &recordingSink{q: NewBoundedQueue(16, time.Second)})
		}()
		for i := 0; i < 5; i++ {
			<-peer.recv // startup succeeds
		}
		// let the INITIAL DR (sent after connect) succeed first, so each subcase exercises its OWN frame.
		if peer.waitFor("DR", 1, 2*time.Second) < 1 {
			t.Fatalf("%s: initial DR never arrived", tc.name)
		}
		gc.fail.Store(true) // arm: all further writes fail
		tc.trigger(peer)
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: Serve must return an error on the write failure", tc.name)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: Serve did not terminate on the write failure", tc.name)
		}
		_ = server.Close()
		time.Sleep(30 * time.Millisecond)
		if after := runtime.NumGoroutine(); after > before+3 {
			t.Errorf("%s: goroutine leak before=%d after=%d", tc.name, before, after)
		}
	}
}

// TestShutdown_LEWriteFailureStillTerminates proves that even if the controlled LE write fails during
// shutdown, the writer still closes the transport and Serve returns promptly.
func TestShutdown_LEWriteFailureStillTerminates(t *testing.T) {
	adapter, gc, server := newAdapterOverGatedPipe(t)
	adapter.rev.HeartbeatTimeout = 5 * time.Minute
	adapter.rev.HeartbeatInterval = time.Hour
	peer := newPipePeer(server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = adapter.Serve(ctx, &recordingSink{q: NewBoundedQueue(16, time.Second)}); close(done) }()
	for i := 0; i < 5; i++ {
		<-peer.recv
	}
	gc.fail.Store(true) // the LE write during shutdown will fail
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not terminate when the shutdown LE write failed")
	}
	_ = server.Close()
}

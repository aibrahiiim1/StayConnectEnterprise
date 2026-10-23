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

// gateConn can be armed to fail all subsequent Writes, injecting a write failure after a successful startup.
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

// A malformed control record or unknown record id ends the cycle with PROTOCOL_FRAMING_ERROR, no heartbeat, no ack.
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

// A malformed domain record drives a gap/resync, admits zero events and keeps the link up.
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
	if peer.waitFor("DR", 1, 2*time.Second) < 1 {
		t.Fatal("adapter must send an initial DR after connect")
	}
	if err := peer.send("GIjunk|"); err != nil { // malformed domain
		t.Fatal(err)
	}
	// A resync is already outstanding, so no new DR is sent.
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

// Cancellation must release a reader parked behind a long heartbeat timeout: emit LE, close, return promptly.
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

// A write failure after startup, on each critical outbound frame, must end the cycle without leaking goroutines.
func TestFrames_PostStartWriteFailures(t *testing.T) {
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
			// A new DR is only emitted when not already resyncing, i.e. after a completed DS→DE.
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
		// Let the initial DR succeed first so each subcase exercises its own frame.
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

// Even if the LE write fails during shutdown, the transport closes and Serve returns.
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

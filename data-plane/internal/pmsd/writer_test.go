package pmsd

import (
	"bufio"
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// pipePeer reads continuously so the adapter's writer never blocks, forwarding each received record id on recv.
type pipePeer struct {
	conn net.Conn
	recv chan string
}

func newPipePeer(conn net.Conn) *pipePeer {
	p := &pipePeer{conn: conn, recv: make(chan string, 512)}
	go func() {
		br := bufio.NewReader(conn)
		for {
			body, err := pms.ReadFramedRecord(br)
			if err != nil {
				close(p.recv)
				return
			}
			p.recv <- pms.RecordID(body)
		}
	}()
	return p
}

func (p *pipePeer) send(body string) error { return pms.WriteFramedRecord(p.conn, body) }

func (p *pipePeer) waitFor(id string, want int, d time.Duration) int {
	deadline := time.After(d)
	got := 0
	for got < want {
		select {
		case rid, ok := <-p.recv:
			if !ok {
				return got
			}
			if rid == id {
				got++
			}
		case <-deadline:
			return got
		}
	}
	return got
}

// errConn's Write always fails; Read blocks until Close so the failure is attributable to the write.
type errConn struct {
	closed chan struct{}
	once   sync.Once
}

func newErrConn() *errConn                          { return &errConn{closed: make(chan struct{})} }
func (c *errConn) Write(b []byte) (int, error)      { return 0, errors.New("write failed") }
func (c *errConn) Read(b []byte) (int, error)       { <-c.closed; return 0, errors.New("closed") }
func (c *errConn) Close() error                     { c.once.Do(func() { close(c.closed) }); return nil }
func (c *errConn) LocalAddr() net.Addr              { return dummyAddr{} }
func (c *errConn) RemoteAddr() net.Addr             { return dummyAddr{} }
func (c *errConn) SetDeadline(time.Time) error      { return nil }
func (c *errConn) SetReadDeadline(time.Time) error  { return nil }
func (c *errConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "pipe" }
func (dummyAddr) String() string  { return "pipe" }

func TestWriter_StartupAndAcks(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	adapter.rev.HeartbeatInterval = time.Hour // no idle LA during this test
	peer := newPipePeer(server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = adapter.Serve(ctx, &recordingSink{q: NewBoundedQueue(16, time.Second)}) }()

	// startup: LS then LD then three LR
	want := []string{"LS", "LD", "LR", "LR", "LR"}
	for i, w := range want {
		select {
		case got := <-peer.recv:
			if got != w {
				t.Fatalf("startup frame %d = %q, want %q", i, got, w)
			}
		case <-time.After(time.Second):
			t.Fatalf("startup frame %d (%s) never arrived", i, w)
		}
	}
	// incoming LS → bare LA
	if err := peer.send("LS|DA260101|TI120000|"); err != nil {
		t.Fatal(err)
	}
	if peer.waitFor("LA", 1, time.Second) < 1 {
		t.Fatal("incoming LS must be acked with a bare LA")
	}
	// incoming LA → bare LA
	if err := peer.send(pms.BuildLA()); err != nil {
		t.Fatal(err)
	}
	if peer.waitFor("LA", 1, time.Second) < 1 {
		t.Fatal("incoming LA must be acked with a bare LA")
	}
}

func TestWriter_IdleKeepaliveAndLinkAlive(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	adapter.rev.HeartbeatInterval = 25 * time.Millisecond
	adapter.rev.HeartbeatTimeout = 5 * time.Second // don't let the read deadline end the link during the test
	peer := newPipePeer(server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = adapter.Serve(ctx, &recordingSink{q: NewBoundedQueue(16, time.Second)}); close(done) }()

	// consume the 5 startup frames, then count idle keepalives: a dated LS, which Protel answers (a bare LA it does not)
	for i := 0; i < 5; i++ {
		<-peer.recv
	}
	if n := peer.waitFor("LS", 3, 2*time.Second); n < 3 {
		t.Fatalf("expected >=3 idle keepalive LS records, got %d", n)
	}
	// link must still be alive (Serve has not returned)
	select {
	case <-done:
		t.Fatal("Serve returned while idle — link should stay alive")
	default:
	}
}

func TestWriter_CancellationEmitsLEAndNoLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	adapter, server := newAdapterOverPipe(t)
	adapter.rev.HeartbeatInterval = 20 * time.Millisecond
	adapter.rev.HeartbeatTimeout = 100 * time.Millisecond
	peer := newPipePeer(server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = adapter.Serve(ctx, &recordingSink{q: NewBoundedQueue(16, time.Second)}); close(done) }()
	for i := 0; i < 5; i++ {
		<-peer.recv
	}
	cancel()
	if peer.waitFor("LE", 1, time.Second) < 1 {
		t.Fatal("cancellation must emit a controlled LE")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancellation")
	}
	_ = server.Close()
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutine leak: before=%d after=%d", before, after)
	}
}

func TestWriter_WriteFailureTerminatesServe(t *testing.T) {
	k := testKeys()
	adapter := &fiasAdapter{
		g:     &guardedConn{c: newErrConn(), writeTimeout: 100 * time.Millisecond},
		br:    bufio.NewReader(newErrConn()),
		iface: iface("i1"), rev: testRev(),
		evKey: k.EvidenceKey, evKeyNo: k.EvidenceKeyVersion,
		identKey: k.IdentityKey, identKeyN: k.IdentityKeyVersion, profile: "protel-fias/v1", now: time.Now,
	}
	done := make(chan error, 1)
	go func() {
		done <- adapter.Serve(context.Background(), &recordingSink{q: NewBoundedQueue(4, time.Second)})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Serve must return an error when the startup write fails")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not terminate on write failure")
	}
}

func TestWriter_ConcurrentRequestsSerialized(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	w := newSerialWriter(&guardedConn{c: client, writeTimeout: time.Second}, 0, nil) // idle disabled
	ctx, cancel := context.WithCancel(context.Background())
	go w.run(ctx)

	const n = 200
	// reader collects frames; each must be a complete, correctly-framed DR or LA (never a spliced/overlapped frame)
	got := make(chan string, n)
	go func() {
		br := bufio.NewReader(server)
		for i := 0; i < n; i++ {
			body, err := pms.ReadFramedRecord(br)
			if err != nil {
				return
			}
			got <- body
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = w.Submit(context.Background(), pms.BuildDR())
			} else {
				_ = w.Submit(context.Background(), pms.BuildLA())
			}
		}(i)
	}
	wg.Wait()

	seen := 0
	timeout := time.After(3 * time.Second)
	for seen < n {
		select {
		case body := <-got:
			if id := pms.RecordID(body); id != "DR" && id != "LA" {
				t.Fatalf("overlapping/corrupt frame received: %q", body)
			}
			seen++
		case <-timeout:
			t.Fatalf("only %d/%d serialized frames arrived", seen, n)
		}
	}
	cancel()
	<-w.done
}

// A PMS that, like the real CHR Protel, never answers a bare LA but answers every LS with LA at once: a quiet link must
// stay up past the read deadline, and each answer is recorded as heartbeat evidence. With the old bare-LA keepalive this
// peer sent nothing and the link ended at HeartbeatTimeout.
func TestWriter_AQuietProtelLinkStaysUpOnAnsweredKeepalives(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	adapter.rev.HeartbeatInterval = 30 * time.Millisecond
	adapter.rev.HeartbeatTimeout = 120 * time.Millisecond // four keepalive periods, as 30 s / 90 s in production
	conn := server
	go func() {
		br := bufio.NewReader(conn)
		for {
			body, err := pms.ReadFramedRecord(br)
			if err != nil {
				return
			}
			if pms.RecordID(body) == "LS" {
				// Answered off the read loop: net.Pipe is unbuffered, a TCP socket is not.
				go func() { _ = pms.WriteFramedRecord(conn, pms.BuildLA()) }()
			}
		}
	}()
	sink := &recordingSink{q: NewBoundedQueue(16, time.Second)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- adapter.Serve(ctx, sink) }()
	select {
	case err := <-done:
		t.Fatalf("a quiet link that answers every keepalive ended: %v", err)
	case <-time.After(10 * adapter.rev.HeartbeatTimeout):
	}
	sink.mu.Lock()
	hb := sink.heartbeats
	sink.mu.Unlock()
	if hb < 5 {
		t.Fatalf("%d heartbeats recorded from the answered keepalives, want at least 5", hb)
	}
}

// A stable FIAS link makes a complete sync every complete_sync_ms - heartbeat_timeout, so the occupancy evidence Room sign-in
// relies on never ages past its ceiling. Before, a FIAS link that stayed up never swept again after the first.
func TestFIAS_AStableLinkSweepsAgainBeforeTheEvidenceAgesOut(t *testing.T) {
	adapter, server := newAdapterOverPipe(t)
	adapter.rev.HeartbeatInterval = 20 * time.Millisecond
	adapter.rev.HeartbeatTimeout = 100 * time.Millisecond
	adapter.rev.CompleteSyncBound = 250 * time.Millisecond // next sweep requested 150 ms after each publish
	conn := server
	var mu sync.Mutex
	drs := 0
	go func() {
		br := bufio.NewReader(conn)
		for {
			body, err := pms.ReadFramedRecord(br)
			if err != nil {
				return
			}
			switch pms.RecordID(body) {
			case "LS":
				go func() { _ = pms.WriteFramedRecord(conn, pms.BuildLA()) }()
			case "DR":
				mu.Lock()
				drs++
				mu.Unlock()
				go func() {
					_ = pms.WriteFramedRecord(conn, "DS|")
					_ = pms.WriteFramedRecord(conn, "GI|RN201|G#2001|")
					_ = pms.WriteFramedRecord(conn, "DE|")
				}()
			}
		}
	}()
	sink := &recordingSink{q: NewBoundedQueue(64, time.Second)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- adapter.Serve(ctx, sink) }()
	select {
	case err := <-done:
		t.Fatalf("the link ended: %v", err)
	case <-time.After(1200 * time.Millisecond):
	}
	mu.Lock()
	n := drs
	mu.Unlock()
	sink.mu.Lock()
	complete := sink.resyncComplete
	sink.mu.Unlock()
	if n < 3 || complete < 3 {
		t.Fatalf("%d sweeps requested and %d published in 1.2 s on a stable link, want at least 3 (the first plus periodic ones)", n, complete)
	}
}

package pmsd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func opErr(op string, err error) error {
	return &net.OpError{Op: op, Net: "tcp", Err: &os.SyscallError{Syscall: op, Err: err}}
}

func TestClassifyTransport_RealErrorValues(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Code
	}{
		{"eof", io.EOF, CodeTransportRemoteClosed},
		{"eof mid-frame", fmt.Errorf("frame: %w", io.ErrUnexpectedEOF), CodeTransportRemoteClosed},
		{"econnreset on read", opErr("read", syscall.ECONNRESET), CodeTransportReset},
		{"econnaborted", opErr("read", syscall.ECONNABORTED), CodeTransportReset},
		{"epipe on write", opErr("write", syscall.EPIPE), CodeTransportReset},
		{"deadline exceeded", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, CodeTransportTimeout},
		{"net.Error timeout", timeoutErr{}, CodeTransportTimeout},
		{"closed locally", &net.OpError{Op: "read", Net: "tcp", Err: net.ErrClosed}, CodeTransportClosedLocal},
		{"unknown", errors.New("guest John Q. Guest room-1408"), CodeUnclassified},
	}
	for _, c := range cases {
		err := wrapTransport(transportOpRead, c.err)
		if got := Classify(err); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
		if !errors.Is(err, c.err) {
			t.Errorf("%s: the wrapped error must stay reachable for errors.Is", c.name)
		}
		if err.Error() != c.want.String() {
			t.Errorf("%s: Error() = %q must be the code only", c.name, err.Error())
		}
	}
}

func TestClassify_NonTransportEndings(t *testing.T) {
	if got := Classify(context.Canceled); got != CodeContextCanceled {
		t.Errorf("context.Canceled = %q", got)
	}
	if got := Classify(coded(CodeProtocolFraming, nil)); got != CodeProtocolFraming {
		t.Errorf("framing = %q", got)
	}
	if got := Classify(coded(CodeProtocolLinkEnded, nil)); got != CodeProtocolLinkEnded {
		t.Errorf("LE = %q", got)
	}
	pg := fmt.Errorf("admit: %w", &pgconn.PgError{Code: "40001", Message: "guest John Q. Guest"})
	if got := Classify(pg); got != CodeDBStatementFailed {
		t.Errorf("PgError = %q", got)
	}
	if got := sqlState(pg); got != "40001" {
		t.Errorf("sqlState = %q", got)
	}
	if got := sqlState(&pgconn.PgError{Code: "room-1408"}); got != "" {
		t.Errorf("a malformed SQLSTATE must be dropped, got %q", got)
	}
	// A database socket failure is not a PMS transport ending.
	if got := Classify(opErr("read", syscall.ECONNRESET)); got != CodeUnclassified {
		t.Errorf("an unwrapped socket error = %q, want UNCLASSIFIED", got)
	}
}

type loopbackPeer struct {
	c  *net.TCPConn
	br *bufio.Reader
}

func (p *loopbackPeer) read(t *testing.T, n int) {
	t.Helper()
	_ = p.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < n; i++ {
		if _, err := pms.ReadFramedRecord(p.br); err != nil {
			t.Fatalf("peer read %d: %v", i, err)
		}
	}
}

func (p *loopbackPeer) send(t *testing.T, body string) {
	t.Helper()
	if err := pms.WriteFramedRecord(p.c, body); err != nil {
		t.Fatalf("peer send: %v", err)
	}
}

// serveOverLoopback dials a real TCP listener through NewFIASDial and returns once the handshake and the
// initial DR have been read by the peer.
func serveOverLoopback(t *testing.T, heartbeatTimeout time.Duration) (*fiasAdapter, *loopbackPeer, chan error) {
	return serveOverLoopbackTo(t, heartbeatTimeout, &recordingSink{q: NewBoundedQueue(16, time.Second)})
}

func serveOverLoopbackTo(t *testing.T, heartbeatTimeout time.Duration, sink AxisSink) (*fiasAdapter, *loopbackPeer, chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan *net.TCPConn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c.(*net.TCPConn)
		}
	}()
	rev := testRev()
	rev.Endpoint = ln.Addr().String()
	rev.HeartbeatInterval = time.Hour
	rev.HeartbeatTimeout = heartbeatTimeout
	var d net.Dialer
	conn, err := NewFIASDial(d.DialContext, testKeys(), time.Now, nil)(context.Background(), DialParams{Iface: iface("i1"), Rev: rev})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	a := conn.(*fiasAdapter)
	t.Cleanup(func() { _ = a.Close() })
	var srv *net.TCPConn
	select {
	case srv = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("no accept")
	}
	t.Cleanup(func() { _ = srv.Close() })
	peer := &loopbackPeer{c: srv, br: bufio.NewReader(srv)}
	done := make(chan error, 1)
	go func() { done <- a.Serve(context.Background(), sink) }()
	peer.read(t, 6) // LS, LD, three LR, DR
	return a, peer, done
}

func waitServe(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not end")
		return nil
	}
}

const canaryGI = "GI|RN1408|G#88213371|GNSmith|GFJohn|GA260101|GD260105|"

func TestServe_LoopbackEndings(t *testing.T) {
	t.Run("remote close", func(t *testing.T) {
		a, peer, done := serveOverLoopback(t, 5*time.Second)
		peer.send(t, canaryGI)
		time.Sleep(50 * time.Millisecond)
		_ = peer.c.Close()
		err := waitServe(t, done)
		if got := Classify(err); got != CodeTransportRemoteClosed {
			t.Fatalf("code = %q, want TRANSPORT_REMOTE_CLOSED (%v)", got, errors.Unwrap(err))
		}
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		g := a.linkDiagnostics()
		logProtocolEnded(log, Classify(err), SafeFields{InterfaceID: NewUUIDValue(tRevUUID), Stage: StageServe},
			endingDetail{err: err, publishedAt: time.Now().Add(-2 * time.Second), diag: &g})
		line := buf.String()
		for _, want := range []string{
			`"code":"TRANSPORT_REMOTE_CLOSED"`, `"transport_op":"read"`, `"direction_hint":"remote_closed"`,
			`"last_in_record":"GI"`, `"last_out_record":"DR"`, `"resync_in_progress":true`,
			`"soon_after_resync_published":true`, `"bytes_in":`, `"remote_port":`,
		} {
			if !strings.Contains(line, want) {
				t.Errorf("log line lacks %s: %s", want, line)
			}
		}
		for _, leak := range []string{"1408", "88213371", "Smith", "John", "127.0.0.1"} {
			if strings.Contains(line, leak) {
				t.Errorf("log line leaked %q: %s", leak, line)
			}
		}
		if g.BytesIn < int64(len(canaryGI)) || g.BytesOut == 0 || g.RemotePort == 0 || g.LocalPort == 0 {
			t.Errorf("diagnostics not counted: %+v", g)
		}
	})
	t.Run("reset", func(t *testing.T) {
		_, peer, done := serveOverLoopback(t, 5*time.Second)
		_ = peer.c.SetLinger(0)
		_ = peer.c.Close()
		if got := Classify(waitServe(t, done)); got != CodeTransportReset {
			t.Fatalf("code = %q, want TRANSPORT_RESET", got)
		}
	})
	t.Run("read deadline", func(t *testing.T) {
		_, _, done := serveOverLoopback(t, 200*time.Millisecond)
		err := waitServe(t, done)
		if got := Classify(err); got != CodeTransportTimeout {
			t.Fatalf("code = %q, want TRANSPORT_TIMEOUT", got)
		}
		if directionHint(Classify(err)) != hintLocalDeadline {
			t.Errorf("a pmsd deadline is a local observation")
		}
	})
}

// An unparseable inbound frame contributes no payload bytes to the record-type field.
func TestLinkStats_InboundTypeIsClosedSet(t *testing.T) {
	var st linkStats
	st.inbound("Jo|hn Q. Guest")
	if g := st.snapshot(); g.LastInType != inboundUnparsed {
		t.Errorf("LastInType = %q", g.LastInType)
	}
	st.inbound("LA|")
	if g := st.snapshot(); g.LastInType != "LA" {
		t.Errorf("LastInType = %q", g.LastInType)
	}
}

// A database rejection names the step, the SQLSTATE and the schema objects, never the message or detail.
func TestLogProtocolEnded_DatabaseRejection(t *testing.T) {
	pg := &pgconn.PgError{Code: "23505", ConstraintName: "se_live_identity", TableName: "stay_events",
		Routine: "_bt_check_unique", Message: "duplicate key RES88213371", Detail: "Key (x)=(RES88213371 Smith)"}
	err := failedAt(stepDomainRecord, failedAt(stepAdmitLive, pg))
	if got := Classify(err); got != CodeDBStatementFailed {
		t.Fatalf("Classify = %q", got)
	}
	var buf bytes.Buffer
	logProtocolEnded(slog.New(slog.NewJSONHandler(&buf, nil)), Classify(err),
		SafeFields{InterfaceID: NewUUIDValue(tRevUUID), Stage: StageServe}, endingDetail{err: err})
	line := buf.String()
	for _, want := range []string{
		`"code":"DB_STATEMENT_FAILED"`, `"failed_at":"admit_live_record"`, `"sqlstate":"23505"`,
		`"pg_constraint":"se_live_identity"`, `"pg_table":"stay_events"`, `"pg_routine":"_bt_check_unique"`,
		`"error_chain":"*pmsd.stepErr>*pgconn.PgError"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log line lacks %s: %s", want, line)
		}
	}
	for _, leak := range []string{"RES88213371", "Smith", "duplicate key"} {
		if strings.Contains(line, leak) {
			t.Errorf("log line leaked %q: %s", leak, line)
		}
	}
}

func TestSink_DuplicateLiveRecordIsIgnored(t *testing.T) {
	s, repo, id := newBarrierSink(t)
	var buf bytes.Buffer
	s.w.deps.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	s.synced = true
	e := ev("RES88213371")
	if err := s.OnDomainEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	delete(repo.continuity, id)
	repo.mu.Unlock()
	if err := s.OnDomainEvent(context.Background(), e); err != nil {
		t.Fatalf("an exact retransmission must not end serving: %v", err)
	}
	if live, _ := counts(repo); live != 1 {
		t.Fatalf("live rows = %d, want 1", live)
	}
	repo.mu.Lock()
	_, advanced := repo.continuity[id]
	repo.mu.Unlock()
	if advanced {
		t.Error("a duplicate must not advance continuity")
	}
	line := buf.String()
	if !strings.Contains(line, "pmsd: duplicate live record ignored") || !strings.Contains(line, `"record_type":"GI"`) ||
		!strings.Contains(line, `"processing_status":"PENDING"`) {
		t.Errorf("missing duplicate line: %s", line)
	}
	if strings.Contains(line, "RES88213371") {
		t.Errorf("duplicate line leaked the reservation: %s", line)
	}
}

// Protel re-sends an already admitted record after every reconnect; each repeat must leave the session up.
func TestServe_RetransmissionStormKeepsTheSession(t *testing.T) {
	repo := newFakeRepo(iface("i1"))
	for conn := 0; conn < 3; conn++ {
		gen, err := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{PMSInterfaceID: ifaceUUID("i1")})
		if err != nil {
			t.Fatal(err)
		}
		w := &worker{iface: iface("i1"), repo: repo, deps: &Deps{Now: time.Now}}
		sink := &workerSink{w: w, ctx: context.Background(), gen: gen, q: NewBoundedQueue(16, time.Second)}
		_, peer, done := serveOverLoopbackTo(t, 5*time.Second, sink)
		peer.send(t, "DS|")
		peer.send(t, "DE|")
		for i := 0; i < 3; i++ {
			peer.send(t, canaryGI)
		}
		peer.send(t, "LA|")
		peer.read(t, 1) // the LA ack proves the session survived every repeat
		select {
		case err := <-done:
			t.Fatalf("connection %d ended on a retransmission: %v", conn, err)
		default:
		}
		_ = peer.c.Close()
		if got := Classify(waitServe(t, done)); got != CodeTransportRemoteClosed {
			t.Fatalf("connection %d ended with %q", conn, got)
		}
	}
	if live, _ := counts(repo); live != 1 {
		t.Fatalf("live rows = %d, want exactly one", live)
	}
}

package pmsd

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	transportOpRead  = "read"
	transportOpWrite = "write"

	hintRemoteClosed  = "remote_closed"
	hintLocalDeadline = "local_deadline"
	hintLocalClosed   = "local_closed"

	inboundUnparsed = "UNPARSED"

	// An ending this soon after a published resync is flagged so the two can be correlated in the journal.
	afterResyncWindow = 5 * time.Second
)

// transportErr is a socket failure on the PMS link. Like typedErr, its text is only the code; the raw error
// stays reachable through Unwrap for local debugging and is never logged or persisted.
type transportErr struct {
	op   string
	code Code
	err  error
}

func (e *transportErr) Error() string { return e.code.String() }
func (e *transportErr) Unwrap() error { return e.err }

func wrapTransport(op string, err error) error {
	if err == nil {
		return nil
	}
	var tr *transportErr
	if errors.As(err, &tr) {
		return err
	}
	return &transportErr{op: op, code: classifyTransport(err), err: err}
}

// classifyTransport inspects only error identities and types, never the text.
func classifyTransport(err error) Code {
	var ne net.Error
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return CodeTransportRemoteClosed
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED), errors.Is(err, syscall.EPIPE),
		isPlatformReset(err):
		return CodeTransportReset
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return CodeTransportTimeout
	case errors.Is(err, net.ErrClosed):
		return CodeTransportClosedLocal
	default:
		return CodeUnclassified
	}
}

func directionHint(code Code) string {
	switch code {
	case CodeTransportRemoteClosed:
		return hintRemoteClosed
	case CodeTransportTimeout:
		return hintLocalDeadline
	case CodeTransportClosedLocal, CodeContextCanceled, CodeLockSessionLost:
		return hintLocalClosed
	default:
		return ""
	}
}

// linkStats is written by the read loop, the serial writer and the socket wrapper concurrently.
type linkStats struct {
	start      time.Time
	in, out    atomic.Int64
	resyncing  atomic.Bool
	localPort  int
	remotePort int

	mu          sync.Mutex
	lastIn      time.Time
	lastOut     time.Time
	lastInType  string
	lastOutType string
	frozen      *LinkDiagnostics
}

func newLinkStats(c net.Conn) *linkStats {
	return &linkStats{start: time.Now(), localPort: tcpPort(c.LocalAddr()), remotePort: tcpPort(c.RemoteAddr())}
}

func tcpPort(a net.Addr) int {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.Port
	}
	return 0
}

// inbound takes a record type only from the closed RecordType set, so a malformed frame cannot place its
// first two payload bytes in a log line.
func (s *linkStats) inbound(body string) {
	if s == nil {
		return
	}
	t := inboundUnparsed
	if len(body) >= 2 && RecordType(body[:2]).Valid() {
		t = body[:2]
	}
	s.mu.Lock()
	s.lastIn, s.lastInType = time.Now(), t
	s.mu.Unlock()
}

// outbound is called only after a frame passed the outbound allowlist, so the type is always a known record.
func (s *linkStats) outbound(record string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastOut, s.lastOutType = time.Now(), record
	s.mu.Unlock()
}

// LinkDiagnostics describes how a PMS link ended. Every field is protocol or socket metadata; none is derived
// from a payload or from error text.
type LinkDiagnostics struct {
	ConnectedFor     time.Duration
	SinceLastIn      time.Duration // -1 when nothing was received
	SinceLastOut     time.Duration // -1 when nothing was sent
	LastInType       string
	LastOutType      string
	BytesIn          int64
	BytesOut         int64
	LocalPort        int
	RemotePort       int
	ResyncInProgress bool
}

func (s *linkStats) setResyncing(v bool) {
	if s != nil {
		s.resyncing.Store(v)
	}
}

// snapshot of a nil linkStats (an adapter built without a counted socket) is the zero value.
func (s *linkStats) snapshot() LinkDiagnostics {
	if s == nil {
		return LinkDiagnostics{SinceLastIn: -1, SinceLastOut: -1}
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen != nil {
		return *s.frozen
	}
	since := func(t time.Time) time.Duration {
		if t.IsZero() {
			return -1
		}
		return now.Sub(t)
	}
	return LinkDiagnostics{
		ConnectedFor: now.Sub(s.start), SinceLastIn: since(s.lastIn), SinceLastOut: since(s.lastOut),
		LastInType: s.lastInType, LastOutType: s.lastOutType,
		BytesIn: s.in.Load(), BytesOut: s.out.Load(),
		LocalPort: s.localPort, RemotePort: s.remotePort, ResyncInProgress: s.resyncing.Load(),
	}
}

// freeze fixes the snapshot at the moment serving ended, before the shutdown LE is written.
func (s *linkStats) freeze() {
	if s == nil {
		return
	}
	g := s.snapshot()
	s.mu.Lock()
	if s.frozen == nil {
		s.frozen = &g
	}
	s.mu.Unlock()
}

type linkDiagnoser interface{ linkDiagnostics() LinkDiagnostics }

// countingConn counts bytes at the socket, beneath TLS when TLS is used.
type countingConn struct {
	net.Conn
	st *linkStats
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.st.in.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.st.out.Add(int64(n))
	return n, err
}

func ms(d time.Duration) int64 {
	if d < 0 {
		return -1
	}
	return d.Milliseconds()
}

// Steps label which handler call returned the error that ended serving.
const (
	stepHandshake        = "write_handshake"
	stepOnConnected      = "on_connected"
	stepRequireResync    = "require_initial_resync"
	stepWriteResyncReq   = "write_resync_request"
	stepRead             = "read"
	stepParseControl     = "parse_control_record"
	stepContinuityFault  = "continuity_fault"
	stepHeartbeat        = "heartbeat"
	stepWriteAck         = "write_ack"
	stepInboundLinkEnd   = "inbound_link_end"
	stepResyncStart      = "resync_start"
	stepPublishResync    = "publish_resync"
	stepDomainRecord     = "domain_record"
	stepAdmitLive        = "admit_live_record"
	stepStageResync      = "stage_resync_record"
	stepRecordContinuity = "record_continuity"
)

// stepErr carries a step label; its text and identity are those of the wrapped error, so wrapping changes
// nothing for any caller.
type stepErr struct {
	step string
	err  error
}

func (e *stepErr) Error() string { return e.err.Error() }
func (e *stepErr) Unwrap() error { return e.err }

// failedAt keeps the innermost label: the call closest to the failure names it.
func failedAt(step string, err error) error {
	if err == nil {
		return nil
	}
	var se *stepErr
	if errors.As(err, &se) {
		return err
	}
	return &stepErr{step: step, err: err}
}

func stepOf(err error) string {
	var se *stepErr
	if errors.As(err, &se) {
		return se.step
	}
	return ""
}

func safeRecordType(r RecordType) string {
	if r.Valid() {
		return string(r)
	}
	return inboundUnparsed
}

func safeProcessingStatus(s string) string {
	switch s {
	case "PENDING", "APPLIED", "SKIPPED_DUPLICATE", "MANUAL_REVIEW", "FAILED":
		return s
	default:
		return "UNKNOWN"
	}
}

// safeErrAttrs describes an error for a log line without its text, which can carry row values.
func safeErrAttrs(err error) []any {
	return []any{"code", Classify(err).String(), "error_chain", errorChain(err), "sqlstate", sqlState(err)}
}

// errorChain lists the Go types along the error chain: code identifiers, never values.
func errorChain(err error) string {
	var b strings.Builder
	for i := 0; err != nil && i < 10; i++ {
		if i > 0 {
			b.WriteString(">")
		}
		b.WriteString(fmt.Sprintf("%T", err))
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			if es := u.Unwrap(); len(es) > 0 {
				err = es[0]
			} else {
				err = nil
			}
		default:
			err = nil
		}
	}
	return b.String()
}

// pgIdent keeps a schema identifier (constraint, table, routine) only when it has an identifier's shape.
// Message, Detail and Where are never read: they can carry row values.
func pgIdent(s string) string {
	if len(s) == 0 || len(s) > 63 {
		return ""
	}
	for _, r := range s {
		if !(r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return ""
		}
	}
	return s
}

// sqlState is the five-character SQLSTATE of a database rejection, or "" when there is none.
func sqlState(err error) string {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || len(pg.Code) != 5 {
		return ""
	}
	for _, r := range pg.Code {
		if (r < '0' || r > '9') && (r < 'A' || r > 'Z') {
			return ""
		}
	}
	return pg.Code
}

// endingDetail is what the worker adds to an ending: the error that ended serving and when the last resync
// was published on this link (zero when none was).
type endingDetail struct {
	err         error
	publishedAt time.Time
	diag        *LinkDiagnostics
}

// logProtocolEnded is the single WORKER_PROTOCOL_ENDED line. It carries no raw error text and no payload.
func logProtocolEnded(log *slog.Logger, code Code, sf SafeFields, d endingDetail) {
	if log == nil {
		return
	}
	attrs := []any{
		"code", code.String(),
		"interface", sf.InterfaceID.String(),
		"generation", sf.Generation,
		"stage", sf.Stage.safe(),
		"attempt", sf.Attempt,
	}
	if d.err != nil {
		attrs = append(attrs, "failed_at", stepOf(d.err), "error_chain", errorChain(d.err))
	}
	var tr *transportErr
	if errors.As(d.err, &tr) {
		attrs = append(attrs, "transport_op", tr.op)
	}
	if h := directionHint(code); h != "" {
		attrs = append(attrs, "direction_hint", h)
	}
	var pg *pgconn.PgError
	if errors.As(d.err, &pg) {
		attrs = append(attrs,
			"sqlstate", sqlState(d.err),
			"pg_constraint", pgIdent(pg.ConstraintName),
			"pg_table", pgIdent(pg.TableName),
			"pg_routine", pgIdent(pg.Routine),
		)
	}
	if g := d.diag; g != nil {
		attrs = append(attrs,
			"connected_ms", ms(g.ConnectedFor),
			"since_last_in_ms", ms(g.SinceLastIn),
			"since_last_out_ms", ms(g.SinceLastOut),
			"last_in_record", g.LastInType,
			"last_out_record", g.LastOutType,
			"bytes_in", g.BytesIn,
			"bytes_out", g.BytesOut,
			"local_port", g.LocalPort,
			"remote_port", g.RemotePort,
			"resync_in_progress", g.ResyncInProgress,
		)
	}
	sincePub := time.Duration(-1)
	if !d.publishedAt.IsZero() {
		sincePub = time.Since(d.publishedAt)
	}
	attrs = append(attrs,
		"since_resync_published_ms", ms(sincePub),
		"soon_after_resync_published", sincePub >= 0 && sincePub <= afterResyncWindow,
	)
	log.Error(EventWorkerProtocolEnded.safe(), attrs...)
}

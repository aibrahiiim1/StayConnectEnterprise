package pmsd

// THE FINANCIAL RELAY: pmsd CARRIES A ROOM CHARGE, AND DECIDES NOTHING ABOUT IT (decision D45).
//
// The property's PMS accepts one FIAS client connection and pmsd owns it. So a room charge the financial path
// has validated and durably recorded is handed here, over a root-only unix socket, as an immutable command:
// the exact PS bytes, their SHA-256, the interface and the P# they own. The relay
//
//  1. refuses unless room-charge transmission is deployed on THIS process too (STAYCONNECT_PHASE4_MASTER,
//     _OUTBOX_WORKER and _PMS_TRANSMIT) -- scd alone cannot turn pmsd into a financial sender;
//  2. checks the command's shape (a bounded, wire-safe PS carrying exactly its own P#, matching its hash);
//  3. asks the database whether exactly this command is authorised (p4_posting_command_authorised: a SENDING,
//     recent attempt for this interface and P# recording this hash, on an in-flight CHARGE, on an interface
//     still financially ready). pmsd holds EXECUTE on that check and no privilege on any posting table;
//  4. carries it only on a connected link that is between resyncs, one command at a time per link, and never
//     the same P# twice;
//  5. writes the bytes verbatim through the link's single serialized writer, and returns the PA whose P#
//     matches -- verbatim -- or UNKNOWN when none arrives in time or the link ends.
//
// What it will NOT do, by construction: build or edit a PS, choose an amount, room, G#, currency or posting
// code, create a posting, retry anything, interpret AS, settle, reverse or grant access. Those belong to the
// posting engine and the database, on the other side of the socket.
//
// "NOT_TRANSMITTED" is answered only when no byte of the command was handed to the writer. Every doubt after
// that is UNKNOWN -- including a command for a P# this process already carried, which could otherwise be
// mistaken for "never sent" and retried into a second charge.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/postinghandoff"
)

// Environment flags pmsd reads for itself. They are the same names the posting engine uses; pmsd does not
// import the posting package (that package imports this one), so it reads them directly and fails closed.
const (
	envPhase4Master   = "STAYCONNECT_PHASE4_MASTER"
	envPhase4Outbox   = "STAYCONNECT_PHASE4_OUTBOX_WORKER"
	envPhase4Transmit = "STAYCONNECT_PHASE4_PMS_TRANSMIT"
)

// RelayTransmitDeployed reports whether room-charge transmission is deployed on this process: all three flags
// must parse as true. Anything else -- unset, false, malformed -- is "not deployed".
func RelayTransmitDeployed(getenv func(string) string) bool {
	for _, k := range []string{envPhase4Master, envPhase4Outbox, envPhase4Transmit} {
		v, err := strconv.ParseBool(strings.TrimSpace(getenv(k)))
		if err != nil || !v {
			return false
		}
	}
	return true
}

// Authoriser asks the database whether exactly this command may be carried. It returns "AUTHORISED" or a
// bounded refusal code.
type Authoriser func(ctx context.Context, interfaceID string, pNumber int64, bodySHA256 string) (string, error)

// FinancialRelay routes authorised posting commands to the FIAS link that owns their interface.
type FinancialRelay struct {
	enabled   bool
	authorise Authoriser
	log       *slog.Logger

	mu      sync.Mutex
	ports   map[string]*relayPort
	carried map[string]map[int64]struct{} // interface -> P#s this process has handed to a writer
}

// NewFinancialRelay builds the relay. enabled=false answers every command NOT_TRANSMITTED; a nil authoriser
// likewise refuses everything.
func NewFinancialRelay(enabled bool, authorise Authoriser, log *slog.Logger) *FinancialRelay {
	return &FinancialRelay{enabled: enabled, authorise: authorise, log: log,
		ports: map[string]*relayPort{}, carried: map[string]map[int64]struct{}{}}
}

// relayPort is one live FIAS link offered to the relay.
type relayPort struct {
	iface  string
	submit func(ctx context.Context, body string) (bool, error)
	steady atomic.Bool
	lost   chan struct{} // closed when the link's ownership cycle ends

	mu      sync.Mutex
	pending *pendingCommand
}

type pendingCommand struct {
	pn     int64
	answer chan string // buffered 1
}

func (p *relayPort) setSteady(v bool) { p.steady.Store(v) }

// onPA hands a PA to the command waiting for its P#. It reports whether one was waiting.
func (p *relayPort) onPA(body string) bool {
	pn := postinghandoff.PNumberOf(body)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil || pn <= 0 || p.pending.pn != pn {
		return false
	}
	p.pending.answer <- body
	p.pending = nil
	return true
}

func (r *FinancialRelay) attach(iface string, submit func(ctx context.Context, body string) (bool, error)) *relayPort {
	p := &relayPort{iface: iface, submit: submit, lost: make(chan struct{})}
	r.mu.Lock()
	r.ports[iface] = p
	r.mu.Unlock()
	return p
}

func (r *FinancialRelay) detach(p *relayPort) {
	r.mu.Lock()
	if r.ports[p.iface] == p {
		delete(r.ports, p.iface)
	}
	r.mu.Unlock()
	p.setSteady(false)
	close(p.lost)
}

func (r *FinancialRelay) portFor(iface string) *relayPort {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ports[iface]
}

// markCarried records that a P# was handed to a writer on this interface, and reports whether it already had
// been. Bounded: an interface keeps at most 4096 recent P#s (a P# is never reused by the allocator).
func (r *FinancialRelay) markCarried(iface string, pn int64) (already bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := r.carried[iface]
	if set == nil {
		set = map[int64]struct{}{}
		r.carried[iface] = set
	}
	if _, ok := set[pn]; ok {
		return true
	}
	if len(set) >= 4096 {
		for k := range set {
			delete(set, k)
			break
		}
	}
	set[pn] = struct{}{}
	return false
}

func (r *FinancialRelay) wasCarried(iface string, pn int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.carried[iface][pn]
	return ok
}

func notSent(code string) postinghandoff.Response {
	return postinghandoff.Response{Result: postinghandoff.NotTransmitted, Code: code}
}

func unknown(code string) postinghandoff.Response {
	return postinghandoff.Response{Result: postinghandoff.Unknown, Code: code}
}

// Handle carries one command and returns its single answer.
func (r *FinancialRelay) Handle(ctx context.Context, req postinghandoff.Request) postinghandoff.Response {
	if !r.enabled {
		return notSent("PMS_TRANSMIT_NOT_DEPLOYED_ON_PMSD")
	}
	if err := req.Validate(); err != nil {
		return notSent("COMMAND_MALFORMED")
	}
	// A P# this process already handed to a writer is never answered "not sent": it may be with the PMS.
	if r.wasCarried(req.InterfaceID, req.PNumber) {
		return unknown("DUPLICATE_COMMAND")
	}
	// Authorisation first: bytes the financial path did not record are refused whatever the link is doing.
	if r.authorise == nil {
		return notSent("AUTHORISATION_UNAVAILABLE")
	}
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	verdict, err := r.authorise(actx, req.InterfaceID, req.PNumber, req.BodySHA256)
	cancel()
	if err != nil {
		return notSent("AUTHORISATION_UNAVAILABLE")
	}
	if verdict != "AUTHORISED" {
		return notSent(boundedRelayCode(verdict))
	}
	port := r.portFor(req.InterfaceID)
	if port == nil {
		return notSent("LINK_NOT_CONNECTED")
	}
	if !port.steady.Load() {
		return notSent("LINK_RESYNCING")
	}

	wait := time.Duration(req.WaitMillis) * time.Millisecond
	if wait <= 0 || wait > postinghandoff.MaxWait {
		wait = postinghandoff.MaxWait
	}
	pc := &pendingCommand{pn: req.PNumber, answer: make(chan string, 1)}
	port.mu.Lock()
	if port.pending != nil {
		port.mu.Unlock()
		return notSent("LINK_BUSY")
	}
	// Registered BEFORE the write, so a PA that arrives immediately is matched rather than dropped.
	port.pending = pc
	port.mu.Unlock()
	release := func() {
		port.mu.Lock()
		if port.pending == pc {
			port.pending = nil
		}
		port.mu.Unlock()
	}

	if r.markCarried(req.InterfaceID, req.PNumber) {
		release()
		return unknown("DUPLICATE_COMMAND")
	}
	enqueued, werr := port.submit(ctx, req.Body) // the exact bytes, unmodified
	if !enqueued {
		// The writer never took it: provably nothing was written. Forget the P# so the record is exact.
		release()
		r.mu.Lock()
		delete(r.carried[req.InterfaceID], req.PNumber)
		r.mu.Unlock()
		return notSent("LINK_WRITER_UNAVAILABLE")
	}
	if werr != nil {
		release()
		return unknown("WRITE_INCOMPLETE")
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case pa := <-pc.answer:
		return postinghandoff.Response{Result: postinghandoff.Answered, PABody: pa}
	case <-timer.C:
		release()
		return unknown("ANSWER_TIMEOUT")
	case <-port.lost:
		release()
		return unknown("LINK_LOST_AFTER_WRITE")
	case <-ctx.Done():
		release()
		return unknown("RELAY_STOPPED_AFTER_WRITE")
	}
}

func boundedRelayCode(c string) string {
	if len(c) == 0 || len(c) > 48 {
		return "COMMAND_NOT_AUTHORISED"
	}
	for i := 0; i < len(c); i++ {
		ch := c[i]
		if !(ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			return "COMMAND_NOT_AUTHORISED"
		}
	}
	return c
}

// ServeSocket accepts commands on a root-only unix socket until ctx ends. One request and one answer per
// connection. Nothing from a command's body is ever logged: it carries a room number and a reservation.
func (r *FinancialRelay) ServeSocket(ctx context.Context, path string) error {
	if path == "" {
		path = postinghandoff.DefaultSocket
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go r.serveConn(ctx, c)
	}
}

func (r *FinancialRelay) serveConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReaderSize(c, 4096).ReadSlice('\n')
	var resp postinghandoff.Response
	var req postinghandoff.Request
	switch {
	case err != nil:
		resp = notSent("COMMAND_UNREADABLE")
	case json.Unmarshal(line, &req) != nil:
		resp = notSent("COMMAND_UNREADABLE")
	default:
		_ = c.SetReadDeadline(time.Time{})
		resp = r.Handle(ctx, req)
	}
	if r.log != nil {
		r.log.Info("pmsd: posting command", "interface", req.InterfaceID, "p_number", req.PNumber,
			"result", resp.Result, "code", resp.Code)
	}
	b, _ := json.Marshal(resp)
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Write(append(b, '\n'))
}

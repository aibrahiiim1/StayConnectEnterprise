package pmsd

import (
	"bufio"
	"context"
	"errors"
	"net"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// Dialer opens a transport connection. Production wires net.Dialer.DialContext; tests inject net.Pipe or a
// fake TCP listener. This is the ONLY way the adapter obtains a socket.
type Dialer func(ctx context.Context, network, address string) (net.Conn, error)

// guardedConn is the SOLE outbound write path. Every frame passes CheckOutbound before a byte is written;
// there is no exported raw Write, so no production code can bypass the read-only allowlist. A blocked
// record writes ZERO bytes.
type guardedConn struct {
	c            net.Conn
	writeTimeout time.Duration
}

func (g *guardedConn) writeFrame(body string) error {
	if err := CheckOutbound(pms.RecordID(body)); err != nil {
		return coded(CodeOutboundBlocked, err) // blocked BEFORE any byte is written
	}
	if g.writeTimeout > 0 {
		_ = g.c.SetWriteDeadline(time.Now().Add(g.writeTimeout))
	}
	return pms.WriteFramedRecord(g.c, body)
}

// fiasAdapter is the production-capable read-only FIAS connection. It reuses internal/pms framing + parsing
// (no second parser) and drives the interface-level axes + typed domain-event queue via the AxisSink.
type fiasAdapter struct {
	g       *guardedConn
	br      *bufio.Reader
	iface   Interface
	rev     Revision
	evKey   []byte
	evKeyNo int
	now     func() time.Time
}

// NewFIASDial builds a Deps.Dial using an injected Dialer. evidenceKey/version drive the keyed-HMAC source
// evidence digest; the key is never stored on the Event or logged.
func NewFIASDial(dialer Dialer, evidenceKey []byte, evidenceKeyVersion int, now func() time.Time) func(context.Context, DialParams) (Conn, error) {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, p DialParams) (Conn, error) {
		conn, err := dialer(ctx, "tcp", p.Rev.Endpoint)
		if err != nil {
			return nil, coded(CodeDialFailed, err)
		}
		return &fiasAdapter{
			g:     &guardedConn{c: conn, writeTimeout: p.Rev.WriteTimeout},
			br:    bufio.NewReader(conn),
			iface: p.Iface, rev: p.Rev,
			evKey: evidenceKey, evKeyNo: evidenceKeyVersion, now: now,
		}, nil
	}
}

func (a *fiasAdapter) Close() error {
	if a.g != nil && a.g.c != nil {
		return a.g.c.Close()
	}
	return nil
}

// Serve runs the verified read-only protocol: LS/LD/LR startup handshake, then a read loop translating
// LS/LA/heartbeat into axis updates, GI/GC/GO into typed domain events, and DS..DE into resync state. On
// context cancel it sends LE (controlled shutdown). It NEVER emits PS/PA (guarded at writeFrame).
func (a *fiasAdapter) Serve(ctx context.Context, sink AxisSink) error {
	t := a.now()
	da, ti := t.Format("060102"), t.Format("150405")
	for _, body := range append([]string{pms.BuildLS(da, ti), pms.BuildLD(da, ti, "pmsd", "1")}, pms.BuildLRs()...) {
		if err := a.g.writeFrame(body); err != nil {
			return err
		}
	}
	if err := sink.OnConnected(a.now()); err != nil {
		return err
	}

	resyncing := false
	readDeadline := a.rev.HeartbeatTimeout
	for {
		if ctx.Err() != nil {
			_ = a.g.writeFrame(pms.BuildLE()) // controlled shutdown (best effort)
			return ctx.Err()
		}
		if readDeadline > 0 {
			_ = a.g.c.SetReadDeadline(time.Now().Add(readDeadline))
		}
		body, err := pms.ReadFramedRecord(a.br)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return coded(CodeProtocolLinkEnded, err)
			}
			return coded(CodeProtocolLinkEnded, err)
		}
		switch pms.RecordID(body) {
		case "LS":
			if err := a.g.writeFrame(pms.BuildLA()); err != nil { // ack incoming link start
				return err
			}
			if err := sink.OnHeartbeat(a.now()); err != nil {
				return err
			}
		case "LA", "HB":
			if err := sink.OnHeartbeat(a.now()); err != nil {
				return err
			}
		case "LE":
			return coded(CodeProtocolLinkEnded, nil)
		case "DS":
			resyncing = true
			if err := sink.OnResyncStart(a.now()); err != nil {
				return err
			}
		case "DE":
			resyncing = false
			if err := sink.OnResyncComplete(a.now(), pms.RecordID(body)); err != nil {
				return err
			}
		case "GI", "GC", "GO":
			ev := a.toEvent(body)
			if err := ev.Validate(); err != nil {
				// a malformed record is a framing/normalization fault, not a guest event
				continue
			}
			if derr := sink.OnDomainEvent(ctx, ev); derr != nil {
				if Classify(derr) == CodeQueueOverflow && !resyncing {
					// stop normal application; request a verified full resync
					if err := a.g.writeFrame(pms.BuildDR()); err != nil {
						return err
					}
					resyncing = true
					continue
				}
				if errors.Is(derr, ErrStaleGeneration) {
					return derr // a newer owner exists: terminate this ownership cycle
				}
				// other persist errors close the transport
				return derr
			}
		default:
			// ignore unknown/no-op records (handshake noise)
		}
	}
}

// toEvent parses a GI/GC/GO record into a typed, provenance-stamped domain Event. Raw frame bytes stay
// here; only bounded typed fields leave.
func (a *fiasAdapter) toEvent(body string) Event {
	f := pms.ParseFields(body)
	hash, ver := ComputeEvidenceHMAC(a.evKey, a.evKeyNo, body)
	return Event{
		InterfaceID: a.iface.ID, RevisionID: a.rev.ID, SecretGenerationID: a.rev.ActiveSecretGenerationID,
		NormalizationVer:      a.rev.NormalizationVersion,
		RecordType:            RecordType(pms.RecordID(body)),
		ExternalEventIdentity: clip(f["RN"], maxExtIdentityLen), // reservation number = external identity
		ReservationRef:        clip(f["RN"], maxReservationLen),
		RoomNumber:            clip(f["FL"], maxRoomLen),
		GuestName:             clip(f["GN"], maxGuestLen),
		PMSTimestampRaw:       clip(f["GA"], maxRawTimestampLen),
		NormalizedAt:          a.now(),
		Cursor:                clip(f["RN"], maxCursorLen),
		SourceEvidenceHash:    hash, EvidenceKeyVersion: ver,
		ReceivedAt: a.now(),
	}
}

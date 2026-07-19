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
	g         *guardedConn
	br        *bufio.Reader
	iface     Interface
	rev       Revision
	evKey     []byte
	evKeyNo   int
	identKey  []byte // dedicated PMS_EVENT_IDENTITY key (distinct purpose from evidence/encryption keys)
	identKeyN int
	profile   string
	now       func() time.Time
}

// AdapterKeys carries the two DISTINCT keyed-HMAC keys the adapter needs, each with its own purpose and
// generation/version. IdentityKey (PMS_EVENT_IDENTITY) derives the idempotency fingerprint; EvidenceKey
// derives the source-evidence provenance digest. Neither is an encryption key; neither is ever logged.
type AdapterKeys struct {
	IdentityKey        []byte
	IdentityKeyVersion int
	EvidenceKey        []byte
	EvidenceKeyVersion int
}

// NewFIASDial builds a Deps.Dial using an injected Dialer. It fails closed if the dedicated identity key is
// absent (an empty/invalid identity key must never silently produce un-fingerprinted events).
func NewFIASDial(dialer Dialer, keys AdapterKeys, now func() time.Time) func(context.Context, DialParams) (Conn, error) {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, p DialParams) (Conn, error) {
		if len(keys.IdentityKey) == 0 || keys.IdentityKeyVersion <= 0 {
			return nil, coded(CodeConfigInvalid, errors.New("missing PMS_EVENT_IDENTITY key"))
		}
		if len(keys.EvidenceKey) == 0 || keys.EvidenceKeyVersion <= 0 {
			return nil, coded(CodeConfigInvalid, errors.New("missing evidence key"))
		}
		conn, err := dialer(ctx, "tcp", p.Rev.Endpoint)
		if err != nil {
			return nil, coded(CodeDialFailed, err)
		}
		return &fiasAdapter{
			g:     &guardedConn{c: conn, writeTimeout: p.Rev.WriteTimeout},
			br:    bufio.NewReader(conn),
			iface: p.Iface, rev: p.Rev,
			evKey: keys.EvidenceKey, evKeyNo: keys.EvidenceKeyVersion,
			identKey: keys.IdentityKey, identKeyN: keys.IdentityKeyVersion,
			profile: "protel-fias/v1", now: now,
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
				// A malformed / overlong / identity-truncating record is a feed-continuity fault, NOT a
				// silently-droppable event. Drive continuity→GAP_DETECTED + sync→RESYNC_REQUIRED durably
				// (a persist/generation failure closes the transport), then request a verified full resync.
				// No shortened or partial value from this record is exposed to the queue/inbox.
				if ferr := sink.OnContinuityFault(ctx, CodeEventInvalid); ferr != nil {
					return ferr
				}
				if !resyncing {
					if werr := a.g.writeFrame(pms.BuildDR()); werr != nil {
						return werr
					}
					resyncing = true
				}
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

// toEvent parses a GI/GC/GO record into a typed, provenance-stamped domain Event using the AUTHORITATIVE
// Protel FIAS field map (Phase-0 live evidence):
//
//	RN = room number     G# = reservation number
//	GN = last name       GF = first name
//	GA = arrival date    GD = departure date
//
// Room number is NOT globally unique (it repeats across reservations/stays), so it is never an identity.
// The external Event identity is a deterministic content hash over the record type + reservation + room +
// arrival + departure (interface-scoped), so GI/GC/GO, repeated updates, Room Moves, and the same
// reservation across different lifecycle episodes never collide, and reservation number alone is not the
// idempotency key. Raw frame bytes stay here; only bounded typed fields leave.
func (a *fiasAdapter) toEvent(body string) Event {
	rt := RecordType(pms.RecordID(body))
	// The fingerprint is computed over the COMPLETE parsed record (every code+value in order, duplicates and
	// present-but-empty preserved, incl. unknown codes) — not just the typed fields — so any source-token
	// change alters it and an exact retransmission reproduces it.
	rawPairs := pms.ParseFieldPairs(body)
	pairs := make([]FieldPair, len(rawPairs))
	for i, p := range rawPairs {
		pairs[i] = FieldPair{Code: p[0], Value: p[1]}
	}
	se := newSourceEvent(a.iface.ID, rt, a.rev.NormalizationVersion, a.profile, "", pairs)
	fp, fpVer := ComputeSourceFingerprint(a.identKey, a.identKeyN, se)
	evHash, evVer := ComputeEvidenceHMAC(a.evKey, a.evKeyNo, body)

	// Typed fields are extracted (first occurrence per code) but NEVER truncated: an overlong PMS value is
	// left intact so Validate rejects it (→ the caller triggers a continuity gap/resync), rather than
	// silently changing it into a different valid-looking value. Display name is derived from the (validated)
	// typed components. Runtime/Resync generation are stamped later by the owning worker at admission.
	f := pms.ParseFields(body)
	reservation := f["G#"]
	room := f["RN"]
	last := f["GN"]
	first := f["GF"]

	now := a.now()
	return Event{
		InterfaceID: a.iface.ID, RevisionID: a.rev.ID, SecretGenerationID: a.rev.ActiveSecretGenerationID,
		NormalizationVer:       a.rev.NormalizationVersion,
		RecordType:             rt,
		SourceEventFingerprint: fp, FingerprintKeyVersion: fpVer, ExternalEventIdentity: fp,
		StayResolutionCandidate: DeriveStayResolutionCandidate(a.iface.TenantID, a.iface.SiteID, a.iface.ID, reservation),
		ReservationRef:          reservation,
		RoomNumber:              room,
		FolioRef:                f["FO"],
		GuestLastName:           last,
		GuestFirstName:          first,
		GuestName:               deriveDisplayName(last, first),
		ArrivalRaw:              f["GA"],
		DepartureRaw:            f["GD"],
		// GI/GC/GO carry no verified FIAS event timestamp -> PMSEvent* left unavailable (never substitute GA).
		PMSEventTimestampRaw: "",
		PMSEventAt:           nil,
		NormalizedAt:         now,
		Cursor:               reservation + ":" + room,
		SourceEvidenceHash:   evHash, EvidenceKeyVersion: evVer,
		ReceivedAt: now,
	}
}

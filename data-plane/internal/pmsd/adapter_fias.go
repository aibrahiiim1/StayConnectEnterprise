package pmsd

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
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
	// log carries the connector's redacted diagnostics. A nil logger is inert, so tests that construct an
	// adapter directly need not supply one.
	log *slog.Logger
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
//
// log may be nil, and is threaded in rather than taken from a package global because the connector's
// diagnostics are the only window onto why a feed is being rejected. Without it, a record the connector
// refuses is visible solely as a bounded code in pms_interface_runtime — which is how a live appliance came
// to sit CONNECTED, resyncing every few seconds, admitting nothing, and saying nothing about why.
func NewFIASDial(dialer Dialer, keys AdapterKeys, now func() time.Time, log *slog.Logger) func(context.Context, DialParams) (Conn, error) {
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
			profile: "protel-fias/v1", now: now, log: log,
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
	// The SINGLE serialized writer owns EVERY outbound frame (startup, LA acks, idle keepalive, DR, LE). The
	// read loop below NEVER touches the socket writer directly — it only submits requests. wctx cancellation
	// makes the writer emit a controlled LE and stop; the deferred drain guarantees no goroutine leak.
	wctx, wcancel := context.WithCancel(ctx)
	w := newSerialWriter(a.g, a.rev.HeartbeatInterval)
	go w.run(wctx)
	defer func() { wcancel(); <-w.done }()

	t := a.now()
	da, ti := t.Format("060102"), t.Format("150405")
	for _, body := range append([]string{pms.BuildLS(da, ti), pms.BuildLD(da, ti, "pmsd", "1")}, pms.BuildLRs()...) {
		if err := w.SubmitSync(ctx, body); err != nil { // startup handshake through the single writer
			return err
		}
	}
	if err := sink.OnConnected(a.now()); err != nil {
		return err
	}
	// §G/§H: raise the application barrier and request the INITIAL full resync through the single writer. Until
	// a complete DS→DE generation is published, no LIVE admission occurs. The DR is submitted (bounded, async
	// through the serialized writer) so the read loop can immediately begin consuming DS…DE; a DR write failure
	// closes the transport and ends the cycle via the read path.
	if err := sink.RequireInitialResync(a.now()); err != nil {
		return err
	}
	if err := w.Submit(ctx, pms.BuildDR()); err != nil {
		return err
	}

	resyncing := true // a DR is outstanding; we are awaiting DS…DE (gates duplicate DR requests only)
	// skippedNoIdentity counts well-formed records this ownership cycle carried that describe no keyable Stay.
	// It is a running total rather than a per-resync one on purpose: the number an operator wants is "how much
	// of this roster is unusable", and a counter that reset on every DS would only ever show the tail.
	skippedNoIdentity := 0
	readDeadline := a.rev.HeartbeatTimeout
	for {
		if ctx.Err() != nil {
			return ctx.Err() // the writer emits LE + closes the socket on wctx cancel (deferred)
		}
		if readDeadline > 0 {
			_ = a.g.c.SetReadDeadline(time.Now().Add(readDeadline))
		}
		body, err := pms.ReadFramedRecord(a.br)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // cancellation closed the socket to release this read promptly
			}
			// a writer failure closes the transport to unblock this read; surface the writer's cause if any
			select {
			case <-w.done:
				return w.stoppedErr()
			default:
			}
			return coded(CodeProtocolLinkEnded, err)
		}

		// STRICT-PARSE EVERY inbound frame BEFORE any action (idle reset, heartbeat, ack, resync, dispatch).
		pr, perr := parseStrictRecord(body)
		if perr != nil {
			if intendedDomain(body) {
				// malformed DOMAIN record: recoverable feed fault → atomic gap/resync, zero admission, DR while
				// owned, barrier stays active. No idle reset, no heartbeat, no ack for an invalid record.
				if a.log != nil {
					a.log.Error("pmsd: unparseable domain record — continuity fault",
						"interface", a.iface.ID, "reason", "strict_parse")
				}
				if ferr := sink.OnContinuityFault(ctx, CodeEventInvalid); ferr != nil {
					return ferr
				}
				if !resyncing {
					if werr := w.SubmitSync(ctx, pms.BuildDR()); werr != nil {
						return werr
					}
					resyncing = true
				}
				continue
			}
			// malformed / unsupported CONTROL record (or unknown record id): a protocol-level fault. Do NOT
			// reset the idle timer, record a heartbeat, or acknowledge it — terminate the ownership cycle with
			// a bounded typed code. No raw payload is logged.
			return coded(CodeProtocolFraming, nil)
		}

		w.activity() // a VALID (strictly-parsed) frame resets the idle keepalive timer
		switch pr.RecordType {
		case RecLS, RecLA:
			// Record the heartbeat evidence we OBSERVED first (receiving a valid LS/LA is the evidence), then
			// synchronously acknowledge with a bare LA. Recording must not depend on our ack succeeding.
			if err := sink.OnHeartbeat(a.now()); err != nil {
				return err
			}
			if err := w.SubmitSync(ctx, pms.BuildLA()); err != nil { // incoming LS/LA → bare LA
				return err
			}
		case RecLE:
			return coded(CodeProtocolLinkEnded, nil)
		case RecDS:
			resyncing = true
			if err := sink.OnResyncStart(a.now()); err != nil {
				return err
			}
		case RecDE:
			resyncing = false
			if err := sink.OnResyncComplete(a.now(), ""); err != nil { // never use the record id "DE" as a cursor
				return err
			}
		case RecGI, RecGC, RecGO:
			ev, perr := a.toEvent(body)
			if perr == nil {
				perr = ev.Validate()
			}
			// A well-formed record with no Stay identity is SKIPPED, not faulted. It cannot be admitted — there
			// is nothing to key a Stay on — but it is normal content of a real in-house roster (house-use and
			// out-of-order rooms), and treating it as evidence of a broken feed makes every resync request the
			// resync that will contain it again. See ErrIdentityAbsent.
			//
			// Logged at WARN, with the record type and nothing else: the count of skipped records over a resync
			// is the operational signal, and no guest value, room or reservation belongs in a log line.
			if errors.Is(perr, ErrIdentityAbsent) {
				skippedNoIdentity++
				if a.log != nil {
					a.log.Warn("pmsd: skipping record with no Stay identity",
						"record_type", string(pr.RecordType), "interface", a.iface.ID,
						"missing", missingIdentityField(perr), "skipped_this_cycle", skippedNoIdentity)
				}
				continue
			}
			if perr != nil {
				if a.log != nil {
					// The reason is a bounded classification, never the payload: enough to tell a duplicate
					// identity field apart from a failed fingerprint or an overlong value, which is the
					// difference between a PMS-side problem and a key/configuration problem on our side.
					a.log.Error("pmsd: domain record rejected — continuity fault",
						"record_type", string(pr.RecordType), "interface", a.iface.ID,
						"reason", classifyDomainReject(perr))
				}
				// A malformed domain record (ambiguous duplicate field, overlong or identity-truncating value)
				// is a feed-continuity fault, NOT a silently-droppable event. Drive continuity→GAP_DETECTED +
				// sync→RESYNC_REQUIRED durably (a persist/generation failure closes the transport), then request
				// a verified full resync. No shortened or partial value reaches the queue/inbox — toEvent
				// returned no Event.
				if ferr := sink.OnContinuityFault(ctx, CodeEventInvalid); ferr != nil {
					return ferr
				}
				if !resyncing {
					if werr := w.SubmitSync(ctx, pms.BuildDR()); werr != nil {
						return werr
					}
					resyncing = true
				}
				continue
			}
			// The sink routes the Event through the §H barrier + durable inbox (stage while resyncing, hold
			// while the barrier is up, admit durably when synced). The durable row is authoritative — there is
			// no in-memory-queue-overflow gap here. Any DB/ownership failure closes the transport.
			if derr := sink.OnDomainEvent(ctx, ev); derr != nil {
				return derr // ErrStaleGeneration or any persist error terminates the ownership cycle
			}
		}
	}
}

// toEvent STRICTLY parses a GI/GC/GO record into a typed, provenance-stamped domain Event using the
// AUTHORITATIVE Protel FIAS field map (Phase-0 live evidence):
//
//	RN = room number     G# = reservation number
//	GN = last name       GF = first name
//	GA = arrival date    GD = departure date
//
// It fails closed (returns ErrRecordMalformed) on any strict-grammar violation OR ambiguous duplicate typed
// field, so a malformed record produces NO Event and the caller drives a continuity gap/resync. Room number
// is NOT globally unique, so it is never an identity. The external Event identity is a keyed HMAC over the
// STRICT COMPLETE parsed source record (every code+value in source order, duplicates and present-but-empty
// preserved, incl. unknown well-formed codes) — never a reservation/room/date-only hash — so GI/GC/GO,
// repeated updates, Room Moves, and the same reservation across lifecycle episodes never collide, and
// reservation number alone is not the idempotency key. Raw frame bytes stay here; only bounded typed fields
// leave, and none is ever truncated (an overlong value is rejected upstream by Validate, not shortened).
func (a *fiasAdapter) toEvent(body string) (Event, error) {
	pr, err := parseStrictRecord(body)
	if err != nil {
		return Event{}, err
	}
	// The fingerprint is computed over the COMPLETE strict parsed record (order, duplicates, present-but-empty
	// and unknown well-formed codes preserved) — not just the typed fields — so any source-token change alters
	// it and an exact retransmission reproduces it.
	se := newSourceEvent(a.iface.ID, pr.RecordType, a.rev.NormalizationVersion, a.profile, "", pr.pairs())
	fp, fpVer := ComputeSourceFingerprint(a.identKey, a.identKeyN, se)
	evHash, evVer := ComputeEvidenceHMAC(a.evKey, a.evKeyNo, body)

	// Typed projection fails closed on ambiguous duplicate identity/evidence fields (RN/G# exactly once;
	// GN/GF/FO/GA/GD at most once). Values are NEVER truncated. Runtime/Resync generation are stamped later
	// by the owning worker at admission.
	tf, err := extractTypedDomainFields(pr)
	if err != nil {
		return Event{}, err
	}

	now := a.now()
	return Event{
		InterfaceID: a.iface.ID, RevisionID: a.rev.ID, SecretGenerationID: a.rev.ActiveSecretGenerationID,
		NormalizationVer:       a.rev.NormalizationVersion,
		RecordType:             pr.RecordType,
		SourceEventFingerprint: fp, FingerprintKeyVersion: fpVer, ExternalEventIdentity: fp,
		StayResolutionCandidate: DeriveStayResolutionCandidate(a.iface.TenantID, a.iface.SiteID, a.iface.ID, tf.Reservation),
		ReservationRef:          tf.Reservation,
		RoomNumber:              tf.Room,
		FolioRef:                tf.Folio,
		GuestLastName:           tf.LastName,
		GuestFirstName:          tf.FirstName,
		ArrivalRaw:              tf.Arrival,
		DepartureRaw:            tf.Departure,
		// GI/GC/GO carry no verified FIAS event timestamp -> PMSEvent* left unavailable (never substitute GA).
		PMSEventTimestampRaw: "",
		PMSEventAt:           nil,
		NormalizedAt:         now,
		Cursor:               tf.Reservation + ":" + tf.Room,
		SourceEvidenceHash:   evHash, EvidenceKeyVersion: evVer,
		ReceivedAt: now,
	}, nil
}

// classifyDomainReject maps a domain-record rejection to a bounded reason token for logs. It exists so a
// continuity fault can be diagnosed at all: before this, the only trace a rejected record left was the string
// "EVENT_INVALID" in last_sync_failure_code, which is the same value for a duplicate room field, an overlong
// guest name and a mis-derived fingerprint — three problems with three different owners and three different
// fixes. The tokens are fixed strings; no payload, value or guest field ever appears in one.
func classifyDomainReject(err error) string {
	switch {
	case errors.Is(err, ErrIdentityAbsent):
		return "identity_absent"
	case errors.Is(err, ErrRecordMalformed):
		return "identity_ambiguous_or_malformed"
	case errors.Is(err, ErrEventInvalid):
		return "event_validation_failed"
	default:
		return "unclassified"
	}
}

// missingIdentityField reports which mandatory identity code an ErrIdentityAbsent rejection was missing, or
// "unknown" if the error carries no detail. Protocol field codes only — never a value.
func missingIdentityField(err error) string {
	var ia identityAbsentError
	if errors.As(err, &ia) {
		return ia.Missing
	}
	return "unknown"
}

package pmsd

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"sort"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// Dialer is the only way the adapter obtains a socket; tests inject net.Pipe or a fake listener.
type Dialer func(ctx context.Context, network, address string) (net.Conn, error)

// guardedConn is the sole outbound write path: every frame passes CheckOutbound first, there is no exported raw Write,
// and a blocked record writes zero bytes.
type guardedConn struct {
	c            net.Conn
	writeTimeout time.Duration
	st           *linkStats
}

func (g *guardedConn) writeFrame(body string) error {
	if err := CheckOutbound(pms.RecordID(body)); err != nil {
		return coded(CodeOutboundBlocked, err)
	}
	if g.writeTimeout > 0 {
		_ = g.c.SetWriteDeadline(time.Now().Add(g.writeTimeout))
	}
	return g.write(body)
}

func (g *guardedConn) write(body string) error {
	if err := pms.WriteFramedRecord(g.c, body); err != nil {
		return wrapTransport(transportOpWrite, err)
	}
	g.st.outbound(pms.RecordID(body))
	return nil
}

// writeFinancialFrame is the only path by which a PS reaches the wire: it accepts a PS record only (never PA, control
// or domain), and only from the serialized writer for a request the financial relay submitted after the database
// confirmed the exact bytes. The read-only allowlist still refuses PS and PA for everyone else.
func (g *guardedConn) writeFinancialFrame(body string) error {
	if pms.RecordID(body) != "PS" || !IsFinancialRecord("PS") {
		return coded(CodeOutboundBlocked, ErrOutboundNotAllowed{Record: pms.RecordID(body)})
	}
	if g.writeTimeout > 0 {
		_ = g.c.SetWriteDeadline(time.Now().Add(g.writeTimeout))
	}
	return g.write(body)
}

// fiasAdapter reuses internal/pms framing and parsing (no second parser) and drives interface axes and the typed
// domain-event queue via the AxisSink.
type fiasAdapter struct {
	g         *guardedConn
	br        *bufio.Reader
	iface     Interface
	rev       Revision
	evKey     []byte
	evKeyNo   int
	identKey  []byte
	identKeyN int
	profile   string
	now       func() time.Time
	// log carries redacted diagnostics; nil is inert.
	log *slog.Logger
	// relay, when non-nil, lets the financial path hand a posting command to this link; nil unless room-charge
	// transmission is deployed.
	relay *FinancialRelay
}

// AdapterKeys carries two distinct keyed-HMAC keys: IdentityKey (PMS_EVENT_IDENTITY) derives the idempotency
// fingerprint, EvidenceKey the source-evidence digest. Neither is an encryption key or ever logged.
type AdapterKeys struct {
	IdentityKey        []byte
	IdentityKeyVersion int
	EvidenceKey        []byte
	EvidenceKeyVersion int
}

// NewFIASDial fails closed if the identity key is absent: events must never be un-fingerprinted. log may be nil; it is
// threaded in because connector diagnostics are the only window onto why a feed is rejected (otherwise an appliance
// sits CONNECTED, resyncing and admitting nothing, silently).
func NewFIASDial(dialer Dialer, keys AdapterKeys, now func() time.Time, log *slog.Logger) func(context.Context, DialParams) (Conn, error) {
	return NewFIASDialWithRelay(dialer, keys, now, log, nil)
}

// NewFIASDialWithRelay attaches the financial relay so each link can carry authorised posting commands; a nil relay is
// NewFIASDial.
func NewFIASDialWithRelay(dialer Dialer, keys AdapterKeys, now func() time.Time, log *slog.Logger, relay *FinancialRelay) func(context.Context, DialParams) (Conn, error) {
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
		st := newLinkStats(conn)
		conn = &countingConn{Conn: conn, st: st}
		if p.Rev.TLS {
			if conn, err = fiasTLS(ctx, conn, p.Rev); err != nil {
				return nil, coded(CodeDialFailed, err)
			}
		}
		return &fiasAdapter{
			g:     &guardedConn{c: conn, writeTimeout: p.Rev.WriteTimeout, st: st},
			br:    bufio.NewReader(conn),
			iface: p.Iface, rev: p.Rev,
			evKey: keys.EvidenceKey, evKeyNo: keys.EvidenceKeyVersion,
			identKey: keys.IdentityKey, identKeyN: keys.IdentityKeyVersion,
			profile: "protel-fias/v1", now: now, log: log, relay: relay,
		}, nil
	}
}

func (a *fiasAdapter) linkDiagnostics() LinkDiagnostics { return a.g.st.snapshot() }

func (a *fiasAdapter) Close() error {
	if a.g != nil && a.g.c != nil {
		return a.g.c.Close()
	}
	return nil
}

// Serve runs the verified read-only protocol (LS/LD/LR handshake, read loop, DS..DE resync) and sends LE on cancel. It
// never emits PS/PA (guarded at writeFrame).
func (a *fiasAdapter) Serve(ctx context.Context, sink AxisSink) error {
	// A single serialized writer owns every outbound frame; the read loop only submits requests. wctx cancellation
	// emits a controlled LE and stops; the deferred drain prevents goroutine leaks.
	wctx, wcancel := context.WithCancel(ctx)
	w := newSerialWriter(a.g, a.rev.HeartbeatInterval)
	go w.run(wctx)
	defer func() { wcancel(); <-w.done }()
	defer a.g.st.freeze() // runs before the writer is stopped

	t := a.now()
	da, ti := t.Format("060102"), t.Format("150405")
	for _, body := range append([]string{pms.BuildLS(da, ti), pms.VerifiedLD(da, ti)}, pms.BuildLRs()...) {
		if err := w.SubmitSync(ctx, body); err != nil {
			return failedAt(stepHandshake, err)
		}
	}
	if err := sink.OnConnected(a.now()); err != nil {
		return failedAt(stepOnConnected, err)
	}
	// The link is offered to the financial relay only after the handshake and withdrawn when the cycle ends; a command
	// waiting then resolves UNKNOWN because its PS may already be with the PMS. The port starts NOT steady: nothing is
	// carried until a full DS..DE generation is published.
	var port *relayPort
	if a.relay != nil {
		port = a.relay.attach(a.iface.ID, w.SubmitFinancial)
		defer a.relay.detach(port)
	}
	// Raise the application barrier and request the initial full resync through the single writer; no LIVE admission
	// occurs until a complete DS->DE generation is published. A DR write failure closes the transport and ends the
	// cycle.
	if err := sink.RequireInitialResync(a.now()); err != nil {
		return failedAt(stepRequireResync, err)
	}
	if err := w.Submit(ctx, pms.BuildDR()); err != nil {
		return failedAt(stepWriteResyncReq, err)
	}
	// Say the automatic sync is waiting on the PMS, so an operator who re-plugged the socket doesn't see
	// REQUESTING_FULL_SYNC forever.
	sink.OnFullSyncRequested()

	resyncing := true
	var fin finResyncScheduler
	// Per-sweep observation, reset at every DS: two sets rather than counters, so a sweep that reports a room both
	// occupied and empty is detected and cannot close a guest's stay.
	occupiedSeen := map[string]struct{}{}
	vacantSeen := map[string]struct{}{}
	rosterRecords := 0
	vacantRecords := 0
	// skippedNoIdentity counts well-formed records describing no keyable Stay since the current roster started.
	skippedNoIdentity := 0
	lastReportedSkipped := 0
	readDeadline := a.rev.HeartbeatTimeout
	for {
		if port != nil {
			// A posting command is carried only between resyncs, never inside a DS..DE window.
			port.setSteady(!resyncing)
		}
		a.g.st.setResyncing(resyncing)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if readDeadline > 0 {
			_ = a.g.c.SetReadDeadline(time.Now().Add(readDeadline))
		}
		body, err := pms.ReadFramedRecord(a.br)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, pms.ErrFrameTooLong) {
				// An unterminated or oversized frame: the stream is no longer framed. No payload is logged.
				return failedAt(stepRead, coded(CodeProtocolFraming, err))
			}
			// a writer failure closes the transport to unblock this read; surface the writer's cause if any
			select {
			case <-w.done:
				return failedAt(stepRead, w.stoppedErr())
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				// Only the writer closes the socket while ctx is live, and it records its cause just before; wait
				// for it so a write failure is not reported as a local close.
				select {
				case <-w.done:
					return failedAt(stepRead, w.stoppedErr())
				case <-time.After(time.Second):
				}
			}
			return failedAt(stepRead, wrapTransport(transportOpRead, err))
		}
		a.g.st.inbound(body)

		// Strict-parse every inbound frame before any action.
		pr, perr := parseStrictRecord(body)
		if perr != nil {
			if intendedDomain(body) {
				// Malformed domain record, a recoverable feed fault: atomic gap/resync, zero admission, DR while owned,
				// barrier stays active; no idle reset, heartbeat or ack.
				if a.log != nil {
					a.log.Error("pmsd: unparseable domain record — continuity fault",
						"interface", a.iface.ID, "reason", "strict_parse")
				}
				if ferr := sink.OnContinuityFault(ctx, CodeEventInvalid); ferr != nil {
					return failedAt(stepContinuityFault, ferr)
				}
				if !resyncing {
					if werr := w.SubmitSync(ctx, pms.BuildDR()); werr != nil {
						return failedAt(stepWriteResyncReq, werr)
					}
					resyncing = true
				}
				continue
			}
			// A malformed or unknown control record is a protocol fault: don't reset idle, record a heartbeat or ack;
			// terminate the cycle with a bounded typed code. No raw payload is logged.
			return failedAt(stepParseControl, coded(CodeProtocolFraming, nil))
		}

		w.activity()

		// Operator-requested full resync is checked here only: the read loop owns the socket, so the DR goes through
		// the same serialized writer; edged only writes a row and this claims it. `resyncing` gates it like the
		// fault-driven DR, since a second window over an open DS..DE would corrupt it; the claim also refuses in that
		// state.
		if !resyncing {
			if cmd := sink.ClaimOperatorResync(); cmd != nil {
				if a.log != nil {
					a.log.Info("pmsd: operator requested full resync",
						"interface", a.iface.ID, "command", cmd.ID, "reason", cmd.Reason)
				}
				if werr := w.SubmitSync(ctx, pms.BuildDR()); werr != nil {
					return failedAt(stepWriteResyncReq, werr)
				}
				sink.OnFullSyncRequested()
				resyncing = true
			}
		}
		// The financial mirror's own resync, from the same place under the same gate.
		if asker, ok := sink.(FinancialResyncAsker); ok {
			now := a.now()
			if fin.unanswered(now) && resyncing {
				// Our DR was never begun: release the gate so an operator resync and posting are not held; the mirror
				// age keeps running and room charge fails closed at the bound.
				resyncing = false
				if a.log != nil {
					a.log.Warn("pmsd: the PMS did not begin the financial mirror resync; releasing and backing off",
						"interface", a.iface.ID)
				}
			}
			if fin.shouldRequest(now, resyncing, asker.FinancialResyncDue) {
				if werr := w.SubmitSync(ctx, pms.BuildDR()); werr != nil {
					return failedAt(stepWriteResyncReq, werr)
				}
				sink.OnFullSyncRequested()
				resyncing = true
				fin.requested(now)
				if a.log != nil {
					a.log.Info("pmsd: financial mirror resync requested", "interface", a.iface.ID)
				}
			}
		}
		switch pr.RecordType {
		case RecLS, RecLA:
			// Record the heartbeat evidence first; recording must not depend on our ack succeeding.
			if err := sink.OnHeartbeat(a.now()); err != nil {
				return failedAt(stepHeartbeat, err)
			}
			if err := w.SubmitSync(ctx, pms.BuildLA()); err != nil {
				return failedAt(stepWriteAck, err)
			}
		case RecLE:
			return failedAt(stepInboundLinkEnd, coded(CodeProtocolLinkEnded, nil))
		case RecPA:
			// A posting answer goes verbatim to the command waiting for this P#, never interpreted here; an unawaited
			// PA is reported without content and dropped.
			if port == nil || !port.onPA(body) {
				if a.log != nil {
					a.log.Warn("pmsd: posting answer with no waiting command ignored", "interface", a.iface.ID)
				}
			}
		case RecDS:
			resyncing = true
			fin.started()
			// Count what the sweep sees, including vacant rooms that are never admitted as departures: that description
			// is the only thing that proves a roster COMPLETE, so it must not depend on the admission rule.
			occupiedSeen = map[string]struct{}{}
			vacantSeen = map[string]struct{}{}
			rosterRecords = 0
			vacantRecords = 0
			// Per roster, not per connection: reset here so a clean roster's skipped count isn't inherited from an
			// earlier sync.
			skippedNoIdentity = 0
			lastReportedSkipped = 0
			if err := sink.OnResyncStart(a.now()); err != nil {
				return failedAt(stepResyncStart, err)
			}
		case RecDE:
			// Record the observation before the publish: a generation without one could never be judged complete and
			// reconciliation would defer forever.
			if resyncing {
				// The union is the building the sweep described, the intersection where it contradicted itself;
				// reconciliation must refuse to close anything while it is self-inconsistent.
				union := make([]string, 0, len(occupiedSeen)+len(vacantSeen))
				conflicts := 0
				for r := range occupiedSeen {
					union = append(union, r)
					if _, both := vacantSeen[r]; both {
						conflicts++
					}
				}
				for r := range vacantSeen {
					if _, dup := occupiedSeen[r]; !dup {
						union = append(union, r)
					}
				}
				sort.Strings(union)
				if conflicts > 0 && a.log != nil {
					a.log.Warn("pmsd: sweep reported rooms as both occupied and empty; reconciliation will refuse",
						"interface", a.iface.ID, "conflicting_rooms", conflicts)
				}
				sink.RecordCoverage(union, rosterRecords, vacantRecords, conflicts)
			}
			// Flush the exact skipped total before the publish stamps COMPLETE.
			if skippedNoIdentity != lastReportedSkipped {
				lastReportedSkipped = skippedNoIdentity
				sink.RecordSkipped(int64(skippedNoIdentity))
			}
			resyncing = false
			fin.completed()
			if err := sink.OnResyncComplete(a.now(), ""); err != nil {
				return failedAt(stepPublishResync, err)
			}
		case RecGI, RecGC, RecGO:
			// A roster snapshot is not a departure announcement: a GO inside a DS..DE window with no reservation number
			// cannot say somebody left, and admitting those built a review backlog that could never resolve (a room
			// number is not an identity). Such records are skipped, counted and reported, never faulted.
			// Real departures arrive LIVE with G#, and a stay that ended while the link was down is closed by roster
			// reconciliation against the complete generation. The live room-only path is dormant (no live GO lacked
			// G#); it stays as a safety net for a PMS that might announce a departure without a reservation.
			if pr.RecordType == RecGO && resyncing && reservationAbsent(pr) {
				// Observed, then refused: the room is part of the building the sweep described; the record is not an
				// announced departure.
				if room := roomOf(pr); room != "" {
					vacantSeen[room] = struct{}{}
					vacantRecords++
				}
				skippedNoIdentity++
				if a.log != nil {
					a.log.Warn("pmsd: skipping roster-snapshot departure with no reservation",
						"record_type", string(pr.RecordType), "interface", a.iface.ID,
						"missing", fcReservation, "skipped_this_cycle", skippedNoIdentity)
				}
				if skippedNoIdentity-lastReportedSkipped >= 25 {
					lastReportedSkipped = skippedNoIdentity
					sink.RecordSkipped(int64(skippedNoIdentity))
				}
				continue
			}
			ev, perr := a.toEvent(body)
			if perr == nil {
				perr = ev.Validate()
			}
			if perr == nil && a.relay != nil {
				// Update the financial relay's view before admission so a room move stops a stale posting command at
				// the writer.
				a.relay.ObserveGuestRecord(a.iface.ID, pr.RecordType, ev.ReservationRef, ev.RoomNumber)
			}
			// A well-formed record with no Stay identity is skipped, not faulted: it is normal in a real roster
			// (house-use, out-of-order rooms), and faulting makes every resync request the resync that will contain it
			// again. See ErrIdentityAbsent. Logged at WARN with the record type only: no guest value, room or
			// reservation belongs in a log line.
			if errors.Is(perr, ErrIdentityAbsent) {
				skippedNoIdentity++
				if a.log != nil {
					a.log.Warn("pmsd: skipping record with no Stay identity",
						"record_type", string(pr.RecordType), "interface", a.iface.ID,
						"missing", missingIdentityField(perr), "skipped_this_cycle", skippedNoIdentity)
				}
				// Surfaced durably in batches so an operator sees the rejected count next to the received one, without
				// a write per skip on an already troubled feed.
				if skippedNoIdentity-lastReportedSkipped >= 25 {
					lastReportedSkipped = skippedNoIdentity
					sink.RecordSkipped(int64(skippedNoIdentity))
				}
				continue
			}
			if perr != nil {
				if a.log != nil {
					// The reason is a bounded classification, never the payload: it separates a PMS-side problem from a
					// key/configuration problem on our side.
					a.log.Error("pmsd: domain record rejected — continuity fault",
						"record_type", string(pr.RecordType), "interface", a.iface.ID,
						"reason", classifyDomainReject(perr))
				}
				// A malformed domain record (duplicate field, overlong or identity-truncating value) is a
				// feed-continuity fault, not a droppable event: drive GAP_DETECTED and RESYNC_REQUIRED durably (a
				// persist failure closes the transport), then request a verified full resync. No partial value reaches
				// the queue.
				if ferr := sink.OnContinuityFault(ctx, CodeEventInvalid); ferr != nil {
					return failedAt(stepContinuityFault, ferr)
				}
				if !resyncing {
					if werr := w.SubmitSync(ctx, pms.BuildDR()); werr != nil {
						return failedAt(stepWriteResyncReq, werr)
					}
					resyncing = true
				}
				continue
			}
			// The sink routes the Event through the barrier and durable inbox (stage while resyncing, hold while the
			// barrier is up, admit when synced); the durable row is authoritative. Any DB or ownership failure closes
			// the transport.
			if resyncing && ev.RoomNumber != "" {
				// Same contract as the vacant branch; see roomKey.
				occupiedSeen[roomKey(ev.RoomNumber)] = struct{}{}
				if pr.RecordType != RecGO {
					rosterRecords++
				}
			}
			if derr := sink.OnDomainEvent(ctx, ev); derr != nil {
				return failedAt(stepDomainRecord, derr) // ErrStaleGeneration or any persist error terminates the ownership cycle
			}
		}
	}
}

// toEvent strictly parses a GI/GC/GO record with the authoritative Protel field map (RN room, G# reservation, GN last
// name, GF first name, GA arrival, GD departure). It fails closed (ErrRecordMalformed) on a grammar violation or
// duplicate typed field, producing no Event. Room number is never an identity; the Event identity is a keyed HMAC over
// the complete parsed source record (every code and value in order, duplicates and empties preserved), so lifecycle
// episodes, room moves and repeated updates never collide. Only bounded typed fields leave, never truncated (an
// overlong value is rejected by Validate).
func (a *fiasAdapter) toEvent(body string) (Event, error) {
	pr, err := parseStrictRecord(body)
	if err != nil {
		return Event{}, err
	}
	// The fingerprint covers the complete strict parsed record, so any source-token change alters it and an exact
	// retransmission reproduces it.
	se := newSourceEvent(a.iface.ID, pr.RecordType, a.rev.NormalizationVersion, a.profile, "", pr.pairs())
	fp, fpVer := ComputeSourceFingerprint(a.identKey, a.identKeyN, se)
	evHash, evVer := ComputeEvidenceHMAC(a.evKey, a.evKeyNo, body)

	// Typed projection fails closed on duplicate identity/evidence fields (RN/G# exactly once; GN/GF/FO/GA/GD at most
	// once); values are never truncated. Generations are stamped later by the owning worker.
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
		TravelAgent:             tf.TravelAgent,
		VIP:                     tf.VIP,
		ArrivalRaw:              tf.Arrival,
		DepartureRaw:            tf.Departure,
		// GI/GC/GO carry no verified FIAS event timestamp, so PMSEvent* stay unavailable (never substitute GA).
		PMSEventTimestampRaw: "",
		PMSEventAt:           nil,
		NormalizedAt:         now,
		Cursor:               tf.Reservation + ":" + tf.Room,
		SourceEvidenceHash:   evHash, EvidenceKeyVersion: evVer,
		ReceivedAt: now,
	}, nil
}

// classifyDomainReject maps a rejection to a fixed bounded token for logs so a continuity fault can be diagnosed:
// EVENT_INVALID alone cannot separate a duplicate field, an overlong name and a mis-derived fingerprint. No payload
// ever appears in a token.
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

// missingIdentityField reports which mandatory identity code was missing, or "unknown"; protocol codes only, never a
// value.
func missingIdentityField(err error) string {
	var ia identityAbsentError
	if errors.As(err, &ia) {
		return ia.Missing
	}
	return "unknown"
}

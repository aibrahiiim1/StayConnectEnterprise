package pmsd

// THE POLLED REST CONNECTOR.
//
// One Conn for every REST provider (Mews, Apaleo, OPERA Cloud). The provider-specific part -- authentication,
// endpoints, paging, field mapping -- lives in internal/pmsrest; this file maps what those clients return onto
// the SAME sink contract the FIAS adapter drives, so the Stay Engine, the §H application barrier, the resync
// generations and roster reconciliation are shared rather than re-implemented:
//
//	connect    authenticate + one bounded read, then OnConnected. A refused credential never reports CONNECTED.
//	barrier    RequireInitialResync, exactly as FIAS does on link start.
//	full sync  OnResyncStart (allocates a generation) -> page the COMPLETE in-house list -> stage one GI per
//	           reservation -> RecordCoverage when the provider can enumerate rooms -> OnResyncComplete
//	           (publishes the generation). Repeated every complete_sync_ms, and on an operator's request.
//	live       every heartbeat_interval_ms: read what changed (or, for a provider with no change feed, the
//	           in-house list again) and admit GI for a new check-in, GC for a change to a known in-house stay
//	           (a room move included), GO for a check-out. Each successful poll is the keep-alive.
//	failure    a request is retried inside pmsrest (429 honouring Retry-After, 408/5xx/network with bounded
//	           backoff). A refused credential ends the cycle at once. Any other poll failure is tolerated until
//	           no poll has succeeded for heartbeat_timeout_ms; a failed full sync ends the cycle. The worker's
//	           reconnect backoff then applies, and the last published roster keeps serving meanwhile.
//
// Nothing here writes to a PMS. Nothing here writes a Stay directly. Every write is a durable inbox row keyed by
// this interface's id, through the sink.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsrest"
)

// RESTDialDeps are the injectable effects of the REST connector.
type RESTDialDeps struct {
	Keys AdapterKeys
	Now  func() time.Time
	Log  *slog.Logger
	// NewClient builds the provider client. Defaults to pmsrest.New; tests inject a client pointed at an
	// httptest server.
	NewClient func(kind string, provider map[string]any, secret []byte, o pmsrest.Options) (pmsrest.Client, error)
	// HTTP overrides the HTTP client (tests). Production builds one per connection with the revision's timeout.
	HTTP *http.Client
	// Sleep overrides waiting (tests): both the poll wait and pmsrest's retry backoff.
	Sleep func(ctx context.Context, d time.Duration) error
}

// changeOverlap re-reads a little of the previous window on every poll, so a change the provider committed
// just before our last read, or a small clock difference, is never missed. Re-reading an unchanged
// reservation admits nothing: the connector compares content before emitting.
const changeOverlap = 2 * time.Minute

// NewRESTDial builds the Deps.Dial for REST connector kinds. Dial performs no network I/O: it validates the
// pinned revision, parses the provider configuration and credential, and returns a Conn whose Serve does the
// work. It fails closed without the identity/evidence keys, like the FIAS dial.
func NewRESTDial(d RESTDialDeps) func(context.Context, DialParams) (Conn, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewClient == nil {
		d.NewClient = pmsrest.New
	}
	return func(ctx context.Context, p DialParams) (Conn, error) {
		if len(d.Keys.IdentityKey) == 0 || d.Keys.IdentityKeyVersion <= 0 {
			return nil, coded(CodeConfigInvalid, errors.New("missing PMS_EVENT_IDENTITY key"))
		}
		if len(d.Keys.EvidenceKey) == 0 || d.Keys.EvidenceKeyVersion <= 0 {
			return nil, coded(CodeConfigInvalid, errors.New("missing evidence key"))
		}
		prov, ok := pmsprovider.Get(p.Rev.ConnectorKind)
		if !ok || !prov.IsREST() {
			return nil, coded(CodeRevisionInvalid, errors.New("not a REST connector kind"))
		}
		if !p.Rev.RequiresSecret() {
			return nil, coded(CodeRevisionInvalid, errors.New("a REST connector requires AUTH_KEY"))
		}
		cfg, err := pmsprovider.ParseStoredProvider(prov, p.Rev.ProviderConfig)
		if err != nil {
			return nil, coded(CodeConfigInvalid, err)
		}
		loc, err := loadLocation(p.Rev.SourceTimezone)
		if err != nil {
			return nil, coded(CodeConfigInvalid, errors.New("invalid source timezone"))
		}
		hc := d.HTTP
		if hc == nil {
			hc = &http.Client{Timeout: p.Rev.ReadTimeout}
		}
		// The secret is copied into the client's own strings by pmsrest.New, so the worker zeroing its buffer
		// after Dial returns cannot corrupt the credential mid-connection.
		client, err := d.NewClient(prov.Kind, cfg, p.Secret.Bytes(), pmsrest.Options{
			HTTP: hc, Now: d.Now, Location: loc, Sleep: d.Sleep,
		})
		if err != nil {
			return nil, coded(CodeConfigInvalid, err)
		}
		sleep := d.Sleep
		if sleep == nil {
			sleep = func(ctx context.Context, t time.Duration) error {
				tm := time.NewTimer(t)
				defer tm.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-tm.C:
					return nil
				}
			}
		}
		return &restConn{
			client: client, iface: p.Iface, rev: p.Rev, kind: prov.Kind,
			identKey: d.Keys.IdentityKey, identKeyN: d.Keys.IdentityKeyVersion,
			evKey: d.Keys.EvidenceKey, evKeyN: d.Keys.EvidenceKeyVersion,
			profile: prov.Kind + "/rest-v1", now: d.Now, log: d.Log, sleep: sleep,
			known: map[string]knownStay{},
		}, nil
	}
}

// knownStay is what the connector last admitted for an in-house reservation: the room (a GO must name it)
// and a digest of every admitted field, so an unchanged re-read emits nothing.
type knownStay struct {
	room   string
	digest string
}

type restConn struct {
	client    pmsrest.Client
	iface     Interface
	rev       Revision
	kind      string
	identKey  []byte
	identKeyN int
	evKey     []byte
	evKeyN    int
	profile   string
	now       func() time.Time
	log       *slog.Logger
	sleep     func(context.Context, time.Duration) error

	known       map[string]knownStay
	lastFull    time.Time
	lastSuccess time.Time
	since       time.Time // start of the change window for the next poll
}

func (c *restConn) Close() error { return nil }

// providerCode maps a pmsrest failure to the bounded runtime vocabulary.
func providerCode(err error) Code {
	switch pmsrest.KindOf(err) {
	case pmsrest.KindAuth:
		return CodeProviderAuth
	case pmsrest.KindRateLimited:
		return CodeProviderRateLimited
	case pmsrest.KindTimeout:
		return CodeProviderTimeout
	case pmsrest.KindUnavailable:
		return CodeProviderUnavailable
	case pmsrest.KindInvalidResponse:
		return CodeProviderResponse
	case pmsrest.KindRequest:
		return CodeProviderRejected
	case pmsrest.KindConfig:
		return CodeConfigInvalid
	}
	return Classify(err)
}

func (c *restConn) fail(sink AxisSink, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code := providerCode(err)
	if c.log != nil {
		c.log.Error("pmsd: REST connector cycle ended", "interface", c.iface.ID, "connector", c.kind, "code", code.String())
	}
	_ = sink.OnDisconnected(c.now(), code)
	return coded(code, err)
}

func (c *restConn) Serve(ctx context.Context, sink AxisSink) error {
	// Prove the credential and the endpoint BEFORE saying CONNECTED: a REST link has no socket to open, so
	// the first authenticated read is the only honest meaning of "connected".
	if _, err := c.client.Probe(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := providerCode(err)
		if c.log != nil {
			c.log.Error("pmsd: REST connector could not connect", "interface", c.iface.ID, "connector", c.kind, "code", code.String())
		}
		return coded(code, err)
	}
	if err := sink.OnConnected(c.now()); err != nil {
		return err
	}
	if err := sink.RequireInitialResync(c.now()); err != nil {
		return err
	}
	sink.OnFullSyncRequested()
	if err := c.fullSync(ctx, sink); err != nil {
		return c.fail(sink, err)
	}

	poll := c.rev.HeartbeatInterval
	for {
		if err := c.sleep(ctx, poll); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if cmd := sink.ClaimOperatorResync(); cmd != nil {
			if c.log != nil {
				c.log.Info("pmsd: operator requested full resync", "interface", c.iface.ID, "command", cmd.ID, "reason", cmd.Reason)
			}
			sink.OnFullSyncRequested()
			if err := c.fullSync(ctx, sink); err != nil {
				return c.fail(sink, err)
			}
			continue
		}
		if c.rev.CompleteSyncBound > 0 && c.now().Sub(c.lastFull) >= c.rev.CompleteSyncBound {
			sink.OnFullSyncRequested()
			if err := c.fullSync(ctx, sink); err != nil {
				return c.fail(sink, err)
			}
			continue
		}
		if err := c.pollChanges(ctx, sink); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if pmsrest.KindOf(err) == "" {
				return err // a sink/persistence failure: ownership or the database is gone, end the cycle now
			}
			if pmsrest.KindOf(err) == pmsrest.KindAuth || c.now().Sub(c.lastSuccess) > c.rev.HeartbeatTimeout {
				return c.fail(sink, err)
			}
			if c.log != nil {
				c.log.Warn("pmsd: REST poll failed; retrying on the next interval",
					"interface", c.iface.ID, "connector", c.kind, "code", providerCode(err).String())
			}
			continue
		}
	}
}

// fullSync reads the complete in-house roster into a new resync generation and publishes it.
func (c *restConn) fullSync(ctx context.Context, sink AxisSink) error {
	started := c.now()
	if err := sink.OnResyncStart(started); err != nil {
		return err
	}
	snap, err := c.client.Snapshot(ctx)
	if err != nil {
		return err
	}
	known := map[string]knownStay{}
	occupied := map[string]struct{}{}
	skipped := int64(0)
	admitted := 0
	for _, r := range snap.Reservations {
		if r.State != pmsrest.StateInHouse {
			continue
		}
		if _, dup := known[r.ID]; dup {
			continue
		}
		ev, ok := c.event(RecGI, r, r.Room)
		if !ok {
			skipped++
			continue
		}
		if err := sink.OnDomainEvent(ctx, ev); err != nil {
			return err
		}
		known[r.ID] = knownStay{room: r.Room, digest: digestOf(r)}
		occupied[roomKey(r.Room)] = struct{}{}
		admitted++
	}
	// Coverage is recorded only when the provider enumerated the property's rooms. Without that enumeration
	// the sweep cannot say which rooms it saw empty, and a coverage row built from occupied rooms alone would
	// teach reconciliation a building that shrinks and grows with occupancy -- so none is written, and
	// reconciliation refuses (REFUSED_NO_COVERAGE_EVIDENCE) rather than judging an incomplete picture.
	if snap.RoomsKnown {
		all := map[string]struct{}{}
		for _, rm := range snap.Rooms {
			if k := roomKey(rm); k != "" {
				all[k] = struct{}{}
			}
		}
		for k := range occupied {
			all[k] = struct{}{}
		}
		union := make([]string, 0, len(all))
		vacant := 0
		for k := range all {
			union = append(union, k)
			if _, occ := occupied[k]; !occ {
				vacant++
			}
		}
		sort.Strings(union)
		sink.RecordCoverage(union, admitted, vacant, 0)
	}
	sink.RecordSkipped(skipped)
	if err := sink.OnResyncComplete(c.now(), ""); err != nil {
		return err
	}
	c.known = known
	c.lastFull = started
	c.lastSuccess = c.now()
	// The change window restarts where this roster was read, less the overlap, so nothing committed while
	// the roster was being paged is lost.
	c.since = started.Add(-changeOverlap)
	return sink.OnHeartbeat(c.now())
}

// pollChanges admits what changed since the previous poll.
func (c *restConn) pollChanges(ctx context.Context, sink AxisSink) error {
	windowStart := c.now()
	var changed []pmsrest.Reservation
	var departed []string
	if c.client.SupportsChanges() {
		rs, err := c.client.Changes(ctx, c.since, windowStart.Add(time.Minute))
		if err != nil {
			return err
		}
		changed = rs
	} else {
		// No change feed: re-read the in-house list and compare. A reservation that has left the list is
		// looked up individually, because leaving the in-house list is not by itself a check-out (it may be a
		// reversed check-in) and only a check-out may become a GO.
		snap, err := c.client.Snapshot(ctx)
		if err != nil {
			return err
		}
		changed = snap.Reservations
		present := map[string]bool{}
		for _, r := range snap.Reservations {
			if r.State == pmsrest.StateInHouse {
				present[r.ID] = true
			}
		}
		for id := range c.known {
			if !present[id] {
				departed = append(departed, id)
			}
		}
		sort.Strings(departed)
		if len(departed) > 0 {
			rs, err := c.client.Lookup(ctx, departed)
			if err != nil {
				return err
			}
			changed = append(changed, rs...)
		}
	}
	// Last observation per reservation wins, so a guest seen twice in one window is judged once.
	latest := map[string]pmsrest.Reservation{}
	order := []string{}
	for _, r := range changed {
		if r.ID == "" {
			continue
		}
		if _, seen := latest[r.ID]; !seen {
			order = append(order, r.ID)
		}
		latest[r.ID] = r
	}
	for _, id := range order {
		r := latest[id]
		prev, wasKnown := c.known[id]
		switch r.State {
		case pmsrest.StateInHouse:
			d := digestOf(r)
			if wasKnown && prev.digest == d {
				continue // re-read of an unchanged stay: nothing to say
			}
			rt := RecGI
			if wasKnown {
				rt = RecGC // a change to a stay already in the house -- a room move included
			}
			ev, ok := c.event(rt, r, r.Room)
			if !ok {
				continue
			}
			if err := sink.OnDomainEvent(ctx, ev); err != nil {
				return err
			}
			c.known[id] = knownStay{room: r.Room, digest: d}
		case pmsrest.StateCheckedOut:
			if !wasKnown {
				continue // never admitted as in-house by this connection; nothing to close
			}
			room := r.Room
			if strings.TrimSpace(room) == "" {
				room = prev.room
			}
			ev, ok := c.event(RecGO, r, room)
			if !ok {
				continue
			}
			if err := sink.OnDomainEvent(ctx, ev); err != nil {
				return err
			}
			delete(c.known, id)
		default:
			// Left the house without a check-out (a reversed check-in, a cancellation). No GO is invented;
			// the next complete roster's reconciliation decides, as it does for FIAS.
			if wasKnown {
				delete(c.known, id)
				if c.log != nil {
					c.log.Info("pmsd: reservation left the in-house list without a check-out; left to reconciliation",
						"interface", c.iface.ID, "connector", c.kind)
				}
			}
		}
	}
	if c.client.SupportsChanges() {
		c.since = windowStart.Add(-changeOverlap)
	}
	c.lastSuccess = c.now()
	return sink.OnHeartbeat(c.now())
}

// digestOf is the content an admitted event carries; an unchanged digest means an unchanged stay.
func digestOf(r pmsrest.Reservation) string {
	var b strings.Builder
	for _, s := range []string{r.Room, r.LastName, r.FirstName, r.Arrival, r.Departure} {
		b.WriteString(s)
		b.WriteByte(0x1f)
	}
	for _, g := range r.Sharers {
		b.WriteString(g.ExternalID + "\x1e" + g.FirstName + "\x1e" + g.LastName)
		if g.Primary {
			b.WriteString("\x1eP")
		}
		b.WriteByte(0x1f)
	}
	return b.String()
}

// event builds a validated domain Event for one reservation, or reports false when the reservation cannot be
// admitted (no room, an overlong value). Such a record is SKIPPED, never faulted: faulting would request a
// resync that re-reads the same record forever. It is counted and logged without its content.
func (c *restConn) event(rt RecordType, r pmsrest.Reservation, room string) (Event, bool) {
	room = strings.TrimSpace(room)
	if strings.TrimSpace(r.ID) == "" || room == "" {
		if c.log != nil {
			c.log.Warn("pmsd: skipping reservation with no Stay identity",
				"interface", c.iface.ID, "connector", c.kind, "record_type", string(rt))
		}
		return Event{}, false
	}
	pairs := []FieldPair{
		{Code: "ID", Value: r.ID}, {Code: "ST", Value: r.State.String()}, {Code: "RN", Value: room},
		{Code: "GN", Value: r.LastName}, {Code: "GF", Value: r.FirstName},
		{Code: "GA", Value: r.Arrival}, {Code: "GD", Value: r.Departure},
	}
	var sharers []EventSharer
	for _, g := range r.Sharers {
		sharers = append(sharers, EventSharer{ExternalGuestID: g.ExternalID, FirstName: g.FirstName, LastName: g.LastName, IsPrimary: g.Primary})
		p := "0"
		if g.Primary {
			p = "1"
		}
		pairs = append(pairs, FieldPair{Code: "SH", Value: g.ExternalID + "\x1e" + g.FirstName + "\x1e" + g.LastName + "\x1e" + p})
	}
	// The provider's change stamp is the source sequence: the same reservation re-read at the same version
	// reproduces the fingerprint, and any later version of it does not.
	se := newSourceEvent(c.iface.ID, rt, c.rev.NormalizationVersion, c.profile, r.Stamp, pairs)
	fp, fpVer := ComputeSourceFingerprint(c.identKey, c.identKeyN, se)
	evHash, evVer := ComputeEvidenceHMAC(c.evKey, c.evKeyN, se.canonical())
	now := c.now()
	ev := Event{
		InterfaceID: c.iface.ID, RevisionID: c.rev.ID, SecretGenerationID: c.rev.ActiveSecretGenerationID,
		NormalizationVer: c.rev.NormalizationVersion, RecordType: rt,
		SourceEventFingerprint: fp, FingerprintKeyVersion: fpVer, ExternalEventIdentity: fp,
		StayResolutionCandidate: DeriveStayResolutionCandidate(c.iface.TenantID, c.iface.SiteID, c.iface.ID, r.ID),
		ReservationRef:          r.ID,
		RoomNumber:              room,
		GuestLastName:           r.LastName,
		GuestFirstName:          r.FirstName,
		ArrivalRaw:              r.Arrival,
		DepartureRaw:            r.Departure,
		Sharers:                 sharers,
		// Like FIAS, no provider timestamp is presented as a verified event time: a modification stamp is not
		// the moment a guest arrived or left, and the checkout boundary must not be derived from it.
		NormalizedAt: now, ReceivedAt: now,
		Cursor:             r.ID + ":" + room,
		SourceEvidenceHash: evHash, EvidenceKeyVersion: evVer,
	}
	if err := ev.Validate(); err != nil {
		if c.log != nil {
			c.log.Warn("pmsd: skipping reservation that fails the event contract",
				"interface", c.iface.ID, "connector", c.kind, "record_type", string(rt), "reason", classifyDomainReject(err))
		}
		return Event{}, false
	}
	return ev, true
}

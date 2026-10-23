package pmsd

// The polled REST connector drives the same sink contract as the FIAS adapter; provider specifics live in
// internal/pmsrest. It never writes to a PMS or a Stay directly, only durable inbox rows through the sink.

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
	Keys      AdapterKeys
	Now       func() time.Time
	Log       *slog.Logger
	NewClient func(kind string, provider map[string]any, secret []byte, o pmsrest.Options) (pmsrest.Client, error)
	HTTP      *http.Client
	Sleep     func(ctx context.Context, d time.Duration) error
}

// changeOverlap re-reads part of the previous window so changes committed just before the last read, or clock skew, are not missed.
const changeOverlap = 2 * time.Minute

// NewRESTDial builds the Deps.Dial for REST connector kinds; it does no network I/O and fails closed without the identity/evidence keys.
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
		// pmsrest.New copies the secret, so the worker zeroing its buffer after Dial cannot corrupt the credential.
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

// knownStay is the last admitted room (a GO must name it) and a field digest so an unchanged re-read emits nothing.
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
	// A REST link has no socket, so the first authenticated read is the only honest meaning of "connected".
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
	// Coverage is recorded only when the provider enumerated rooms; occupied rooms alone would describe a building
	// that shrinks with occupancy, so reconciliation refuses (REFUSED_NO_COVERAGE_EVIDENCE) instead.
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
	// Restart the change window at the roster read, less the overlap, so changes made while paging are not lost.
	c.since = started.Add(-changeOverlap)
	return sink.OnHeartbeat(c.now())
}

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
		// No change feed: compare the in-house list. A reservation that left it is looked up, because only a
		// check-out may become a GO (it may be a reversed check-in).
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

// event reports false for a reservation that cannot be admitted (no room, overlong value). It is skipped, never
// faulted: a fault would request a resync that re-reads the same record forever.
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
	// The provider's change stamp is the source sequence, so a re-read at the same version reproduces the fingerprint.
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
		// No provider timestamp is presented as event time: a modification stamp is not when a guest arrived or left.
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

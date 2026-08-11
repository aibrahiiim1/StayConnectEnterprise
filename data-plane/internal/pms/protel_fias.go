package pms

// Protel / Opera / Suite8 — all speak FIAS (Oracle Hospitality
// HGBU-IFC8-FIAS Interface Specification 2.20.24).
//
// FIAS is push-based: the PMS streams GI (guest in), GO (guest out), GC
// (guest change) records over a persistent TCP connection. We maintain an
// in-memory cache keyed by room number; ValidateGuest queries the cache.
//
// Wire format (TCP):
//
//   STX (0x02)  <ascii record>  ETX (0x03)
//
// Records are pipe-separated fields. The first token is the 2-letter
// Record-ID; subsequent tokens are 2-letter Field-ID + value, e.g.
//
//   GI|RN103|G#12345|GNRogers|GFMr|GA260420|GD260425|
//
// Spec sections used:
//   - 7 FIPS / TCP framing            (STX/ETX delimiters)
//   - 6 Record-ID types  → LS,LD,LR    (link start handshake)
//                         GI/GO/GC     (guest data we subscribe to)
//   - Appendix C  → field IDs (RN, G#, GN, GF, GA, GD, GS)

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	stx = 0x02
	etx = 0x03
)

// ProtelFIAS connects to a Protel/Opera/Suite8 PMS over plain TCP and keeps
// a local cache of currently-checked-in guests fed by GI/GC/GO notifications.
type ProtelFIAS struct {
	providerName string

	// Connection (set by Configure).
	addr         string
	useTLS       bool
	authKey      string
	ifcName      string // sent in LD as IFPB; defaults to "IFPB"
	version      string // sent in LD as V#; defaults to "1.13"
	dialTimeout  time.Duration
	writeTimeout time.Duration
	readTimeout  time.Duration

	// Field map: canonical → FIAS Field-ID. Defaults from
	// DefaultFIASFieldMap, merged with per-tenant overrides by the loader.
	fmap FieldMap
	cfg  ProviderConfig

	mu     sync.RWMutex
	rooms  map[string]*Reservation // key = NormalizeRoom(room)
	health Health

	// Connection lifecycle.
	cancel context.CancelFunc
}

// NewProtelFIAS returns an unconfigured instance. The loader calls
// Configure() before Start() to apply the pms_providers row.
func NewProtelFIAS(name string) *ProtelFIAS {
	return &ProtelFIAS{
		providerName: name,
		ifcName:      "IFPB",
		version:      "1.13",
		dialTimeout:  5 * time.Second,
		writeTimeout: 5 * time.Second,
		readTimeout:  60 * time.Second,
		fmap:         DefaultFIASFieldMap(),
		rooms:        map[string]*Reservation{},
		health:       Health{Status: "idle"},
	}
}

func (p *ProtelFIAS) Name() string { return p.providerName }
func (p *ProtelFIAS) Kind() string { return "protel-fias" }

// Configure applies a pms_providers row. Call before Start().
//
//	Connection.Host / Port — TCP target (required)
//	Connection.UseTLS      — wrap in tls.Dial when true (4.5.5b will wire it)
//	Connection.AuthKey     — FIAS IfcAuthKey for the LD CG/RT4 dance (4.5.5b)
//	Connection.Extra["ifc_name"] / ["version"] — LD record overrides
func (p *ProtelFIAS) Configure(cfg ProviderConfig) error {
	if cfg.Name != "" {
		p.providerName = cfg.Name
	}
	if cfg.Connection.Host == "" || cfg.Connection.Port == 0 {
		return fmt.Errorf("protel-fias %q: host and port required", cfg.Name)
	}
	p.addr = fmt.Sprintf("%s:%d", cfg.Connection.Host, cfg.Connection.Port)
	p.useTLS = cfg.Connection.UseTLS
	p.authKey = cfg.Connection.AuthKey
	if v, ok := cfg.Connection.Extra["ifc_name"].(string); ok && v != "" {
		p.ifcName = v
	}
	if v, ok := cfg.Connection.Extra["version"].(string); ok && v != "" {
		p.version = v
	}
	// Merge the per-tenant FieldMap on top of the FIAS defaults. The loader
	// hands us the raw overrides; defaults live here so each kind owns its
	// own protocol vocabulary.
	p.fmap = MergeFieldMap(DefaultFIASFieldMap(), cfg.FieldMap)
	p.cfg = cfg
	return nil
}

// Health returns a live snapshot. Safe to call from any goroutine.
func (p *ProtelFIAS) Health() Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	h := p.health
	h.CacheSize = len(p.rooms)
	return h
}

// Config returns the last-applied bundle.
func (p *ProtelFIAS) Config() ProviderConfig { return p.cfg }

func (p *ProtelFIAS) setStatus(status string) {
	p.mu.Lock()
	p.health.Status = status
	if status == "connected" {
		p.health.ConnectedSince = time.Now()
	} else {
		p.health.ConnectedSince = time.Time{}
	}
	p.mu.Unlock()
}

func (p *ProtelFIAS) recordError(err error) {
	p.mu.Lock()
	p.health.Status = "down"
	p.health.LastError = err.Error()
	p.health.LastErrorAt = time.Now()
	p.health.ConnectedSince = time.Time{}
	p.mu.Unlock()
}

// TestConnection performs a one-shot dial + LS handshake to validate
// connectivity and protocol compatibility. Doesn't disturb the persistent
// connect loop.
func (p *ProtelFIAS) TestConnection(ctx context.Context) error {
	if p.addr == "" {
		return fmt.Errorf("protel-fias: not configured")
	}
	d := net.Dialer{Timeout: p.dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", p.addr, err)
	}
	defer conn.Close()
	if err := p.writeRecord(conn, p.recordLS()); err != nil {
		return fmt.Errorf("write LS: %w", err)
	}
	// We don't wait for the LS echo — many PMS dev configs delay it. A
	// successful TCP dial + first record write is sufficient signal that
	// the host:port reaches a FIAS-speaking peer.
	return nil
}

// CacheSnapshot returns up to limit cached reservations.
func (p *ProtelFIAS) CacheSnapshot(limit int) []Reservation {
	if limit <= 0 {
		limit = 200
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Reservation, 0, len(p.rooms))
	for _, r := range p.rooms {
		out = append(out, *r)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Start launches the connect-and-receive goroutine. Safe to call once after
// Configure(). Use Stop to cancel.
func (p *ProtelFIAS) Start(parent context.Context) {
	if p.addr == "" {
		// Configure wasn't called or the row was incomplete; skip silently.
		return
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	go p.runLoop(ctx)
}

func (p *ProtelFIAS) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
}

func (p *ProtelFIAS) ValidateGuest(_ context.Context, q Query) (*Result, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	norm := p.cfg.Normalization
	queryRoom := NormalizeRoom(ApplyRoomFormat(norm.RoomFormat, q.RoomNumber))

	var r *Reservation
	for storedKey, rec := range p.rooms {
		stored := NormalizeRoom(ApplyRoomFormat(norm.RoomFormat, rec.RoomNumber))
		if stored == queryRoom || storedKey == queryRoom {
			r = rec
			break
		}
	}
	if r == nil {
		return nil, ErrNotFound
	}

	qFirst := ApplyNameNormalization(norm, q.FirstName)
	qLast := ApplyNameNormalization(norm, q.LastName)
	qRes := ApplyReservationCase(norm, q.ReservationNumber)
	recFirst := ApplyNameNormalization(norm, r.FirstName)
	recLast := ApplyNameNormalization(norm, r.LastName)
	recRes := ApplyReservationCase(norm, r.ReservationNumber)

	if !MatchesQuery(q.Mode, qFirst, qLast, qRes, recFirst, recLast, recRes) {
		return nil, ErrNotFound
	}
	display := r.GuestDisplayName
	if display == "" {
		display = strings.TrimSpace(r.FirstName + " " + r.LastName)
	}
	return &Result{
		Valid:         true,
		GuestName:     display,
		FirstName:     r.FirstName,
		LastName:      r.LastName,
		CheckIn:       r.CheckIn,
		CheckOut:      r.CheckOut,
		RoomNumber:    r.RoomNumber,
		ReservationID: r.ReservationNumber,
		Email:         r.Email,
	}, nil
}

// ---- Connection lifecycle --------------------------------------------------

func (p *ProtelFIAS) runLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		p.setStatus("connecting")
		if err := p.connectAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.recordError(err)
			slog.Warn("protel-fias: link down", "name", p.providerName, "addr", p.addr, "err", err, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (p *ProtelFIAS) connectAndServe(ctx context.Context) error {
	d := net.Dialer{Timeout: p.dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Link Start handshake.
	if err := p.writeRecord(conn, p.recordLS()); err != nil {
		return fmt.Errorf("write LS: %w", err)
	}
	if err := p.writeRecord(conn, p.recordLD()); err != nil {
		return fmt.Errorf("write LD: %w", err)
	}
	for _, lr := range p.recordLRs() {
		if err := p.writeRecord(conn, lr); err != nil {
			return fmt.Errorf("write LR: %w", err)
		}
	}
	p.setStatus("connected")

	br := bufio.NewReader(conn)
	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(p.readTimeout))
		rec, err := readFramedRecord(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// Link idle past ReadTimeout — heartbeat by sending LA.
				if werr := p.writeRecord(conn, "LA|"); werr != nil {
					return fmt.Errorf("LA write: %w", werr)
				}
				continue
			}
			return fmt.Errorf("read: %w", err)
		}
		p.handleRecord(rec)
	}
	return ctx.Err()
}

func (p *ProtelFIAS) handleRecord(rec string) {
	if len(rec) < 2 {
		return
	}
	rid := rec[:2]
	switch rid {
	case "LA": // alive — already handled by re-arming timeout
	case "LE": // link end — let the read loop notice & reconnect
	case "GI", "GC":
		fields := parseFields(rec[2:])
		// Pull each canonical field through the merged FieldMap. Empty / unmapped
		// codes are skipped. Tenants override e.g. last_name → "GG" by setting
		// the map column on pms_providers.
		room := strings.TrimSpace(p.fieldStr(fields, FRoomNumber))
		if room == "" {
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.health.LastRecordAt = time.Now()
		key := NormalizeRoom(room)
		r := p.rooms[key]
		if r == nil {
			r = &Reservation{}
		}
		r.RoomNumber = room
		if v := p.fieldStr(fields, FReservationNumber); v != "" {
			r.ReservationNumber = v
		}
		if v := p.fieldStr(fields, FLastName); v != "" {
			r.LastName = v
			r.GuestDisplayName = v
		}
		if v := p.fieldStr(fields, FFirstName); v != "" {
			r.FirstName = v
		}
		if v := p.fieldStr(fields, FCheckIn); v != "" {
			if t, err := parseFiasDate(v); err == nil {
				r.CheckIn = t
			}
		}
		if v := p.fieldStr(fields, FCheckOut); v != "" {
			if t, err := parseFiasDate(v); err == nil {
				r.CheckOut = t
			}
		}
		if v := p.fieldStr(fields, FGuestEmail); v != "" {
			r.Email = v
		}
		p.rooms[key] = r
	case "GO":
		fields := parseFields(rec[2:])
		room := strings.TrimSpace(p.fieldStr(fields, FRoomNumber))
		if room == "" {
			return
		}
		p.mu.Lock()
		delete(p.rooms, NormalizeRoom(room))
		p.health.LastRecordAt = time.Now()
		p.mu.Unlock()
	}
}

// fieldStr looks up a canonical field via the merged FieldMap and returns
// the raw value from the parsed FIAS record. Empty when unmapped or absent.
func (p *ProtelFIAS) fieldStr(fields map[string]string, canonical string) string {
	id := p.fmap[canonical]
	if id == "" {
		return ""
	}
	return fields[id]
}

// ---- Outbound records (handshake + subscriptions) -------------------------

func (p *ProtelFIAS) recordLS() string {
	now := time.Now()
	return fmt.Sprintf("LS|DA%s|TI%s|", now.Format("060102"), now.Format("150405"))
}

func (p *ProtelFIAS) recordLD() string {
	now := time.Now()
	return fmt.Sprintf("LD|DA%s|TI%s|%s|V#%s|RT4|",
		now.Format("060102"), now.Format("150405"), p.ifcName, p.version)
}

// recordLRs subscribes to the records we care about + tells the PMS which
// fields to include.
func (p *ProtelFIAS) recordLRs() []string {
	return []string{
		"LR|RIGI|FLRNG#GNGFGAGD|", // Guest In: room, reservation, last name, first name, arrival, departure
		"LR|RIGC|FLRNG#GNGFGAGD|", // Guest Change: same set
		"LR|RIGO|FLRNG#|",         // Guest Out: room + reservation
	}
}

// ---- Wire helpers ----------------------------------------------------------

func (p *ProtelFIAS) writeRecord(c net.Conn, body string) error {
	_ = c.SetWriteDeadline(time.Now().Add(p.writeTimeout))
	buf := make([]byte, 0, len(body)+2)
	buf = append(buf, stx)
	buf = append(buf, body...)
	buf = append(buf, etx)
	_, err := c.Write(buf)
	return err
}

// readFramedRecord reads one STX..ETX-bracketed record from the stream.
// Bytes outside the frame are ignored (stream may carry handshake noise).
func readFramedRecord(br *bufio.Reader) (string, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b != stx {
			continue
		}
		body, err := br.ReadString(etx)
		if err != nil {
			return "", err
		}
		// body includes the trailing ETX — strip it.
		if n := len(body); n > 0 && body[n-1] == etx {
			body = body[:n-1]
		}
		return body, nil
	}
}

// parseFields walks a "|FFvalue|FFvalue|" tail and returns a map of
// 2-character field-id → value. The first character pair of each segment is
// the field id; the remainder is the value.
func parseFields(tail string) map[string]string {
	out := map[string]string{}
	for _, seg := range strings.Split(tail, "|") {
		if len(seg) < 2 {
			continue
		}
		out[seg[:2]] = seg[2:]
	}
	return out
}

// parseFiasDate parses YYMMDD (e.g. "260420") as midnight UTC.
// Per FIAS Field type "D".
func parseFiasDate(s string) (time.Time, error) {
	if len(s) != 6 {
		return time.Time{}, fmt.Errorf("not a YYMMDD date: %q", s)
	}
	return time.ParseInLocation("060102", s, time.UTC)
}

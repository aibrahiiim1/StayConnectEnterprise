package main

// Records who opened one guest's record (guest_data.viewed) or searched a guest list (guest_data.searched).
// Plain list browsing is not recorded. Entries go to a queue so the request never waits.
// Typed-letter searches collapse into one entry and repeated reads are recorded once per repeatWindow, decided
// server-side. A detail read's target is the record id and a search's text is in the payload, so erasure and
// retention find and anonymise both.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
)

// guestRead: a route with idParams is a detail read; one with searchHeaders or searchQuery is a list recorded only when searched.
type guestRead struct {
	targetType    string
	idParams      []string // URL parameters naming the record; the first is the target id
	searchHeaders []string // headers carrying the free-text search (see readSearch)
	searchQuery   []string // query parameters that narrow the list to someone
}

// guestDataReads lists every route that shows guest data, by the pattern chi.Context.RoutePattern reports (a list mounted
// at "/" has no trailing slash). guest-signin-credentials is audited by its own handler.
var guestDataReads = map[string]guestRead{
	"/edge/v1/guest-accounts/{id}":                 {targetType: "client_account", idParams: []string{"id"}},
	"/edge/v1/guest-accounts/{id}/manage":          {targetType: "client_account", idParams: []string{"id"}},
	"/edge/v1/client-devices/{id}":                 {targetType: "device", idParams: []string{"id"}},
	"/edge/v1/sessions/{id}":                       {targetType: "session", idParams: []string{"id"}},
	"/edge/v1/pms-stays/{id}":                      {targetType: "stay", idParams: []string{"id"}},
	"/edge/v1/guest-signin-attempts/{id}":          {targetType: "sign_in_attempt", idParams: []string{"id"}},
	"/edge/v1/post-stay-profiles/{id}":             {targetType: "post_stay_profile", idParams: []string{"id"}},
	"/edge/v1/usage/stays/{stay_id}":               {targetType: "stay", idParams: []string{"stay_id"}},
	"/edge/v1/usage/devices/{mac}":                 {targetType: "device_mac", idParams: []string{"mac"}},
	"/edge/v1/usage/sessions/{session_id}/samples": {targetType: "session", idParams: []string{"session_id"}},
	"/edge/v1/usage/sources/{type}/{id}":           {targetType: "usage_source", idParams: []string{"id", "type"}},
	"/edge/v1/guest-accounts":                      {targetType: "client_accounts", searchHeaders: []string{accountSearchHeader}},
	"/edge/v1/client-devices":                      {targetType: "devices", searchHeaders: []string{deviceSearchHeader}},
	"/edge/v1/sessions":                            {targetType: "sessions", searchHeaders: []string{sessionSearchHeader}},
	"/edge/v1/pms-stays":                           {targetType: "stays", searchHeaders: []string{staySearchHeader}},
	"/edge/v1/pms-events":                          {targetType: "stay_events", searchHeaders: []string{pmsEventSearchHeader}},
	"/edge/v1/guest-signin-attempts":               {targetType: "sign_in_attempts", searchHeaders: []string{signInSearchHeader}, searchQuery: []string{"room"}},
	"/edge/v1/usage/stays":                         {targetType: "usage_stays", searchHeaders: []string{usageSearchHeader}, searchQuery: []string{"q"}},
	"/edge/v1/usage/sources":                       {targetType: "usage_sources", searchHeaders: []string{usageSearchHeader}, searchQuery: []string{"q"}},
	"/edge/v1/guest-data-rights/search":            {targetType: "guest_data", searchHeaders: []string{guestDataSearchHeader}, searchQuery: []string{"q"}},
	"/edge/v1/guest-data-rights/assisted/{id}":     {targetType: "assisted_guest_request", idParams: []string{"id"}},
	"/edge/v1/guest-data-rights/register":          {targetType: "guest_data_requests"},
	"/edge/v1/network/dhcp/leases":                 {targetType: "dhcp_leases", searchHeaders: []string{dhcpSearchHeader}},
}

const (
	readQueueSize = 4096
	// readSettle is how long a search waits for the next keystroke before it is written.
	readSettle = 3 * time.Second
	// repeatWindow is how long the same read by the same person is recorded only once.
	repeatWindow = 10 * time.Minute
)

// readEntry is one read, with everything taken from the request at the time it happened.
type readEntry struct {
	at         time.Time
	actorID    string
	action     string // guest_data.viewed | guest_data.searched
	targetType string
	targetID   string
	ip         string
	userAgent  string
	payload    map[string]any
	search     string // the combined search text, for collapsing a typed search
}

// readRecorder is the queue and its single writer.
type readRecorder struct {
	in      chan readEntry
	write   func(context.Context, readEntry) error
	now     func() time.Time
	dropped atomic.Int64
	done    chan struct{}
}

func newReadRecorder(write func(context.Context, readEntry) error) *readRecorder {
	return &readRecorder{in: make(chan readEntry, readQueueSize), write: write, now: time.Now, done: make(chan struct{})}
}

// record hands the entry to the writer. When the queue is full it writes the entry itself rather than drop it
// (every search must be recorded): the response has already gone, so only this rare request waits for the write.
func (rr *readRecorder) record(e readEntry) {
	select {
	case rr.in <- e:
	default:
		wctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := rr.write(wctx, e); err != nil {
			if n := rr.dropped.Add(1); n == 1 || n%100 == 0 {
				slog.Warn("guest-data read not recorded: the queue is full and the direct write failed", "dropped_total", n, "err", err)
			}
		}
	}
}

// run drains the queue until ctx ends, then writes what is still waiting and closes done.
func (rr *readRecorder) run(ctx context.Context, tick time.Duration) {
	defer close(rr.done)
	pending := map[string]readEntry{} // actor + list -> the search waiting for its last keystroke
	seen := map[string]time.Time{}    // what was written, and when
	t := time.NewTicker(tick)
	defer t.Stop()

	emit := func(e readEntry) {
		k := e.actorID + "\x00" + e.action + "\x00" + e.targetType + "\x00" + e.targetID + "\x00" + e.search
		if last, ok := seen[k]; ok && e.at.Sub(last) < repeatWindow {
			return
		}
		seen[k] = e.at
		wctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := rr.write(wctx, e); err != nil {
			slog.Warn("guest-data read not recorded", "action", e.action, "err", err)
		}
	}
	accept := func(e readEntry) {
		if e.action != "guest_data.searched" {
			emit(e)
			return
		}
		pk := e.actorID + "\x00" + e.targetType
		if prev, ok := pending[pk]; ok && !sameTyping(prev.search, e.search) {
			emit(prev)
		}
		pending[pk] = e
	}
	flush := func(all bool) {
		now := rr.now()
		for k, e := range pending {
			if all || now.Sub(e.at) >= readSettle {
				emit(e)
				delete(pending, k)
			}
		}
		for k, at := range seen {
			if now.Sub(at) >= repeatWindow {
				delete(seen, k)
			}
		}
	}

	for {
		select {
		case e := <-rr.in:
			accept(e)
		case <-t.C:
			flush(false)
		case <-ctx.Done():
			for {
				select {
				case e := <-rr.in:
					accept(e)
				default:
					flush(true)
					return
				}
			}
		}
	}
}

// sameTyping: one search is the other with letters added or removed at the end, so they are one search being
// typed rather than two.
func sameTyping(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// writeReadEntry appends one entry to the site's audit log.
func (s *server) writeReadEntry(ctx context.Context, e readEntry) error {
	raw, _ := json.Marshal(e.payload)
	_, err := s.db.Exec(ctx, `
        INSERT INTO audit_log (ts, tenant_id, actor_type, actor_id, action, target_type, target_id, ip, user_agent, payload)
        VALUES ($1, NULLIF($2,'')::uuid, 'operator', NULLIF($3,''), $4, NULLIF($5,''), NULLIF($6,''),
                CASE WHEN $7 = '' THEN NULL ELSE $7::inet END, NULLIF($8,''), $9::jsonb)
    `, e.at, s.tenantID, e.actorID, e.action, e.targetType, e.targetID, e.ip, e.userAgent, string(raw))
	return err
}

// recordGuestDataReads is the middleware. It runs after requireAuth, lets the handler answer, and only then
// looks at which route answered: a read is recorded only when it succeeded.
func (s *server) recordGuestDataReads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.reads == nil || r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		at := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if sw.status != http.StatusOK {
			return
		}
		rctx := chi.RouteContext(r.Context())
		if rctx == nil {
			return
		}
		spec, ok := guestDataReads[rctx.RoutePattern()]
		if !ok {
			return
		}
		e, ok := spec.entry(r, rctx)
		if !ok {
			return
		}
		e.at, e.ip, e.userAgent = at, clientIP(r), r.UserAgent()
		if sess := sessFrom(r.Context()); sess != nil {
			e.actorID = sess.OperatorID
		}
		s.reads.record(e)
	})
}

func (g guestRead) entry(r *http.Request, rctx *chi.Context) (readEntry, bool) {
	e := readEntry{targetType: g.targetType, payload: map[string]any{}}
	if len(g.idParams) > 0 {
		e.action = "guest_data.viewed"
		for i, p := range g.idParams {
			v := rctx.URLParam(p)
			if i == 0 {
				e.targetID = v
			} else if v != "" {
				e.payload[p] = v
			}
		}
		return e, e.targetID != ""
	}
	e.action = "guest_data.searched"
	var parts []string
	for _, h := range g.searchHeaders {
		if v := headerSearchText(r, h); v != "" {
			e.payload["search"] = v
			parts = append(parts, v)
		}
	}
	for _, p := range g.searchQuery {
		if v := strings.TrimSpace(r.URL.Query().Get(p)); v != "" {
			e.payload[p] = v
			parts = append(parts, v)
		}
	}
	e.search = strings.Join(parts, " ")
	return e, len(parts) > 0
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming responses working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

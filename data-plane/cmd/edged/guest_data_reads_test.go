package main

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type readSink struct {
	mu  sync.Mutex
	got []readEntry
}

func (k *readSink) write(_ context.Context, e readEntry) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.got = append(k.got, e)
	return nil
}

func (k *readSink) entries() []readEntry {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]readEntry(nil), k.got...)
}

func TestReadRecorderCollapsesTypingAndRepeats(t *testing.T) {
	sink := &readSink{}
	rr := newReadRecorder(sink.write)
	ctx, stop := context.WithCancel(context.Background())
	go rr.run(ctx, 10*time.Millisecond)

	t0 := time.Now()
	search := func(term string, at time.Time) readEntry {
		return readEntry{at: at, actorID: "op-1", action: "guest_data.searched", targetType: "sessions",
			search: term, payload: map[string]any{"search": term}}
	}
	for i, term := range []string{"j", "jo", "joh", "john"} {
		rr.record(search(term, t0.Add(time.Duration(i)*100*time.Millisecond)))
	}
	rr.record(search("mary", t0.Add(time.Second)))
	view := readEntry{at: t0, actorID: "op-1", action: "guest_data.viewed", targetType: "session", targetID: "s-1"}
	rr.record(view)
	view.at = t0.Add(time.Minute)
	rr.record(view)
	view.actorID = "op-2"
	rr.record(view)

	stop()
	<-rr.done
	var lines []string
	for _, e := range sink.entries() {
		lines = append(lines, e.action+" "+e.actorID+" "+e.targetID+e.search)
	}
	sort.Strings(lines)
	want := []string{
		"guest_data.searched op-1 john",
		"guest_data.searched op-1 mary",
		"guest_data.viewed op-1 s-1",
		"guest_data.viewed op-2 s-1",
	}
	if len(lines) != len(want) {
		t.Fatalf("recorded %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("recorded %q, want %q", lines, want)
		}
	}
}

// A full queue writes the read directly instead of dropping it, and does not wait for the queue to drain.
func TestReadRecorderWritesWhenTheQueueIsFull(t *testing.T) {
	var direct atomic.Int64
	rr := newReadRecorder(func(context.Context, readEntry) error { direct.Add(1); return nil })
	done := make(chan struct{})
	go func() {
		for i := 0; i < readQueueSize+50; i++ {
			rr.record(readEntry{action: "guest_data.viewed"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("record blocked with nobody draining the queue")
	}
	if direct.Load() != 50 || rr.dropped.Load() != 0 {
		t.Fatalf("written directly %d, dropped %d; want 50 and 0", direct.Load(), rr.dropped.Load())
	}
	// Only a failing direct write is counted as lost.
	rr = newReadRecorder(func(context.Context, readEntry) error { return context.DeadlineExceeded })
	for i := 0; i < readQueueSize+3; i++ {
		rr.record(readEntry{action: "guest_data.viewed"})
	}
	if rr.dropped.Load() != 3 {
		t.Fatalf("dropped %d, want 3", rr.dropped.Load())
	}
}

// Every route in guestDataReads must exist exactly as written: a pattern that drifted from the router would
// silently stop recording.
func TestGuestDataReadRoutesExist(t *testing.T) {
	s := &server{surfaces: newMountedSurfaces()}
	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		for name, routes := range map[string]func() http.Handler{
			"guest-accounts":        s.guestAccountsRoutes,
			"sessions":              s.sessionsRoutes,
			"pms-stays":             s.pmsStaysRoutes,
			"pms-events":            s.pmsEventsRoutes,
			"guest-signin-attempts": s.signInAttemptsRoutes,
			"post-stay-profiles":    s.postStayProfilesRoutes,
			"usage":                 s.usageRoutes,
			"guest-data-rights":     s.guestDataRightsRoutes,
			"network":               s.networkRoutes,
			// Added with the P9 M4 coverage: their detail reads are recorded too.
			"vouchers":                  s.vouchersRoutes,
			"guest-signin-restrictions": s.signInRestrictionsRoutes,
			"pms-event-decisions":       s.pmsEventDecisionsRoutes,
		} {
			mountResource(r, s, name, routes)
		}
		r.Route("/client-devices", func(r chi.Router) { r.Mount("/", s.clientDevicesAdminRoutes()) })
	})
	have := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet {
			have[(&chi.Context{RoutePatterns: []string{route}}).RoutePattern()] = true
		}
		return nil
	})
	for pattern := range guestDataReads {
		if !have[pattern] {
			t.Errorf("guestDataReads names %s, which the router does not serve", pattern)
		}
	}
}

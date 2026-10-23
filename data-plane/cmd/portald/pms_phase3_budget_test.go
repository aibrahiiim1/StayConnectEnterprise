package main

// pms_phase3_handlers_test.go proves non-successes are the same bytes; these prove they take the same time.
// Most assert the deadline the handler waits for, not measured wall clock, which would be flaky and would
// pass for "about" equal paths.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// recordingClock starts at a real instant (so derived context deadlines are sane) and advances only when the handler waits.
type recordingClock struct {
	mu    sync.Mutex
	now   time.Time
	start time.Time
	waits []time.Time
}

func newRecordingClock() *recordingClock {
	n := time.Now()
	return &recordingClock{now: n, start: n}
}

func (c *recordingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *recordingClock) SleepUntil(_ context.Context, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, t)
	if t.After(c.now) {
		c.now = t
	}
}

func (c *recordingClock) waited() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.waits) != 1 {
		return time.Time{}, false
	}
	return c.waits[0], true
}

func (c *recordingClock) offset() (time.Duration, bool) {
	d, ok := c.waited()
	if !ok {
		return 0, false
	}
	return d.Sub(c.start), true
}

// Cases span how early a failure can be known (two never touch the network); a budget applied only to
// upstream paths would let local ones return first and open a room-enumeration oracle.
func TestPhase3EveryFailureLeavesAtTheSameOffset(t *testing.T) {
	cases := []struct {
		name    string
		stub    *scdStub
		body    map[string]any
		rawBody []byte // set instead of body when the request must be malformed
		noARP   bool
	}{
		{name: "malformed body, decided before anything is contacted",
			stub: &scdStub{}, rawBody: []byte(`{"room":`)},
		{name: "device not on the guest network, decided from a local lookup",
			stub: &scdStub{}, body: map[string]any{"room": "412", "request_id": "r"}, noARP: true},
		{name: "the resolver refused",
			stub: &scdStub{resolve: map[string]any{"outcome": "NOT_VERIFIED"}},
			body: map[string]any{"room": "999", "request_id": "r"}},
		{name: "verified with nothing on offer",
			stub: &scdStub{resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "c",
				"offers": []map[string]any{}}},
			body: map[string]any{"room": "412", "request_id": "r"}},
		{name: "the grant refused",
			stub: &scdStub{
				resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "c",
					"offers": []map[string]any{{"package_revision_id": "p", "code": "STAY"}}},
				grant: map[string]any{"outcome": "NOT_VERIFIED"}},
			body: map[string]any{"room": "412", "request_id": "r"}},
		{name: "the transport failed",
			stub: &scdStub{failWith: errors.New("scd socket unavailable")},
			body: map[string]any{"room": "412", "request_id": "r"}},
		{name: "scd is dark and the route is absent",
			stub: &scdStub{status: http.StatusNotFound, resolve: map[string]any{}},
			body: map[string]any{"room": "412", "request_id": "r"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, clk := stubHandlerWithClock(t, c.stub)
			if c.noARP {
				h.arpCache = func(net.IP) (net.HardwareAddr, bool) { return nil, false }
			}
			raw := c.rawBody
			if raw == nil {
				raw, _ = json.Marshal(c.body)
			}
			req := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", bytes.NewReader(raw))
			req.RemoteAddr = "10.77.0.25:51000"
			rec := httptest.NewRecorder()
			h.authPMSPhase3(rec, req)

			var out guestPMSResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.OK {
				t.Fatalf("expected the uniform non-success, got %q", rec.Body.String())
			}
			off, ok := clk.offset()
			if !ok {
				t.Fatalf("the handler did not wait out the budget exactly once (waits=%d)", len(clk.waits))
			}
			if off != phase3FailureBudget {
				t.Fatalf("left at %v, want exactly %v", off, phase3FailureBudget)
			}
		})
	}
}

// Success is not padded; its content already differs.
func TestPhase3SuccessDoesNotWaitOutTheBudget(t *testing.T) {
	stub := &scdStub{
		resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx",
			"offers": []map[string]any{{"package_revision_id": "pkg", "code": "STAY"}}},
		grant: map[string]any{"outcome": "VERIFIED", "session_id": "sess", "entitlement_id": "ent"},
	}
	h, clk := stubHandlerWithClock(t, stub)
	_, out := phase3Post(t, h, map[string]any{"room": "412", "request_id": "r"})
	if !out.OK || out.SessionID != "sess" {
		t.Fatalf("setup: the guest was not connected: %+v", out)
	}
	if len(clk.waits) != 0 {
		t.Fatalf("a successful connection was delayed by the failure budget (waits=%v)", clk.waits)
	}
}

// The choice step is a success too.
func TestPhase3ChoiceStepDoesNotWaitOutTheBudget(t *testing.T) {
	stub := &scdStub{resolve: map[string]any{"outcome": "VERIFIED", "auth_context_id": "ctx",
		"offers": []map[string]any{
			{"package_revision_id": "p1", "code": "STANDARD"},
			{"package_revision_id": "p2", "code": "PREMIUM"}}}}
	h, clk := stubHandlerWithClock(t, stub)
	_, out := phase3Post(t, h, map[string]any{"room": "412", "request_id": "r"})
	if !out.NeedsChoice || len(out.Choices) != 2 {
		t.Fatalf("setup: the choice was not presented: %+v", out)
	}
	if len(clk.waits) != 0 {
		t.Fatalf("the choice step was delayed by the failure budget (waits=%v)", clk.waits)
	}
}

// blockingStub never answers, like a wedged PMS or deadlocked scd.
type blockingStub struct {
	entered chan struct{}
	once    sync.Once
}

func (s *blockingStub) RoundTrip(req *http.Request) (*http.Response, error) {
	s.once.Do(func() { close(s.entered) })
	<-req.Context().Done() // released only when the budget (or the guest) gives up
	return nil, req.Context().Err()
}

// The ceiling: an unanswered upstream hop is abandoned at the budget. Uses the real clock because the
// property is that the deadline actually fires.
func TestPhase3AnUnansweringUpstreamIsAbandonedAtTheBudget(t *testing.T) {
	stub := &blockingStub{entered: make(chan struct{})}
	h := &handler{scd: &http.Client{Transport: stub}} // nil clock: the real one
	h.arpCache = func(net.IP) (net.HardwareAddr, bool) {
		mac, _ := net.ParseMAC("02:00:00:aa:00:01")
		return mac, true
	}

	raw, _ := json.Marshal(map[string]any{"room": "412", "request_id": "r"})
	req := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", bytes.NewReader(raw))
	req.RemoteAddr = "10.77.0.25:51000"
	rec := httptest.NewRecorder()

	started := time.Now()
	h.authPMSPhase3(rec, req)
	elapsed := time.Since(started)

	select {
	case <-stub.entered:
	default:
		t.Fatal("setup: the upstream hop was never attempted")
	}
	var out guestPMSResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.OK {
		t.Fatalf("a wedged upstream did not produce the uniform non-success: %q", rec.Body.String())
	}
	// The slack absorbs runner scheduling; the failure this catches is the 5s client timeout.
	if elapsed > phase3FailureBudget+500*time.Millisecond {
		t.Fatalf("a wedged upstream took %v, past the %v budget", elapsed, phase3FailureBudget)
	}
	if elapsed < phase3FailureBudget {
		t.Fatalf("a wedged upstream answered in %v, before the %v budget", elapsed, phase3FailureBudget)
	}
}

// A closed page must not pin a goroutine for the rest of the budget.
func TestPhase3TheWaitReleasesWhenTheGuestGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		realClock{}.SleepUntil(ctx, time.Now().Add(10*time.Second))
		done <- time.Since(start)
	}()
	select {
	case d := <-done:
		if d > time.Second {
			t.Fatalf("the wait ignored cancellation for %v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wait did not release when the request was cancelled")
	}
}

// Waiting after the bytes are on the wire would delay nothing an attacker can measure.
func TestPhase3TheWaitHappensBeforeTheBodyIsWritten(t *testing.T) {
	stub := &scdStub{resolve: map[string]any{"outcome": "NOT_VERIFIED"}}
	h, clk := stubHandlerWithClock(t, stub)

	raw, _ := json.Marshal(map[string]any{"room": "999", "request_id": "r"})
	req := httptest.NewRequest(http.MethodPost, "/auth/pms/phase3", bytes.NewReader(raw))
	req.RemoteAddr = "10.77.0.25:51000"
	rec := &orderRecorder{ResponseRecorder: httptest.NewRecorder(), clk: clk}
	h.authPMSPhase3(rec, req)

	if !rec.waitedFirst {
		t.Fatal("the response was written before the budget was spent, so the budget delayed nothing")
	}
}

// orderRecorder notes whether the clock had waited before the first byte was written.
type orderRecorder struct {
	*httptest.ResponseRecorder
	clk         *recordingClock
	waitedFirst bool
	wrote       bool
}

func (o *orderRecorder) WriteHeader(code int) {
	o.note()
	o.ResponseRecorder.WriteHeader(code)
}

func (o *orderRecorder) Write(b []byte) (int, error) {
	o.note()
	return o.ResponseRecorder.Write(b)
}

func (o *orderRecorder) note() {
	if o.wrote {
		return
	}
	o.wrote = true
	_, o.waitedFirst = o.clk.waited()
}

// A budget that quietly changed the answer would trade one leak for another.
func TestPhase3TheBudgetedFailureIsStillTheUniformBody(t *testing.T) {
	h, _ := stubHandlerWithClock(t, &scdStub{resolve: map[string]any{"outcome": "NOT_VERIFIED"}})
	rec, _ := phase3Post(t, h, map[string]any{"room": "999", "request_id": "r"})
	var out guestPMSResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("undecodable: %v", err)
	}
	if out.OK || out.Message != guestAuthMessage || leaksDetail(out) {
		t.Fatalf("the budgeted failure is not the uniform body: %q", rec.Body.String())
	}
	if term := bodyMentionsForbiddenTerm(out); term != "" {
		t.Fatalf("the budgeted failure mentions %q", term)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
}

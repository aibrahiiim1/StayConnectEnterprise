package main

// THE RESPONSE-TIME BUDGET, held to its contract.
//
// pms_phase3_handlers_test.go proves every guest-visible non-success is the same BYTES. These prove it is the
// same TIME, which is the other half of the same property: a uniform body that arrives 4µs after a malformed
// request and 400ms after a wrong room still tells an attacker which rooms exist.
//
// Most of these assert the DEADLINE the handler waits for rather than measuring elapsed wall clock. A
// measurement would be a flake generator on a loaded CI runner — and worse, it would pass for the wrong
// reason, since two paths that both take "about 1.2 seconds" is exactly the property that has to be exact.

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

// recordingClock stands in for the wall clock. It starts at a real instant so that the context deadlines the
// handler derives are sane against the runtime's own timers, then advances only when the handler waits.
type recordingClock struct {
	mu    sync.Mutex
	now   time.Time
	start time.Time
	waits []time.Time // every deadline SleepUntil was asked to wait for, in order
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

// waited reports the single deadline the handler waited for, and whether it waited exactly once.
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

// EVERY non-success leaves at the SAME offset, and it is the budget.
//
// The cases are chosen to span the full range of how early a failure can be known: two of them never touch
// the network at all (the fastest possible answer), one is refused by the resolver, one by the grant, one by
// the transport. If the budget were a floor-with-jitter or applied only to the upstream paths, the local ones
// would come back first and the room-enumeration oracle would be wide open.
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

			// it really is a non-success
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

// SUCCESS is not padded. A guest who is in should be in immediately; success is already distinguishable by
// its content, so spending the guest's patience would hide nothing.
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

// The CHOICE step is a success too — the guest proved who they are and is being asked to pick. Padding it
// would make choosing feel like failing, and it discloses nothing that the success case does not.
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

// blockingStub never answers. It is how a wedged PMS, a saturated interface or a deadlocked scd looks from
// portald: not an error, just silence.
type blockingStub struct {
	entered chan struct{}
	once    sync.Once
}

func (s *blockingStub) RoundTrip(req *http.Request) (*http.Response, error) {
	s.once.Do(func() { close(s.entered) })
	<-req.Context().Done() // released only when the budget (or the guest) gives up
	return nil, req.Context().Err()
}

// THE CEILING. A padded-but-unbounded budget leaks exactly as much as no budget at all: a nine-second answer
// is distinguishable from a 1.2-second one no matter what floor sits underneath it. So an upstream hop that
// has not answered when the budget expires is abandoned, and the guest gets the uniform non-success there.
//
// This one uses the REAL clock, because the property under test is that the deadline actually fires — a fake
// clock would prove only that the arithmetic was written down.
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
	// Bounded above: the whole point. The slack absorbs scheduling on a loaded runner without weakening the
	// assertion — the failure mode this catches is the 5s client timeout, which is 4x outside it.
	if elapsed > phase3FailureBudget+500*time.Millisecond {
		t.Fatalf("a wedged upstream took %v, past the %v budget", elapsed, phase3FailureBudget)
	}
	// Bounded below: it must not have come back early either, or a wedged PMS would be distinguishable from
	// a wrong room by being SLOWER to fail than the padded fast paths.
	if elapsed < phase3FailureBudget {
		t.Fatalf("a wedged upstream answered in %v, before the %v budget", elapsed, phase3FailureBudget)
	}
}

// A guest who closes the page must not pin a goroutine for the rest of the budget. The wait honours the
// request's cancellation; on an appliance serving a lobby full of devices, a budget that outlived its request
// would be a slow leak under exactly the load that matters.
func TestPhase3TheWaitReleasesWhenTheGuestGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the guest is already gone

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

// The budget is spent BEFORE the body reaches the guest. Waiting afterwards would be indistinguishable from
// not waiting at all — the bytes are already on the wire and the clock an attacker reads stops when they land.
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

// orderRecorder notes whether the clock had already been asked to wait by the time the first byte was written.
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

// A sanity check on the stub itself: the uniform failure body is still the uniform failure body when it comes
// out of a budgeted path. A budget that quietly changed the answer would trade one leak for another.
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

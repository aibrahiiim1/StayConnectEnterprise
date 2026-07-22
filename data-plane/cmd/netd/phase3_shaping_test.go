package main

// Tests for the SINGLE Phase-3 shaping writer (ADR-0002). These prove the properties that make one writer
// safe: ordering, idempotency, convergence after a restart, and a truthful degraded report.

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
)

type tcCall struct {
	op     string // ensure | add | delete
	bridge string
	ip     string
	down   int
	up     int
}

type fakeTC struct {
	mu       sync.Mutex
	calls    []tcCall
	failAdd  map[string]error
	failDel  map[string]error
	installs map[string]tcCall // current desired state, keyed by ip — used to prove idempotency
}

func newFakeTC() *fakeTC {
	return &fakeTC{failAdd: map[string]error{}, failDel: map[string]error{}, installs: map[string]tcCall{}}
}

func (f *fakeTC) EnsureBridgeInfra(ctx context.Context, bridge string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tcCall{op: "ensure", bridge: bridge})
	return nil
}
func (f *fakeTC) AddSession(ctx context.Context, bridge string, ip net.IP, down, up int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := tcCall{op: "add", bridge: bridge, ip: ip.String(), down: down, up: up}
	f.calls = append(f.calls, c)
	if err, ok := f.failAdd[ip.String()]; ok {
		return err
	}
	f.installs[ip.String()] = c
	return nil
}
func (f *fakeTC) DeleteSession(ctx context.Context, bridge string, ip net.IP) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tcCall{op: "delete", bridge: bridge, ip: ip.String()})
	if err, ok := f.failDel[ip.String()]; ok {
		return err
	}
	delete(f.installs, ip.String())
	return nil
}
func (f *fakeTC) snapshot() ([]tcCall, map[string]tcCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]tcCall, len(f.installs))
	for k, v := range f.installs {
		out[k] = v
	}
	return append([]tcCall(nil), f.calls...), out
}

func plan() shapingPlanRequest {
	return shapingPlanRequest{
		Tear: []shapingSession{
			{SessionID: "gone-1", IP: "10.0.0.8", Bridge: "br-guest"},
			{SessionID: "gone-2", IP: "10.0.0.9", Bridge: "br-guest"},
		},
		Shape: []shapingSession{
			{SessionID: "live-1", IP: "10.0.0.1", Bridge: "br-guest", DownKbps: 8000, UpKbps: 3000},
			{SessionID: "live-2", IP: "10.0.0.2", Bridge: "br-lan", DownKbps: 4000, UpKbps: 1500},
		},
	}
}

// Teardown happens before shaping. The other order leaves a window in which access that has ended is still
// forwarded while capacity is handed to whoever is still entitled.
func TestShapingTearsDownBeforeShaping(t *testing.T) {
	tc := newFakeTC()
	p := &phase3Shaping{shp: tc}
	res := p.apply(context.Background(), plan())
	if res.TornDown != 2 || res.Shaped != 2 || res.Degraded {
		t.Fatalf("unexpected result: %+v", res)
	}
	calls, _ := tc.snapshot()
	firstAdd, lastDelete := -1, -1
	for i, c := range calls {
		if c.op == "add" && firstAdd == -1 {
			firstAdd = i
		}
		if c.op == "delete" {
			lastDelete = i
		}
	}
	if lastDelete > firstAdd {
		t.Fatalf("shaping was applied before teardown finished: %+v", calls)
	}
}

// Applying the same plan repeatedly converges: the desired state is identical every time, which is what makes
// restart and reboot recovery ordinary rather than special.
func TestShapingIsIdempotentAndConverges(t *testing.T) {
	tc := newFakeTC()
	p := &phase3Shaping{shp: tc}
	p.apply(context.Background(), plan())
	_, first := tc.snapshot()

	// a "restart": a brand-new writer with no memory at all applies the same plan
	p2 := &phase3Shaping{shp: tc}
	p2.apply(context.Background(), plan())
	_, second := tc.snapshot()

	if len(first) != len(second) {
		t.Fatalf("state diverged across a restart: %v vs %v", first, second)
	}
	for ip, c := range first {
		if second[ip] != c {
			t.Fatalf("session %s diverged: %+v vs %+v", ip, c, second[ip])
		}
	}
	if len(second) != 2 {
		t.Fatalf("installed %d sessions, want exactly the 2 entitled ones", len(second))
	}
}

// A plan that could not be fully applied is reported as degraded rather than treated as success — especially a
// TEARDOWN failure, which means traffic may still be forwarded for access that has ended.
func TestPartialApplicationIsTruthfullyDegraded(t *testing.T) {
	tc := newFakeTC()
	tc.failDel["10.0.0.8"] = errors.New("class busy")
	p := &phase3Shaping{shp: tc}
	res := p.apply(context.Background(), plan())
	if !res.Degraded || res.Failed != 1 {
		t.Fatalf("a failed teardown was not reported: %+v", res)
	}
	if res.TornDown != 1 || res.Shaped != 2 {
		t.Fatalf("the rest of the plan was abandoned: %+v", res)
	}
	if len(res.Problems) == 0 {
		t.Fatal("degraded state must say what went wrong")
	}
}

// A session with no addressing or no rates is left UNSHAPED. Installing a zero rate would be a silent
// full-speed pass — worse than not shaping it, because it looks deliberate.
func TestUnusablePlanEntriesAreNotShaped(t *testing.T) {
	tc := newFakeTC()
	p := &phase3Shaping{shp: tc}
	res := p.apply(context.Background(), shapingPlanRequest{
		Shape: []shapingSession{
			{SessionID: "no-ip", Bridge: "br-guest", DownKbps: 1000, UpKbps: 500},
			{SessionID: "no-rates", IP: "10.0.0.3", Bridge: "br-guest"},
			{SessionID: "no-bridge", IP: "10.0.0.4", DownKbps: 1000, UpKbps: 500},
		},
	})
	if res.Shaped != 0 || res.Failed != 3 || !res.Degraded {
		t.Fatalf("unusable entries were shaped or hidden: %+v", res)
	}
	_, installed := tc.snapshot()
	if len(installed) != 0 {
		t.Fatalf("something was installed for an unusable entry: %v", installed)
	}
}

// The HTTP surface accepts only a COMPLETE plan and rejects anything it does not understand, so the two sides
// cannot drift into exchanging deltas.
func TestShapingEndpointContract(t *testing.T) {
	tc := newFakeTC()
	srv := &server{phase3: &phase3Shaping{shp: tc}}
	h := http.HandlerFunc(srv.phase3ShapingHandler)

	raw, _ := json.Marshal(plan())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/phase3/shaping", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("a valid plan was rejected: %d %s", rec.Code, rec.Body.String())
	}
	var res shapingPlanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 2 || res.TornDown != 2 {
		t.Fatalf("unexpected response: %+v", res)
	}

	// an unknown field is a contract drift signal, not something to silently ignore
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/v1/phase3/shaping",
		bytes.NewReader([]byte(`{"shape":[],"tear":[],"remove_one":"s1"}`))))
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field was accepted: %d", rec2.Code)
	}
}

// Concurrent submissions must not interleave: two half-applied plans would leave the kernel in a state
// neither plan describes.
func TestConcurrentSubmissionsDoNotInterleave(t *testing.T) {
	tc := newFakeTC()
	p := &phase3Shaping{shp: tc}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.apply(context.Background(), plan())
		}()
	}
	wg.Wait()
	_, installed := tc.snapshot()
	if len(installed) != 2 {
		t.Fatalf("installed %d sessions after concurrent submissions, want exactly 2", len(installed))
	}
}

// Health must tell the truth about enforcement. A plan that failed to apply is invisible otherwise, and
// "shaping looks fine" is the most expensive kind of wrong.
func TestHealthReportsTruthfulShapingState(t *testing.T) {
	tc := newFakeTC()
	p := &phase3Shaping{shp: tc}

	// nothing submitted yet: not degraded, and no last-applied claim
	st := p.status()
	if st["degraded"] != false {
		t.Fatalf("a writer that has done nothing reported degraded: %v", st)
	}
	if _, ok := st["last_applied_at"]; ok {
		t.Fatal("a writer that has applied nothing claimed a last-applied time")
	}

	// a clean apply
	p.apply(context.Background(), plan())
	st = p.status()
	if st["degraded"] != false || st["last_applied_at"] == nil {
		t.Fatalf("a clean apply was not reported: %v", st)
	}

	// a failed teardown must surface, with a reason
	tc.failDel["10.0.0.8"] = errors.New("class busy")
	p.apply(context.Background(), plan())
	st = p.status()
	if st["degraded"] != true || st["problem"] == nil {
		t.Fatalf("a failed apply was not reported as degraded: %v", st)
	}

	// and recovering clears it, so a stale problem cannot linger
	delete(tc.failDel, "10.0.0.8")
	p.apply(context.Background(), plan())
	if st := p.status(); st["degraded"] != false {
		t.Fatalf("a recovered writer still reports degraded: %v", st)
	}
}

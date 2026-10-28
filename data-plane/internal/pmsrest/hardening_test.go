package pmsrest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Audit P7 L5: a Mews cursor that never advances fails as an invalid response within a few requests, instead of
// paging up to 10,000 times and returning a partial list.
func TestMewsHardening_StuckCursorFailsFast(t *testing.T) {
	withPageSize(t, &mewsPage, 2)
	f, srv := newMewsFake(t)
	page := map[string]any{"Reservations": []any{
		mewsRes("res-A", "Started", "cust1", "Customer", "r101", "2026-09-23T14:00:00Z", "", "2026-09-26T10:00:00Z", "u1"),
		mewsRes("res-B", "Started", "cust2", "Customer", "r102", "2026-09-22T14:00:00Z", "", "2026-09-25T10:00:00Z", "u2"),
	}, "Cursor": "stuck"}
	f.resPages = map[string]map[string]any{"": page, "stuck": page}
	c := newMewsClient(t, srv.URL, &sleepRecorder{}, "")
	_, err := c.Snapshot(t.Context())
	if KindOf(err) != KindInvalidResponse {
		t.Fatalf("err = %v, want INVALID_RESPONSE", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if p == "/api/connector/v1/reservations/getAll/2023-06-06" {
			n++
		}
	}
	if n > 3 {
		t.Fatalf("reservation requests = %d, want at most 3", n)
	}
}

// Audit P7 L4: a redirect is answered, never followed, so a body carrying credentials is not replayed to another host.
func TestRESTHardening_RedirectNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	c := newMewsClient(t, redirector.URL, &sleepRecorder{}, "")
	_, err := c.Probe(t.Context())
	if err == nil || KindOf(err) != KindRequest {
		t.Fatalf("err = %v, want REQUEST_REJECTED", err)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the redirect target received %d requests, want 0", n)
	}
}

// A caller's own client is not mutated; the redirect policy applies to a copy.
func TestRESTHardening_CallerClientNotMutated(t *testing.T) {
	hc := &http.Client{}
	o := Options{HTTP: hc}
	o.defaults()
	if hc.CheckRedirect != nil {
		t.Fatal("the caller's client was mutated")
	}
	if o.HTTP.CheckRedirect == nil {
		t.Fatal("the client in use follows redirects")
	}
}

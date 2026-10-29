package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// AD-7: a page of a guest list is recorded as guest_data.listed with its filter and never with search text, once per person
// and list per repeat window.
func TestListPagesAreRecordedWithTheirFilterAndNoSearchText(t *testing.T) {
	sink := &readSink{}
	rr := newReadRecorder(sink.write)
	s := &server{reads: rr}
	ctx, stop := context.WithCancel(context.Background())
	go rr.run(ctx, 10*time.Millisecond)

	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Use(s.recordGuestDataReads)
		for _, p := range []string{"/guest-accounts", "/vouchers", "/pms-stays"} {
			r.Get(p, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		}
	})
	get := func(target string, hdr map[string]string) {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	// A filter with a typed name in it: the allow-listed key is kept, the free-text key and the spaced value are not.
	get("/edge/v1/vouchers?status=active&q=Maria+Lopez&from=2026-10-01&room=1204", nil)
	get("/edge/v1/vouchers?status=expired", nil) // the same list again: not a second entry
	get("/edge/v1/guest-accounts", nil)

	stop()
	<-rr.done
	got := sink.entries()
	if len(got) != 2 {
		t.Fatalf("recorded %d entries, want 2 (one per list): %+v", len(got), got)
	}
	byType := map[string]readEntry{}
	for _, e := range got {
		if e.action != "guest_data.listed" {
			t.Errorf("%s was recorded as %s", e.targetType, e.action)
		}
		byType[e.targetType] = e
	}
	v, ok := byType["vouchers"]
	if !ok || byType["client_accounts"].targetType == "" {
		t.Fatalf("lists recorded: %v", byType)
	}
	f, _ := v.payload["filter"].(map[string]any)
	if f["status"] != "active" || f["from"] != "2026-10-01" {
		t.Errorf("the filter was not recorded: %v", v.payload)
	}
	for _, banned := range []string{"q", "room", "search"} {
		if _, has := f[banned]; has {
			t.Errorf("free text %q reached the audit log: %v", banned, f)
		}
	}
	if _, has := v.payload["search"]; has || v.search != "" {
		t.Errorf("a list view carries search text: %+v", v)
	}
}

func TestListFilterKeepsOnlyPlainTokens(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x?status=active&state=Maria%20Lopez&type=a%40b.com&sort=created_at&from=2026-10-01T00:00:00Z", nil)
	f := listFilter(req)
	if f["status"] != "active" || f["sort"] != "created_at" {
		t.Errorf("plain tokens dropped: %v", f)
	}
	if _, has := f["state"]; has {
		t.Errorf("a value with a space was kept: %v", f)
	}
	if _, has := f["type"]; has {
		t.Errorf("an address was kept: %v", f)
	}
}

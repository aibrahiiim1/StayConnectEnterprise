package pmsrest

// MEWS CONNECTOR API CONTRACT TESTS.
//
// The fixtures below are shaped exactly like the documented request/response samples at
// https://docs.mews.com/connector-api/operations/reservations (getAll/2023-06-06), .../customers,
// .../companionships and .../resources, with pagination per .../guidelines/pagination and rate limiting per
// .../guidelines/requests. No request here reaches Mews.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

type mewsFake struct {
	t        *testing.T
	mu       sync.Mutex
	requests []map[string]any
	paths    []string
	// reservation pages keyed by incoming cursor ("" = first page)
	resPages map[string]map[string]any
	status   []int // statuses to return first for reservations (then 200)
	headers  []map[string]string
	raw      string // when set, returned verbatim for reservations
}

func str2(v any) string { s, _ := v.(string); return s }

func (f *mewsFake) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("request body is not JSON: %v", err)
	}
	f.requests = append(f.requests, body)
	f.paths = append(f.paths, r.URL.Path)
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		f.t.Errorf("Mews operations are JSON POSTs, got %s %s", r.Method, r.Header.Get("Content-Type"))
	}
	if body["ClientToken"] != "CT-demo" || body["AccessToken"] != "AT-demo" || body["Client"] != mewsClientName {
		f.t.Errorf("tokens/client missing from %s: %v", r.URL.Path, body)
	}
	lim, _ := body["Limitation"].(map[string]any)
	if lim == nil || lim["Count"] == nil {
		f.t.Errorf("%s without the required Limitation.Count", r.URL.Path)
	}
	cursor := str2(lim["Cursor"])
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/connector/v1/reservations/getAll/2023-06-06":
		if len(f.status) > 0 {
			st := f.status[0]
			f.status = f.status[1:]
			if len(f.headers) > 0 {
				for k, v := range f.headers[0] {
					w.Header().Set(k, v)
				}
				f.headers = f.headers[1:]
			}
			w.WriteHeader(st)
			_, _ = w.Write([]byte(`{"Message":"error"}`))
			return
		}
		if f.raw != "" {
			_, _ = w.Write([]byte(f.raw))
			return
		}
		_ = json.NewEncoder(w).Encode(f.resPages[cursor])
	case "/api/connector/v1/resources/getAll":
		pages := map[string]any{
			"": map[string]any{"Resources": []any{
				map[string]any{"Id": "r101", "Name": "101", "IsActive": true, "Data": map[string]any{"Discriminator": "Space"}},
				map[string]any{"Id": "r102", "Name": "102", "IsActive": true, "Data": map[string]any{"Discriminator": "Space"}},
			}, "Cursor": "rc1"},
			"rc1": map[string]any{"Resources": []any{
				map[string]any{"Id": "r103", "Name": "103", "IsActive": true, "Data": map[string]any{"Discriminator": "Space"}},
				map[string]any{"Id": "r999", "Name": "Parking 1", "IsActive": true, "Data": map[string]any{"Discriminator": "Object"}},
			}, "Cursor": "rc2"},
			"rc2": map[string]any{"Resources": []any{
				map[string]any{"Id": "r104", "Name": "104", "IsActive": false, "Data": map[string]any{"Discriminator": "Space"}},
			}, "Cursor": nil},
		}
		_ = json.NewEncoder(w).Encode(pages[cursor])
	case "/api/connector/v1/customers/getAll":
		names := map[string][2]string{
			"cust1": {"Jana", "Nováková"}, "cust2": {"John", "Smith"}, "cust3": {"Aiko", "Tanaka"}, "cust4": {"Petr", "Novák"},
		}
		out := []any{}
		ids, _ := body["CustomerIds"].([]any)
		for _, id := range ids {
			if n, ok := names[str2(id)]; ok {
				out = append(out, map[string]any{"Id": str2(id), "FirstName": n[0], "LastName": n[1]})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Customers": out, "Cursor": nil})
	case "/api/connector/v1/companionships/getAll":
		comp := map[string][]string{"res-A": {"cust1", "cust4"}, "res-C": {"cust3"}}
		out := []any{}
		ids, _ := body["ReservationIds"].([]any)
		for _, id := range ids {
			for _, c := range comp[str2(id)] {
				out = append(out, map[string]any{"Id": "cp-" + c, "CustomerId": c, "ReservationId": str2(id), "ReservationGroupId": "g1"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Companionships": out, "Cursor": nil})
	default:
		f.t.Errorf("unexpected Mews path %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func mewsRes(id, state, account, accountType, resource, schedStart, actualStart, schedEnd, updated string) map[string]any {
	m := map[string]any{
		"Id": id, "ServiceId": "svc1", "AccountId": account, "AccountType": accountType, "Number": "52",
		"State": state, "Origin": "Connector", "CreatedUtc": "2026-09-01T10:00:00Z", "UpdatedUtc": updated,
		"CancelledUtc": nil, "AssignedResourceId": resource, "StartUtc": schedStart, "EndUtc": schedEnd,
		"ScheduledStartUtc": schedStart, "ActualStartUtc": nil, "ScheduledEndUtc": schedEnd, "ActualEndUtc": nil,
		"Options": map[string]any{"OwnerCheckedIn": true}, "PersonCounts": []any{},
	}
	if actualStart != "" {
		m["ActualStartUtc"] = actualStart
	}
	return m
}

func newMewsFake(t *testing.T) (*mewsFake, *httptest.Server) {
	f := &mewsFake{t: t, resPages: map[string]map[string]any{
		"": {"Reservations": []any{
			// 22:30Z on the 23rd is 00:30 on the 24th in Prague: the property's date is the 24th.
			mewsRes("res-A", "Started", "cust1", "Customer", "r101", "2026-09-23T14:00:00Z", "2026-09-23T22:30:00Z", "2026-09-26T10:00:00Z", "2026-09-23T22:31:00Z"),
			mewsRes("res-B", "Started", "cust2", "Customer", "r102", "2026-09-22T14:00:00Z", "", "2026-09-25T10:00:00Z", "2026-09-22T15:00:00Z"),
		}, "Cursor": "c1"},
		"c1": {"Reservations": []any{
			mewsRes("res-C", "Started", "comp1", "Company", "r103", "2026-09-20T14:00:00Z", "", "2026-09-30T10:00:00Z", "2026-09-20T15:00:00Z"),
			// a cursor page repeating an item already seen is one reservation, not two
			mewsRes("res-A", "Started", "cust1", "Customer", "r101", "2026-09-23T14:00:00Z", "2026-09-23T22:30:00Z", "2026-09-26T10:00:00Z", "2026-09-23T22:31:00Z"),
		}, "Cursor": "c2"},
		"c2": {"Reservations": []any{}, "Cursor": nil},
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, srv
}

func newMewsClient(t *testing.T, base string, rec *sleepRecorder, enterprise string) Client {
	c, err := New("mews", map[string]any{"platform_url": base, "enterprise_id": enterprise},
		[]byte(`{"client_token":"CT-demo","access_token":"AT-demo"}`), testOptions(t, "Europe/Prague", rec))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMewsContract_SnapshotPagesAndNormalises(t *testing.T) {
	withPageSize(t, &mewsPage, 2)
	f, srv := newMewsFake(t)
	c := newMewsClient(t, srv.URL, &sleepRecorder{}, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	snap, err := c.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Reservations) != 3 {
		t.Fatalf("want 3 distinct reservations across pages, got %d", len(snap.Reservations))
	}
	byID := map[string]Reservation{}
	for _, r := range snap.Reservations {
		byID[r.ID] = r
	}
	a := byID["res-A"]
	if a.State != StateInHouse || a.Room != "101" || a.LastName != "Nováková" || a.FirstName != "Jana" {
		t.Fatalf("res-A normalised wrongly: %+v", a)
	}
	if a.Arrival != "260924" || a.Departure != "260926" {
		t.Fatalf("res-A dates %s-%s: ActualStartUtc 22:30Z must be the 24th in Prague", a.Arrival, a.Departure)
	}
	wantSharers := []Guest{{ExternalID: "cust1", FirstName: "Jana", LastName: "Nováková", Primary: true},
		{ExternalID: "cust4", FirstName: "Petr", LastName: "Novák"}}
	if !reflect.DeepEqual(a.Sharers, wantSharers) {
		t.Fatalf("res-A sharers %+v", a.Sharers)
	}
	if b := byID["res-B"]; b.Arrival != "260922" || b.Sharers != nil {
		t.Fatalf("res-B %+v (no actual start: scheduled start; single guest: no sharer list)", b)
	}
	if cc := byID["res-C"]; cc.LastName != "Tanaka" || cc.Room != "103" {
		t.Fatalf("company-owned res-C must take its guest from companionships: %+v", cc)
	}
	rooms := append([]string(nil), snap.Rooms...)
	sort.Strings(rooms)
	if !snap.RoomsKnown || !reflect.DeepEqual(rooms, []string{"101", "102", "103"}) {
		t.Fatalf("rooms %v (Space resources only, active only)", rooms)
	}
	// request shape
	var sawCursor, sawStates, sawEnterprise bool
	for i, p := range f.paths {
		if p != "/api/connector/v1/reservations/getAll/2023-06-06" {
			continue
		}
		b := f.requests[i]
		if lim := b["Limitation"].(map[string]any); str2(lim["Cursor"]) == "c1" {
			sawCursor = true
		}
		if st, _ := b["States"].([]any); len(st) == 1 && st[0] == "Started" {
			sawStates = true
		}
		if e, _ := b["EnterpriseIds"].([]any); len(e) == 1 {
			sawEnterprise = true
		}
		if _, ok := b["CollidingUtc"].(map[string]any); !ok {
			t.Error("in-house list must be bounded by CollidingUtc")
		}
	}
	if !sawCursor || !sawStates || !sawEnterprise {
		t.Fatalf("cursor=%v states=%v enterprise=%v", sawCursor, sawStates, sawEnterprise)
	}
}

func TestMewsContract_429HonoursRetryAfter(t *testing.T) {
	f, srv := newMewsFake(t)
	f.status = []int{429}
	f.headers = []map[string]string{{"Retry-After": "7"}}
	rec := &sleepRecorder{}
	c := newMewsClient(t, srv.URL, rec, "")
	if _, err := c.Probe(t.Context()); err != nil {
		t.Fatalf("a single 429 must be retried: %v", err)
	}
	if len(rec.waits) != 1 || rec.waits[0] != 7*time.Second {
		t.Fatalf("waits %v, want [7s] from Retry-After", rec.waits)
	}
}

func TestMewsContract_429PersistingIsRateLimited(t *testing.T) {
	f, srv := newMewsFake(t)
	f.status = []int{429, 429, 429}
	f.headers = []map[string]string{{"Retry-After": "900"}, {}, {}}
	rec := &sleepRecorder{}
	_, err := newMewsClient(t, srv.URL, rec, "").Probe(t.Context())
	if KindOf(err) != KindRateLimited {
		t.Fatalf("got %v, want RATE_LIMITED", err)
	}
	if rec.waits[0] != 60*time.Second {
		t.Fatalf("an excessive Retry-After must be capped at 60s, waited %v", rec.waits[0])
	}
}

func TestMewsContract_401IsAuthAndNotRetried(t *testing.T) {
	f, srv := newMewsFake(t)
	f.status = []int{401}
	rec := &sleepRecorder{}
	_, err := newMewsClient(t, srv.URL, rec, "").Probe(t.Context())
	if KindOf(err) != KindAuth || len(rec.waits) != 0 || len(f.requests) != 1 {
		t.Fatalf("err=%v waits=%v requests=%d", err, rec.waits, len(f.requests))
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Status != 401 {
		t.Fatalf("status not carried: %v", err)
	}
}

func TestMewsContract_5xxBoundedBackoff(t *testing.T) {
	f, srv := newMewsFake(t)
	f.status = []int{500, 503, 502}
	rec := &sleepRecorder{}
	_, err := newMewsClient(t, srv.URL, rec, "").Probe(t.Context())
	if KindOf(err) != KindUnavailable || len(f.requests) != 3 {
		t.Fatalf("err=%v requests=%d", err, len(f.requests))
	}
	if !reflect.DeepEqual(rec.waits, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("backoff %v", rec.waits)
	}
}

func TestMewsContract_408IsTimeout(t *testing.T) {
	f, srv := newMewsFake(t)
	f.status = []int{408, 408, 408}
	if _, err := newMewsClient(t, srv.URL, &sleepRecorder{}, "").Probe(t.Context()); KindOf(err) != KindTimeout {
		t.Fatalf("got %v", err)
	}
}

func TestMewsContract_MalformedJSON(t *testing.T) {
	f, srv := newMewsFake(t)
	f.raw = `{"Reservations": [ {"Id": "x"`
	if _, err := newMewsClient(t, srv.URL, &sleepRecorder{}, "").Probe(t.Context()); KindOf(err) != KindInvalidResponse {
		t.Fatalf("got %v", err)
	}
	f.raw = `{"Something":"else"}`
	if _, err := newMewsClient(t, srv.URL, &sleepRecorder{}, "").Probe(t.Context()); KindOf(err) != KindInvalidResponse {
		t.Fatalf("a body without Reservations must be refused, got %v", err)
	}
}

func TestMewsContract_ChangesUseUpdatedUtcAndIncludeCheckOuts(t *testing.T) {
	f, srv := newMewsFake(t)
	f.resPages[""] = map[string]any{"Reservations": []any{
		mewsRes("res-B", "Processed", "cust2", "Customer", "r102", "2026-09-22T14:00:00Z", "", "2026-09-25T10:00:00Z", "2026-09-24T11:59:00Z"),
	}, "Cursor": nil}
	c := newMewsClient(t, srv.URL, &sleepRecorder{}, "")
	got, err := c.Changes(t.Context(), fixedNow.Add(-time.Minute), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != StateCheckedOut || got[0].Room != "102" {
		t.Fatalf("got %+v", got)
	}
	for i, p := range f.paths {
		if p == "/api/connector/v1/reservations/getAll/2023-06-06" {
			b := f.requests[i]
			u, _ := b["UpdatedUtc"].(map[string]any)
			if str2(u["StartUtc"]) != "2026-09-24T11:59:00Z" || str2(u["EndUtc"]) != "2026-09-24T12:00:00Z" {
				t.Fatalf("UpdatedUtc %v", u)
			}
			st, _ := b["States"].([]any)
			if len(st) != 2 || st[0] != "Started" || st[1] != "Processed" {
				t.Fatalf("States %v", st)
			}
		}
	}
}

func TestMewsContract_CredentialIncompleteRefused(t *testing.T) {
	_, err := New("mews", map[string]any{"platform_url": "https://api.mews-demo.com"}, []byte(`{"access_token":"x"}`), Options{})
	if KindOf(err) != KindConfig {
		t.Fatalf("got %v", err)
	}
}

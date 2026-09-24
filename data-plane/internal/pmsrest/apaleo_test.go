package pmsrest

// APALEO CONTRACT TESTS.
//
// Shaped after the published Booking v1 and Inventory v1 OpenAPI documents
// (https://api.apaleo.com/swagger/booking-v1/swagger.json, .../inventory-v1/swagger.json) and the
// client-credentials guide (https://apaleo.dev/guides/oauth-connection/simple-client.html). No request here
// reaches Apaleo.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

type apaleoFake struct {
	t            *testing.T
	mu           sync.Mutex
	tokens       int
	tokenStatus  int
	apiFirst401  bool
	reservations []map[string]any
	queries      []url.Values
	units        []string
}

func apaleoRes(id, status, unit, first, last, arrival, departure, modified string, extra ...map[string]any) map[string]any {
	m := map[string]any{
		"id": id, "bookingId": "BK-" + id, "status": status, "arrival": arrival, "departure": departure,
		"created": "2026-09-01T10:00:00+02:00", "modified": modified, "adults": 2,
		"property": map[string]any{"id": "MUC", "code": "MUC", "name": "Hotel Munich"},
		"ratePlan": map[string]any{"id": "MUC-NONREF-SGL"}, "unitGroup": map[string]any{"id": "MUC-SGL"},
		"primaryGuest": map[string]any{"firstName": first, "lastName": last},
		"balance":      map[string]any{"amount": 0, "currency": "EUR"},
	}
	if unit != "" {
		m["unit"] = map[string]any{"id": "MUC-" + unit, "name": unit, "unitGroupId": "MUC-SGL"}
	}
	for _, e := range extra {
		for k, v := range e {
			m[k] = v
		}
	}
	return m
}

func (f *apaleoFake) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/connect/token":
		f.tokens++
		user, pass, ok := r.BasicAuth()
		_ = r.ParseForm()
		if !ok || user != "app-client" || pass != "app-secret" || r.PostForm.Get("grant_type") != "client_credentials" {
			f.t.Errorf("token request shape: basic=%v user=%q grant=%q", ok, user, r.PostForm.Get("grant_type"))
		}
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + strconv.Itoa(f.tokens), "expires_in": 3600, "token_type": "Bearer"})
		return
	}
	if r.Header.Get("Authorization") == "" || r.Header.Get("Authorization")[:7] != "Bearer " {
		f.t.Errorf("API call without a bearer token: %s", r.URL.Path)
	}
	if f.apiFirst401 {
		f.apiFirst401 = false
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	f.queries = append(f.queries, q)
	page, _ := strconv.Atoi(q.Get("pageNumber"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	switch r.URL.Path {
	case "/booking/v1/reservations":
		if q.Get("propertyIds") != "MUC" {
			f.t.Errorf("propertyIds %q", q.Get("propertyIds"))
		}
		start := (page - 1) * size
		if start >= len(f.reservations) {
			w.WriteHeader(http.StatusNoContent) // documented: an empty page is 204
			return
		}
		end := start + size
		if end > len(f.reservations) {
			end = len(f.reservations)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"reservations": f.reservations[start:end], "count": len(f.reservations)})
	case "/booking/v1/reservations/R-GONE":
		_ = json.NewEncoder(w).Encode(apaleoRes("R-GONE", "CheckedOut", "105", "Max", "Mustermann",
			"2026-09-20T15:00:00+02:00", "2026-09-24T11:00:00+02:00", "2026-09-24T10:02:00+02:00"))
	case "/booking/v1/reservations/R-MISSING":
		w.WriteHeader(http.StatusNotFound)
	case "/inventory/v1/units":
		if q.Get("propertyId") != "MUC" {
			f.t.Errorf("propertyId %q", q.Get("propertyId"))
		}
		start := (page - 1) * size
		if start >= len(f.units) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		end := start + size
		if end > len(f.units) {
			end = len(f.units)
		}
		var out []any
		for _, u := range f.units[start:end] {
			out = append(out, map[string]any{"id": "MUC-" + u, "name": u, "description": "Single", "maxPersons": 1,
				"created": "2026-01-01T00:00:00Z", "property": map[string]any{"id": "MUC"}, "status": map[string]any{"isOccupied": false}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"units": out, "count": len(f.units)})
	default:
		f.t.Errorf("unexpected Apaleo path %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func newApaleo(t *testing.T, tz string) (*apaleoFake, Client) {
	f := &apaleoFake{t: t, units: []string{"101", "102", "103", "104"}, reservations: []map[string]any{
		// 00:30 on the 24th in Berlin is 22:30Z on the 23rd: the property's date is the 24th.
		apaleoRes("R1", "InHouse", "101", "Anna", "Schmidt", "2026-09-24T00:30:00+02:00", "2026-09-27T11:00:00+02:00", "2026-09-24T00:31:00+02:00",
			map[string]any{"additionalGuests": []any{map[string]any{"firstName": "Ben", "lastName": "Schmidt"}}}),
		apaleoRes("R2", "InHouse", "102", "Chen", "Li", "2026-09-22T15:00:00+02:00", "2026-09-25T11:00:00+02:00", "2026-09-22T15:05:00+02:00"),
		apaleoRes("R3", "InHouse", "", "No", "Unit", "2026-09-23T15:00:00+02:00", "2026-09-25T11:00:00+02:00", "2026-09-23T15:05:00+02:00"),
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := New("apaleo", map[string]any{"api_url": srv.URL, "identity_url": srv.URL + "/connect/token", "property_id": "MUC"},
		[]byte(`{"client_id":"app-client","client_secret":"app-secret"}`), testOptions(t, tz, &sleepRecorder{}))
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestApaleoContract_SnapshotPagesAndNormalises(t *testing.T) {
	withPageSize(t, &apaleoPage, 2)
	f, c := newApaleo(t, "Europe/Berlin")
	snap, err := c.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f.tokens != 1 {
		t.Fatalf("the token must be reused across calls, fetched %d times", f.tokens)
	}
	if len(snap.Reservations) != 3 || !snap.RoomsKnown || !reflect.DeepEqual(snap.Rooms, []string{"101", "102", "103", "104"}) {
		t.Fatalf("snapshot %+v", snap)
	}
	r1 := snap.Reservations[0]
	if r1.ID != "R1" || r1.State != StateInHouse || r1.Room != "101" || r1.LastName != "Schmidt" || r1.Arrival != "260924" || r1.Departure != "260927" {
		t.Fatalf("R1 %+v", r1)
	}
	if len(r1.Sharers) != 2 || !r1.Sharers[0].Primary || r1.Sharers[1].FirstName != "Ben" {
		t.Fatalf("R1 sharers %+v", r1.Sharers)
	}
	if snap.Reservations[2].Room != "" {
		t.Fatal("a reservation with no unit has no room (the connector skips it)")
	}
	var sawStatus bool
	for _, q := range f.queries {
		if q.Get("status") == "InHouse" && q.Get("pageSize") == "2" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatalf("queries %v", f.queries)
	}
}

func TestApaleoContract_TimezoneIsTheConfiguredPropertyZone(t *testing.T) {
	_, c := newApaleo(t, "UTC")
	snap, err := c.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Reservations[0].Arrival != "260923" {
		t.Fatalf("in UTC the same instant is the 23rd, got %s", snap.Reservations[0].Arrival)
	}
}

func TestApaleoContract_ExpiredTokenRefreshedOnce(t *testing.T) {
	f, c := newApaleo(t, "Europe/Berlin")
	f.apiFirst401 = true
	if _, err := c.Probe(t.Context()); err != nil {
		t.Fatalf("one 401 must be answered with a fresh token: %v", err)
	}
	if f.tokens != 2 {
		t.Fatalf("tokens fetched %d, want 2", f.tokens)
	}
}

func TestApaleoContract_RefusedClientIsAuth(t *testing.T) {
	f, c := newApaleo(t, "Europe/Berlin")
	f.tokenStatus = http.StatusBadRequest // invalid_client
	_, err := c.Probe(t.Context())
	if KindOf(err) != KindAuth || !IsTokenOp(err) {
		t.Fatalf("got %v", err)
	}
}

func TestApaleoContract_ChangesQueryAndCheckOut(t *testing.T) {
	f, c := newApaleo(t, "Europe/Berlin")
	f.reservations = []map[string]any{
		apaleoRes("R2", "CheckedOut", "102", "Chen", "Li", "2026-09-22T15:00:00+02:00", "2026-09-24T11:00:00+02:00", "2026-09-24T13:58:00+02:00",
			map[string]any{"checkOutTime": "2026-09-24T13:58:00+02:00"}),
	}
	got, err := c.Changes(t.Context(), fixedNow.Add(-2*60e9), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].State != StateCheckedOut || got[0].Room != "102" {
		t.Fatalf("got %+v", got)
	}
	q := f.queries[len(f.queries)-1]
	if q.Get("dateFilter") != "Modification" || q.Get("status") != "InHouse,CheckedOut" ||
		q.Get("from") != "2026-09-24T11:58:00Z" || q.Get("to") != "2026-09-24T12:00:00Z" {
		t.Fatalf("change query %v", q)
	}
}

func TestApaleoContract_LookupSkipsNotFound(t *testing.T) {
	_, c := newApaleo(t, "Europe/Berlin")
	got, err := c.Lookup(t.Context(), []string{"R-GONE", "R-MISSING"})
	if err != nil || len(got) != 1 || got[0].State != StateCheckedOut {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestApaleoContract_BasicAuthEncoding(t *testing.T) {
	// the documented header is Basic base64("client_id:client_secret")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("app-client:app-secret"))
	req, _ := http.NewRequest(http.MethodPost, "https://identity.apaleo.com/connect/token", nil)
	req.SetBasicAuth("app-client", "app-secret")
	if req.Header.Get("Authorization") != want {
		t.Fatal("net/http basic auth encoding differs from the documented form")
	}
}

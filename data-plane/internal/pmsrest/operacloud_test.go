package pmsrest

// OPERA CLOUD (OHIP) CONTRACT TESTS.
//
// Shaped after Oracle's published OpenAPI documents (github.com/oracle/hospitality-api-docs):
// rest-api-specs/security/v1/publishedoauth.json (POST /oauth/v1/tokens) and
// rest-api-specs/property/v1/rsv.json (GET /rsv/v1/hotels/{hotelId}/reservations, getHotelReservations).
// No request here reaches Oracle.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

type operaFake struct {
	t       *testing.T
	mu      sync.Mutex
	tokens  int
	queries []url.Values
	inHouse []map[string]any
	byID    map[string]map[string]any
	status  int
}

func operaRes(id, conf, status, room, given, surname, arr, dep, modified string) map[string]any {
	return map[string]any{
		"reservationIdList": []any{
			map[string]any{"id": id, "type": "Reservation"},
			map[string]any{"id": conf, "type": "Confirmation"},
		},
		"roomStay": map[string]any{"arrivalDate": arr, "departureDate": dep, "roomId": room,
			"roomType": "KING", "adultCount": 2},
		"reservationGuest": map[string]any{"id": "P-" + id, "type": "Profile", "givenName": given, "surname": surname,
			"nameType": "Guest"},
		"reservationStatus": status, "computedReservationStatus": status,
		"lastModifyDateTime": modified, "hotelId": "HQ1",
	}
}

func (f *operaFake) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("x-app-key") != "41ecd082-8997-4c69-af34-2f72b83645ff" {
		f.t.Errorf("%s without x-app-key", r.URL.Path)
	}
	if r.URL.Path == "/oauth/v1/tokens" {
		f.tokens++
		user, pass, ok := r.BasicAuth()
		_ = r.ParseForm()
		if !ok || user != "ohip-client" || pass != "ohip-secret" || r.PostForm.Get("grant_type") != "client_credentials" ||
			r.PostForm.Get("scope") != "urn:opc:hgbu:ws:__myscopes__" || r.Header.Get("enterpriseId") != "ENT1" {
			f.t.Errorf("token request shape: user=%q form=%v enterprise=%q", user, r.PostForm, r.Header.Get("enterpriseId"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ohip-token", "expires_in": 3600, "token_type": "Bearer"})
		return
	}
	if r.URL.Path != "/rsv/v1/hotels/HQ1/reservations" {
		f.t.Errorf("unexpected path %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer ohip-token" || r.Header.Get("x-hotelid") != "HQ1" {
		f.t.Errorf("reservation call headers: auth=%q hotel=%q", r.Header.Get("Authorization"), r.Header.Get("x-hotelid"))
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	q := r.URL.Query()
	f.queries = append(f.queries, q)
	var list []map[string]any
	if ids := q["reservationIdList"]; len(ids) > 0 {
		for _, id := range ids {
			if rr, ok := f.byID[id]; ok {
				list = append(list, rr)
			}
		}
	} else {
		list = f.inHouse
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	end := offset + limit
	if end > len(list) {
		end = len(list)
	}
	page := []map[string]any{}
	if offset < len(list) {
		page = list[offset:end]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"reservations": map[string]any{
		"reservationInfo": page, "hasMore": end < len(list), "totalResults": len(list),
	}})
}

func newOpera(t *testing.T) (*operaFake, Client) {
	f := &operaFake{t: t, inHouse: []map[string]any{
		operaRes("1001", "C1001", "InHouse", "0101", "Grace", "Hopper", "2026-09-22", "2026-09-26", "2026-09-22 14:03:11.0"),
		operaRes("1002", "C1002", "DueOut", "0102", "Alan", "Turing", "2026-09-20", "2026-09-24", "2026-09-24 07:15:00.0"),
		operaRes("1003", "C1003", "InHouse", "0103", "Ada", "Lovelace", "2026-09-23", "2026-09-28", "2026-09-23 18:40:00.0"),
	}, byID: map[string]map[string]any{
		"1004": operaRes("1004", "C1004", "CheckedOut", "0104", "Edsger", "Dijkstra", "2026-09-19", "2026-09-24", "2026-09-24 10:00:00.0"),
	}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := New("opera-cloud", map[string]any{"gateway_url": srv.URL, "hotel_id": "HQ1",
		"scope": "urn:opc:hgbu:ws:__myscopes__", "enterprise_id": "ENT1"},
		[]byte(`{"client_id":"ohip-client","client_secret":"ohip-secret","app_key":"41ecd082-8997-4c69-af34-2f72b83645ff"}`),
		testOptions(t, "America/New_York", &sleepRecorder{}))
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestOperaCloudContract_SnapshotPagesWithHasMore(t *testing.T) {
	withPageSize(t, &operaPage, 2)
	f, c := newOpera(t)
	snap, err := c.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Reservations) != 3 || snap.RoomsKnown {
		t.Fatalf("snapshot %+v (OPERA's reservation API cannot enumerate rooms)", snap)
	}
	got := []string{}
	for _, r := range snap.Reservations {
		got = append(got, r.ID+"/"+r.Room+"/"+r.LastName+"/"+r.Arrival+"/"+r.Departure+"/"+r.State.String())
	}
	want := []string{"1001/0101/Hopper/260922/260926/IN_HOUSE", "1002/0102/Turing/260920/260924/IN_HOUSE",
		"1003/0103/Lovelace/260923/260928/IN_HOUSE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if len(f.queries) != 2 || f.queries[0].Get("searchType") != "InHouse" || f.queries[1].Get("offset") != "2" {
		t.Fatalf("paging queries %v", f.queries)
	}
	if f.tokens != 1 {
		t.Fatalf("token fetched %d times", f.tokens)
	}
	if c.SupportsChanges() {
		t.Fatal("getHotelReservations documents no modified-since filter; the connector must compare lists")
	}
}

func TestOperaCloudContract_LookupUsesRepeatedReservationIdList(t *testing.T) {
	f, c := newOpera(t)
	got, err := c.Lookup(t.Context(), []string{"1004", "9999"})
	if err != nil || len(got) != 1 || got[0].State != StateCheckedOut || got[0].Room != "0104" {
		t.Fatalf("%+v %v", got, err)
	}
	if ids := f.queries[0]["reservationIdList"]; !reflect.DeepEqual(ids, []string{"1004", "9999"}) {
		t.Fatalf("reservationIdList %v (collectionFormat multi)", ids)
	}
}

func TestOperaCloudContract_ErrorsClassified(t *testing.T) {
	for status, kind := range map[int]ErrKind{401: KindAuth, 403: KindAuth, 404: KindRequest, 502: KindUnavailable} {
		f, c := newOpera(t)
		f.status = status
		if _, err := c.Probe(t.Context()); KindOf(err) != kind {
			t.Errorf("HTTP %d -> %v, want %s", status, err, kind)
		}
	}
}

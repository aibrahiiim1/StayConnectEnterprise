package pms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMews stands in for api.mews.com. It routes three endpoints:
//   /spaces/getAll        → configurable space list
//   /reservations/getAll  → configurable reservation list
//   /customers/getAll     → responds with every customer the caller asks for
//
// It also verifies that every request body carries the expected auth
// fields so contract drift shows up in tests, not in prod.
type fakeMews struct {
	mu            sync.Mutex
	spaces        []fakeSpace
	reservations  map[string][]fakeRes     // keyed by spaceID
	customers     map[string]fakeCustomer  // keyed by customerID
	lastSpacesReq map[string]any
	srv           *httptest.Server
}

type fakeSpace struct {
	ID     string `json:"Id"`
	Number string `json:"Number"`
	Name   string `json:"Name"`
}
type fakeRes struct {
	ID          string    `json:"Id"`
	Number      string    `json:"Number"`
	CustomerId  string    `json:"CustomerId"`
	CustomerIds []string  `json:"CustomerIds"`
	StartUtc    time.Time `json:"StartUtc"`
	EndUtc      time.Time `json:"EndUtc"`
}
type fakeCustomer struct {
	ID        string `json:"Id"`
	FirstName string `json:"FirstName"`
	LastName  string `json:"LastName"`
	Email     string `json:"Email"`
}

func newFakeMews(t *testing.T) *fakeMews {
	t.Helper()
	f := &fakeMews{
		reservations: map[string][]fakeRes{},
		customers:    map[string]fakeCustomer{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method != http.MethodPost {
			t.Errorf("wrong method %s", r.Method)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Every request must include the three auth fields.
		for _, k := range []string{"ClientToken", "AccessToken", "Client"} {
			if _, ok := body[k]; !ok {
				t.Errorf("missing %s in request to %s", k, r.URL.Path)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/connector/v1/spaces/getAll":
			f.lastSpacesReq = body
			_ = json.NewEncoder(w).Encode(map[string]any{"Spaces": f.spaces})
		case "/api/connector/v1/reservations/getAll":
			ids, _ := body["AssignedResourceIds"].([]any)
			var out []fakeRes
			for _, idRaw := range ids {
				if id, ok := idRaw.(string); ok {
					out = append(out, f.reservations[id]...)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Reservations": out})
		case "/api/connector/v1/customers/getAll":
			ids, _ := body["CustomerIds"].([]any)
			var out []fakeCustomer
			for _, idRaw := range ids {
				if id, ok := idRaw.(string); ok {
					if c, hit := f.customers[id]; hit {
						out = append(out, c)
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Customers": out})
		case "/api/connector/v1/enterprises/getAll":
			_ = json.NewEncoder(w).Encode(map[string]any{"Enterprises": []any{map[string]any{"Id": "ent-1"}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return f
}

func (f *fakeMews) Close() { f.srv.Close() }

func newMewsAt(t *testing.T, baseURL string) *Mews {
	t.Helper()
	m := NewMews("mews-test")
	cfg := ProviderConfig{
		Name: "mews-test", Kind: "mews",
		Connection: ConnectionConfig{
			BaseURL:    baseURL,
			APIKey:     "access-token-1",
			PropertyID: "enterprise-1",
			Extra:      map[string]any{"client_token": "client-token-1", "client_name": "test"},
		},
	}
	if err := m.Configure(cfg); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return m
}

func TestMewsConfigureRequiresTokens(t *testing.T) {
	m := NewMews("x")
	err := m.Configure(ProviderConfig{
		Connection: ConnectionConfig{BaseURL: "https://x", APIKey: "a"}, // no client_token
	})
	if err == nil {
		t.Error("missing client_token should fail Configure")
	}
}

func TestMewsRefreshSpacesBuildsMap(t *testing.T) {
	f := newFakeMews(t); defer f.Close()
	f.mu.Lock()
	f.spaces = []fakeSpace{{ID: "space-101", Number: "101"}, {ID: "space-102", Number: "102"}}
	f.mu.Unlock()

	m := newMewsAt(t, f.srv.URL)
	if err := m.refreshSpaces(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.rooms["101"] != "space-101" || m.rooms["102"] != "space-102" {
		t.Errorf("room map wrong: %+v", m.rooms)
	}
	// Outgoing request carries EnterpriseIds = [enterprise-1].
	ids, _ := f.lastSpacesReq["EnterpriseIds"].([]any)
	if len(ids) != 1 || ids[0].(string) != "enterprise-1" {
		t.Errorf("EnterpriseIds wrong: %v", ids)
	}
}

func TestMewsValidateGuestSuccess(t *testing.T) {
	f := newFakeMews(t); defer f.Close()
	f.mu.Lock()
	f.spaces = []fakeSpace{{ID: "space-101", Number: "101"}}
	f.reservations["space-101"] = []fakeRes{{
		ID: "res-a", Number: "R-1",
		CustomerIds: []string{"cust-a"},
		StartUtc:    time.Now().Add(-1 * time.Hour),
		EndUtc:      time.Now().Add(24 * time.Hour),
	}}
	f.customers["cust-a"] = fakeCustomer{ID: "cust-a", FirstName: "Alice", LastName: "Anderson", Email: "a@example.com"}
	f.mu.Unlock()

	m := newMewsAt(t, f.srv.URL)
	_ = m.refreshSpaces(context.Background())

	res, err := m.ValidateGuest(context.Background(), Query{
		RoomNumber: "101", LastName: "anderson", Mode: ModeRoomLastName,
	})
	if err != nil {
		t.Fatalf("ValidateGuest: %v", err)
	}
	if res.FirstName != "Alice" || res.LastName != "Anderson" || res.Email != "a@example.com" {
		t.Errorf("guest fields wrong: %+v", res)
	}
	if res.ReservationID != "R-1" {
		t.Errorf("reservation number = %q, want R-1", res.ReservationID)
	}
}

func TestMewsValidateGuestNoMatch(t *testing.T) {
	f := newFakeMews(t); defer f.Close()
	f.mu.Lock()
	f.spaces = []fakeSpace{{ID: "space-101", Number: "101"}}
	f.reservations["space-101"] = []fakeRes{{
		ID: "res-a", Number: "R-1",
		CustomerIds: []string{"cust-a"},
		StartUtc:    time.Now().Add(-1 * time.Hour),
		EndUtc:      time.Now().Add(24 * time.Hour),
	}}
	f.customers["cust-a"] = fakeCustomer{ID: "cust-a", FirstName: "Alice", LastName: "Anderson"}
	f.mu.Unlock()

	m := newMewsAt(t, f.srv.URL)
	_ = m.refreshSpaces(context.Background())

	_, err := m.ValidateGuest(context.Background(), Query{
		RoomNumber: "101", LastName: "Wrong", Mode: ModeRoomLastName,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestMewsValidateGuestRefetchesOnMiss(t *testing.T) {
	f := newFakeMews(t); defer f.Close()
	// Start with empty spaces — first ValidateGuest call will miss the map.
	f.mu.Lock()
	f.spaces = nil
	f.mu.Unlock()

	m := newMewsAt(t, f.srv.URL)
	// Intentionally skip the initial refreshSpaces — ValidateGuest must do it.

	// Now the room gets added upstream.
	f.mu.Lock()
	f.spaces = []fakeSpace{{ID: "space-9", Number: "9"}}
	f.reservations["space-9"] = []fakeRes{{
		ID: "res-b", Number: "R-9", CustomerIds: []string{"cust-b"},
		StartUtc: time.Now().Add(-time.Hour), EndUtc: time.Now().Add(time.Hour),
	}}
	f.customers["cust-b"] = fakeCustomer{ID: "cust-b", FirstName: "Bob", LastName: "Builder"}
	f.mu.Unlock()

	res, err := m.ValidateGuest(context.Background(), Query{
		RoomNumber: "9", LastName: "builder", Mode: ModeRoomLastName,
	})
	if err != nil {
		t.Fatalf("ValidateGuest: %v", err)
	}
	if res.FirstName != "Bob" {
		t.Errorf("refetch didn't pick up newly-added room: %+v", res)
	}
}

func TestMewsUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"Message":"Invalid AccessToken."}`))
	}))
	defer srv.Close()

	m := newMewsAt(t, srv.URL)
	err := m.refreshSpaces(context.Background())
	if err == nil {
		t.Fatal("expected error from 401")
	}
	if !strings.Contains(err.Error(), "Invalid AccessToken") {
		t.Errorf("err didn't include upstream message: %v", err)
	}
}

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

// fakeApaleo routes three endpoints on a single httptest server:
//   /connect/token          → OAuth client-credentials grant
//   /inventory/v1/units     → paged units list
//   /booking/v1/reservations → reservation lookup (expects bearer auth)
//
// It also counts how many token grants were issued so tests can assert
// the cache-until-expiry behaviour.
type fakeApaleo struct {
	mu          sync.Mutex
	units       []apaleoUnit
	reservsByID map[string][]apaleoReservation
	tokenIssued int
	tokenTTL    int // seconds; 0 → 3600
	srv         *httptest.Server
}

func newFakeApaleo(t *testing.T) *fakeApaleo {
	t.Helper()
	f := &fakeApaleo{
		reservsByID: map[string][]apaleoReservation{},
		tokenTTL:    3600,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/connect/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token: wrong method %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("token: wrong content-type %q", r.Header.Get("Content-Type"))
		}
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "client_credentials" {
			t.Errorf("token: wrong grant_type")
		}
		if r.PostForm.Get("client_id") == "" || r.PostForm.Get("client_secret") == "" {
			t.Errorf("token: missing credentials")
		}
		f.mu.Lock()
		f.tokenIssued++
		ttl := f.tokenTTL
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apaleoTokenResp{
			AccessToken: "tok-abc", ExpiresIn: ttl, TokenType: "Bearer",
		})
	})
	requireBearer := func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/inventory/v1/units", func(w http.ResponseWriter, r *http.Request) {
		if !requireBearer(w, r) {
			return
		}
		if r.URL.Query().Get("propertyId") == "" {
			t.Errorf("units: missing propertyId")
		}
		f.mu.Lock()
		units := append([]apaleoUnit(nil), f.units...)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apaleoUnitsResp{Units: units, Count: len(units)})
	})
	mux.HandleFunc("/booking/v1/reservations", func(w http.ResponseWriter, r *http.Request) {
		if !requireBearer(w, r) {
			return
		}
		if r.URL.Query().Get("expand") != "primaryGuest" {
			t.Errorf("reservations: missing expand=primaryGuest")
		}
		id := r.URL.Query().Get("unitIds")
		f.mu.Lock()
		out := append([]apaleoReservation(nil), f.reservsByID[id]...)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apaleoReservationsResp{Reservations: out, Count: len(out)})
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeApaleo) Close() { f.srv.Close() }

func newApaleoAt(t *testing.T, base string) *Apaleo {
	t.Helper()
	a := NewApaleo("apaleo-test")
	cfg := ProviderConfig{
		Name: "apaleo-test", Kind: "apaleo",
		Connection: ConnectionConfig{
			BaseURL:    base,
			APIKey:     "secret-xyz",
			PropertyID: "HTL",
			Extra: map[string]any{
				"client_id":    "cli-1",
				"identity_url": base, // route /connect/token through the same fake
			},
		},
	}
	if err := a.Configure(cfg); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return a
}

func TestApaleoConfigureRequiresCreds(t *testing.T) {
	a := NewApaleo("x")
	err := a.Configure(ProviderConfig{Connection: ConnectionConfig{
		BaseURL: "https://x", APIKey: "secret", PropertyID: "HTL",
		// no client_id
	}})
	if err == nil {
		t.Error("missing client_id should fail Configure")
	}
}

func TestApaleoTokenCachedUntilExpiry(t *testing.T) {
	f := newFakeApaleo(t); defer f.Close()
	f.mu.Lock()
	f.units = []apaleoUnit{{ID: "u-1", Name: "101"}}
	f.mu.Unlock()

	a := newApaleoAt(t, f.srv.URL)

	// First refresh triggers a token grant + a units call.
	if err := a.refreshUnits(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Second refresh within TTL should reuse the cached token.
	if err := a.refreshUnits(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := f.tokenIssued
	f.mu.Unlock()
	if got != 1 {
		t.Errorf("expected 1 token grant, got %d", got)
	}
}

func TestApaleoTokenRefreshedWhenExpired(t *testing.T) {
	f := newFakeApaleo(t); defer f.Close()
	// 1-second token TTL (minus apaleoTokenSkew=60s makes expiry "in the
	// past" immediately, so every call re-fetches).
	f.mu.Lock()
	f.tokenTTL = 1
	f.units = []apaleoUnit{{ID: "u-1", Name: "101"}}
	f.mu.Unlock()

	a := newApaleoAt(t, f.srv.URL)
	_ = a.refreshUnits(context.Background())
	_ = a.refreshUnits(context.Background())
	f.mu.Lock()
	got := f.tokenIssued
	f.mu.Unlock()
	if got < 2 {
		t.Errorf("expected >=2 token grants when expiry passed, got %d", got)
	}
}

func TestApaleoValidateGuestSuccess(t *testing.T) {
	f := newFakeApaleo(t); defer f.Close()
	f.mu.Lock()
	f.units = []apaleoUnit{{ID: "u-101", Name: "101"}}
	f.reservsByID["u-101"] = []apaleoReservation{{
		ID: "res-1", BookingID: "BK-42",
		Arrival:   time.Now().Add(-time.Hour),
		Departure: time.Now().Add(24 * time.Hour),
		Status:    "InHouse",
		PrimaryGuest: apaleoPrimaryGuest{
			FirstName: "Alice", LastName: "Anderson", Email: "a@example.com",
		},
	}}
	f.mu.Unlock()

	a := newApaleoAt(t, f.srv.URL)
	_ = a.refreshUnits(context.Background())

	res, err := a.ValidateGuest(context.Background(), Query{
		RoomNumber: "101", LastName: "ANDERSON", Mode: ModeRoomLastName,
	})
	if err != nil {
		t.Fatalf("ValidateGuest: %v", err)
	}
	if res.FirstName != "Alice" || res.LastName != "Anderson" || res.Email != "a@example.com" {
		t.Errorf("guest fields wrong: %+v", res)
	}
	if res.ReservationID != "BK-42" {
		t.Errorf("reservation id mismatch — want BK-42 (bookingId), got %q", res.ReservationID)
	}
}

func TestApaleoValidateGuestNoMatch(t *testing.T) {
	f := newFakeApaleo(t); defer f.Close()
	f.mu.Lock()
	f.units = []apaleoUnit{{ID: "u-101", Name: "101"}}
	f.reservsByID["u-101"] = []apaleoReservation{{
		ID: "r", BookingID: "BK-1",
		PrimaryGuest: apaleoPrimaryGuest{FirstName: "Alice", LastName: "Anderson"},
		Arrival:      time.Now().Add(-time.Hour),
		Departure:    time.Now().Add(time.Hour),
	}}
	f.mu.Unlock()

	a := newApaleoAt(t, f.srv.URL)
	_ = a.refreshUnits(context.Background())

	_, err := a.ValidateGuest(context.Background(), Query{
		RoomNumber: "101", LastName: "Wrong", Mode: ModeRoomLastName,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestApaleoTokenError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/connect/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"unknown client"}`))
	})
	srv := httptest.NewServer(mux); defer srv.Close()

	a := newApaleoAt(t, srv.URL)
	_, err := a.getToken(context.Background())
	if err == nil {
		t.Fatal("expected token error")
	}
	if !strings.Contains(err.Error(), "invalid_client") || !strings.Contains(err.Error(), "unknown client") {
		t.Errorf("err didn't include OAuth2 detail: %v", err)
	}
}

func TestApaleoDropsTokenOn401(t *testing.T) {
	f := newFakeApaleo(t); defer f.Close()
	f.mu.Lock()
	f.units = []apaleoUnit{{ID: "u-1", Name: "101"}}
	f.mu.Unlock()

	a := newApaleoAt(t, f.srv.URL)
	_ = a.refreshUnits(context.Background())

	// Simulate credential rotation: the fake keeps issuing tokens but
	// the "API" now rejects them. We swap in a handler that always 401s
	// by stopping the existing server and standing a new one up isn't
	// worth it — instead grab the token directly and check the cache
	// drop by invoking doGET against a handler that forces 401.
	rejectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer rejectSrv.Close()
	a.apiBase = rejectSrv.URL

	// Pre-seed a cached token.
	a.mu.Lock()
	a.token = "stale"
	a.tokenExpiry = time.Now().Add(1 * time.Hour)
	a.mu.Unlock()

	var out apaleoUnitsResp
	err := a.doGET(context.Background(), "/inventory/v1/units?propertyId=HTL", &out)
	if err == nil {
		t.Fatal("expected 401 error")
	}
	a.mu.RLock()
	cached := a.token
	a.mu.RUnlock()
	if cached != "" {
		t.Errorf("token cache should be cleared on 401, still holds %q", cached)
	}
}

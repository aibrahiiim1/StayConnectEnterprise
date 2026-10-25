package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A network name, domain or reservation hostname that would reach the generated firewall, DHCP or DNS files unsafely is refused at the API, before any
// database work: the server below has no database, so any attempt to use one would panic rather than pass.

func postJSON(h http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestCreateNetworkRefusesHostileText(t *testing.T) {
	s := &server{}
	cases := []struct{ name, body, code string }{
		{"quote and statement in the name", `{"name":"Lobby\"; accept comment \"x"}`, "invalid_name"},
		{"newline in the name", `{"name":"a\nadd rule inet stayconnect forward accept"}`, "invalid_name"},
		{"no name", `{"name":""}`, "invalid_name"},
		{"name too long", `{"name":"` + strings.Repeat("a", 65) + `"}`, "invalid_name"},
		{"bad domain", `{"name":"Guests","domain_name":"guest.local\"}"}`, "invalid_domain"},
	}
	for _, tc := range cases {
		rec := postJSON(s.createGuestNetwork, http.MethodPost, "/guest-networks", tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.code) {
			t.Errorf("%s: status %d body %s, want 400 %s", tc.name, rec.Code, rec.Body.String(), tc.code)
		}
	}
}

func TestBatchCreateRefusesHostileTextInAnyRow(t *testing.T) {
	s := &server{}
	rec := postJSON(s.createGuestNetworkBatch, http.MethodPost, "/guest-networks/batch",
		`{"networks":[{"name":"Fine","network_type":"untagged"},{"name":"Bad\"name","network_type":"untagged"}]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_name") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateNetworkRefusesHostileText(t *testing.T) {
	s := &server{}
	r := chi.NewRouter()
	r.Put("/guest-networks/{id}", s.updateGuestNetwork)
	for _, body := range []string{`{"name":"x\"y"}`, `{"domain_name":"bad domain"}`} {
		req := httptest.NewRequest(http.MethodPut, "/guest-networks/00000000-0000-0000-0000-000000000001", strings.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d body %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestReservationHostnameIsValidated(t *testing.T) {
	s := &server{}
	rec := postJSON(s.createReservation, http.MethodPost, "/dhcp/reservations",
		`{"guest_network_id":"00000000-0000-0000-0000-000000000001","mac":"aa:bb:cc:dd:ee:ff","reserved_ip":"10.0.0.5","hostname":"pc one\"; x"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_hostname") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

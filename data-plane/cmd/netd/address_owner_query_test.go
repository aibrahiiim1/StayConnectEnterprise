package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixedLeases []KeaLease

func (f fixedLeases) Leases() ([]KeaLease, error) { return f, nil }

// The owner query answers from the same DHCP evidence the enforcement pass uses; it never says "owned" for a reassigned address.
func TestAddressOwnerQueryAnswersFromTheLeaseTable(t *testing.T) {
	now := time.Now()
	leases := fixedLeases{
		{IPAddress: "10.3.0.10", HWAddr: "02:00:00:00:00:0a", CLTT: now.Add(-time.Hour).Unix(), ValidLft: 28800},
		{IPAddress: "10.3.0.11", HWAddr: "02:00:00:00:00:0b", CLTT: now.Add(-9 * time.Hour).Unix(), ValidLft: 28800}, // lapsed
	}
	srv := &server{phase3: &phase3Shaping{owner: addressOwner{src: leases}}}
	ask := func(ip, mac string) (int, string) {
		rec := httptest.NewRecorder()
		srv.addressOwnerQuery(rec, httptest.NewRequest(http.MethodPost, "/v1/address/owner",
			strings.NewReader(`{"ip":"`+ip+`","mac":"`+mac+`"}`)))
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out["state"]
	}
	for _, c := range []struct{ ip, mac, want string }{
		{"10.3.0.10", "02:00:00:00:00:0a", "owned"},
		{"10.3.0.10", "02:00:00:00:00:0c", "foreign"}, // the address is leased to another device
		{"10.3.0.11", "02:00:00:00:00:0b", "foreign"}, // its own lease lapsed
	} {
		if code, got := ask(c.ip, c.mac); code != http.StatusOK || got != c.want {
			t.Errorf("%s %s: %d %q, want %q", c.ip, c.mac, code, got, c.want)
		}
	}
	if code, _ := ask("not-an-ip", "02:00:00:00:00:0a"); code != http.StatusBadRequest {
		t.Errorf("a malformed address answered %d", code)
	}
	// A pass swaps the owner for the lease list read when it began; a device that took its lease since (and probes at once) is
	// owned by Kea's own table, while an address Kea gives to someone else stays foreign (test appliance, 2026-11-06 16:10:41).
	fresh := append(fixedLeases{{IPAddress: "10.3.0.12", HWAddr: "02:00:00:00:00:0d", CLTT: now.Unix(), ValidLft: 28800}}, leases...)
	srv.phase3.liveOwner = addressOwner{src: fresh}
	srv.phase3.owner = addressOwner{src: &passLeases{src: leases}}
	for _, c := range []struct{ ip, mac, want string }{
		{"10.3.0.12", "02:00:00:00:00:0d", "owned"},
		{"10.3.0.12", "02:00:00:00:00:0e", "foreign"},
		{"10.3.0.10", "02:00:00:00:00:0c", "foreign"},
	} {
		if code, got := ask(c.ip, c.mac); code != http.StatusOK || got != c.want {
			t.Errorf("during a pass, %s %s: %d %q, want %q", c.ip, c.mac, code, got, c.want)
		}
	}
	// Without the enforcement pass nothing can be vouched for.
	rec := httptest.NewRecorder()
	(&server{}).addressOwnerQuery(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ip":"10.3.0.10","mac":"02:00:00:00:00:0a"}`)))
	if !strings.Contains(rec.Body.String(), `"unknown"`) {
		t.Errorf("no evidence source answered %s", rec.Body.String())
	}
}

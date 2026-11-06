package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTheDiagnosticsFileLeavesOutGuestsAddressesAndSecrets(t *testing.T) {
	at := time.Date(2026, 11, 6, 9, 30, 5, 0, time.UTC)
	body, err := diagnosticsBundle(diagnosticsParts{
		At: at, Version: "1.4.0", SiteID: "site-1",
		Licence:  map[string]any{"state": "ACTIVE"},
		Services: map[string]any{"overall": "degraded", "services": []any{map[string]any{"service": "netd", "health_detail": "lease for 10.20.30.40 on aa:bb:cc:dd:ee:ff"}}},
		Recovery: []recoveryEvent{{Service: "portald", Event: "manual_restart", Cause: "guest in room 1203 could not sign in", Actor: "operator-77", CreatedAt: at}},
		Monitor:  map[string]any{"available": true},
		Logs:     map[string][]string{"pmsd": {`2026-11-06 |RNroom1203|GNSmith posted`, `login password=hunter2 user=jane email jane@example.com`}},
		Errors:   []string{`request failed request_id=abc123 guest_name="Jane Doe"`},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, leak := range []string{"10.20.30.40", "aa:bb:cc", "1203", "Smith", "hunter2", "jane@example.com", "Jane Doe", "operator-77"} {
		if strings.Contains(text, leak) {
			t.Fatalf("the diagnostics file kept %q:\n%s", leak, text)
		}
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out["format"] != bundleFormat {
		t.Fatalf("format = %v", out["format"])
	}
	app := out["appliance"].(map[string]any)
	if app["version"] != "1.4.0" || app["site_id"] != "site-1" || app["licence"].(map[string]any)["state"] != "ACTIVE" {
		t.Fatalf("appliance = %v", app)
	}
	if !strings.Contains(text, "abc123") {
		t.Fatalf("the error reference must stay so support can find it:\n%s", text)
	}
	if ev := out["recovery_events"].([]any)[0].(map[string]any); ev["service"] != "portald" || ev["actor"] != nil {
		t.Fatalf("recovery event = %v", ev)
	}
}

func TestTheDiagnosticsFileIsCompleteWhenPartsAreMissing(t *testing.T) {
	body, err := diagnosticsBundle(diagnosticsParts{At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["logs"].(map[string]any); !ok {
		t.Fatalf("logs must be an object: %v", out["logs"])
	}
	if errs, ok := out["recent_errors"].([]any); !ok || len(errs) != 0 {
		t.Fatalf("recent_errors must be an empty list: %v", out["recent_errors"])
	}
	if ev, ok := out["recovery_events"].([]any); !ok || len(ev) != 0 {
		t.Fatalf("recovery_events must be an empty list: %v", out["recovery_events"])
	}
}

func TestTheDiagnosticsFileNeedsTheRoleThatReadsLogs(t *testing.T) {
	h := (&server{}).healthRoutes()
	for _, role := range []string{"front_office_operator", "guest_relations_operator", "voucher_operator",
		"payments_operator", "site_viewer"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withSession(httptest.NewRequest("GET", "/bundle", nil), role))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s downloaded the diagnostics file: %d", role, w.Code)
		}
	}
}

func TestOnlyTheLatestRequestFailuresAreKept(t *testing.T) {
	lines := []string{"started", "request failed a", "ok", "request failed b", "request failed c"}
	got := recentRequestFailures(lines, 2)
	if len(got) != 2 || got[0] != "request failed b" || got[1] != "request failed c" {
		t.Fatalf("got %v", got)
	}
	if got := recentRequestFailures([]string{"fine"}, 5); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestTheDiagnosticsFileNameCarriesTheTimeInUTC(t *testing.T) {
	at := time.Date(2026, 11, 6, 9, 30, 5, 0, time.FixedZone("EET", 2*3600))
	if got := bundleFileName(at); got != "onegate-diagnostics-20261106-073005Z.json" {
		t.Fatalf("got %s", got)
	}
}

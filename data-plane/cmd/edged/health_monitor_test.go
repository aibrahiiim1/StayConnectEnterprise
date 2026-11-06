package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func monitorAnswer(t *testing.T, body string, write bool) map[string]any {
	t.Helper()
	old := monitorSnapshotPath
	monitorSnapshotPath = filepath.Join(t.TempDir(), "health.json")
	t.Cleanup(func() { monitorSnapshotPath = old })
	if write {
		if err := os.WriteFile(monitorSnapshotPath, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	(&server{}).healthMonitor(rec, httptest.NewRequest(http.MethodGet, "/diagnostics/monitor", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTheMonitorSnapshotIsServedRedactedWithItsAge(t *testing.T) {
	at := time.Now().UTC().Add(-2 * time.Minute).Format("2006-01-02T15:04:05.000000+00:00")
	out := monitorAnswer(t, `{"version":1,"at":"`+at+`","overall":"WARN","checks":[{"key":"k","text":"seen at 10.3.4.5 by aa:bb:cc:dd:ee:ff, room 1203"}]}`, true)
	if out["available"] != true || out["stale"] != false {
		t.Fatalf("a current snapshot: %v", out)
	}
	if age, _ := out["age_seconds"].(float64); age < 100 || age > 200 {
		t.Fatalf("age_seconds = %v, want about 120", out["age_seconds"])
	}
	text := out["snapshot"].(map[string]any)["checks"].([]any)[0].(map[string]any)["text"].(string)
	for _, leak := range []string{"10.3.4.5", "aa:bb:cc", "1203"} {
		if strings.Contains(text, leak) {
			t.Fatalf("the console text kept %q: %s", leak, text)
		}
	}
}

func TestAnOldMissingOrBrokenSnapshotIsNeverCurrentEvidence(t *testing.T) {
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if out := monitorAnswer(t, `{"at":"`+old+`"}`, true); out["available"] != true || out["stale"] != true {
		t.Fatalf("an hour-old snapshot must be served as stale: %v", out)
	}
	if out := monitorAnswer(t, "", false); out["available"] != false || out["reason"] != "not_installed" {
		t.Fatalf("no snapshot: %v", out)
	}
	for _, body := range []string{"{not json", `{"overall":"PASS"}`, `{"at":"yesterday"}`} {
		if out := monitorAnswer(t, body, true); out["available"] != false || out["reason"] != "invalid" {
			t.Fatalf("%q: %v", body, out)
		}
	}
}

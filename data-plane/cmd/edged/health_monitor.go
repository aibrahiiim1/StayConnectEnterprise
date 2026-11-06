package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"time"
)

// monitorSnapshotPath is written by the appliance monitor (deploy/monitor) on every run, root-owned and readable by this service's group.
var monitorSnapshotPath = "/var/lib/onegate-monitor-console/health.json"

const (
	monitorSnapshotMax = 4 << 20
	// Quick runs are every five minutes; three missed runs is no longer current evidence.
	monitorStaleAfter = 15 * time.Minute
)

// healthMonitor serves the monitor's last snapshot as it is, never a substitute: missing, unreadable or old evidence is said to be so.
func (s *server) healthMonitor(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"available": false, "generated_at": time.Now().UTC()}
	st, err := os.Stat(monitorSnapshotPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		out["reason"] = "not_installed"
	case err != nil:
		out["reason"] = "unreadable"
	case st.Size() > monitorSnapshotMax:
		out["reason"] = "invalid"
	default:
		b, rerr := os.ReadFile(monitorSnapshotPath)
		var snap map[string]any
		var at time.Time
		if rerr != nil {
			out["reason"] = "unreadable"
		} else if json.Unmarshal(b, &snap) != nil {
			out["reason"] = "invalid"
		} else if a, _ := snap["at"].(string); a == "" {
			out["reason"] = "invalid"
		} else if at, err = time.Parse(time.RFC3339Nano, a); err != nil {
			out["reason"] = "invalid"
		} else {
			age := time.Since(at)
			out["available"] = true
			out["snapshot"] = redactMonitor(snap)
			out["age_seconds"] = int64(age / time.Second)
			out["stale"] = age > monitorStaleAfter
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// redactMonitor applies the log redaction to every text: the monitor already leaves addresses out and masks hardware addresses.
func redactMonitor(v any) any {
	switch t := v.(type) {
	case string:
		return sanitizeLog(t)
	case []any:
		for i := range t {
			t[i] = redactMonitor(t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = redactMonitor(t[k])
		}
	}
	return v
}

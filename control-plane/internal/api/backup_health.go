package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// BackupHealthHandler exposes the Central host's backup/rollback retention
// health, as written by the stayconnect-backup-cleanup tool
// (/opt/stayconnect/backup-retention-status.json). Read-only, platform view.
// It reports last cleanup, retained/pinned/protected/delete-candidate artifacts,
// disk usage + alert, cleanup failures, and whether a valid rollback path exists.
func (b *Base) BackupHealthHandler(w http.ResponseWriter, r *http.Request) {
	const statusPath = "/opt/stayconnect/backup-retention-status.json"
	raw, err := os.ReadFile(statusPath)
	if err != nil {
		WriteJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"message":   "backup cleanup has not run yet on this host",
		})
		return
	}
	var status map[string]any
	if err := json.Unmarshal(raw, &status); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "status parse failed")
		return
	}
	// Operator-pinned artifacts (paths that cleanup must always keep).
	pins := []string{}
	if pf, err := os.ReadFile("/etc/stayconnect/backup-retention.pins"); err == nil {
		for _, ln := range strings.Split(string(pf), "\n") {
			ln = strings.TrimSpace(ln)
			if ln != "" && !strings.HasPrefix(ln, "#") {
				pins = append(pins, ln)
			}
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"status":    status,
		"pins":      pins,
	})
}

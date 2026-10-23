// Package livez is a filesystem heartbeat for loop-based daemons with no socket to probe (e.g. acctd): the loop
// calls Touch each iteration and the edged supervisor calls Age, since systemd-active does not mean the loop progresses.
package livez

import (
	"os"
	"path/filepath"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/healthdir"
)

const Dir = "/run/stayconnect/health"

func path(name string) string { return filepath.Join(Dir, name+".alive") }

// Touch stamps the liveness file. It writes a fresh file renamed into place (internal/healthdir), never
// Chtimes/OpenFile on the existing name: both follow symlinks and the directory is shared with every service account.
func Touch(name string) {
	_ = healthdir.WriteFile(Dir, name+".alive", nil)
}

// Report publishes a loop's degraded state beside its heartbeat (empty reason = healthy); Age alone cannot see a
// loop that ticks while every observation is refused.
func Report(name, reason string) {
	// Written whole every time, including the healthy case, so a stale reason never outlives recovery; never through a symlink.
	_ = healthdir.WriteFile(Dir, name+".status", []byte(reason))
}

// Status returns the last reported degraded reason ("" when healthy) and whether the loop has reported at all.
func Status(name string) (string, bool) {
	b, err := os.ReadFile(statusPath(name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func statusPath(name string) string { return filepath.Join(Dir, name+".status") }

func Age(name string) (time.Duration, bool) {
	fi, err := os.Stat(path(name))
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}

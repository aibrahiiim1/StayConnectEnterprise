// Package livez is a filesystem liveness heartbeat for loop-based daemons that
// have no socket to probe (notably acctd). The daemon calls Touch(name) each
// iteration of its work loop; the edged health supervisor calls Age(name) to
// verify the loop is actually PROGRESSING — an accounting daemon can be
// systemd-"active" while its loop is wedged, and process-running is not health.
package livez

import (
	"os"
	"path/filepath"
	"time"
)

// Dir is shared with the startupbackoff trackers (tmpfs; reset on reboot).
const Dir = "/run/stayconnect/health"

func path(name string) string { return filepath.Join(Dir, name+".alive") }

// Touch stamps the service's liveness file with the current time.
func Touch(name string) {
	_ = os.MkdirAll(Dir, 0o777)
	_ = os.Chmod(Dir, 0o777)
	p := path(name)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		// File may not exist yet — create it, then it exists for next Touch.
		if f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644); e == nil {
			_ = f.Close()
		}
	}
}

// Age returns how long ago the service last beat, and whether a heartbeat exists.
func Age(name string) (time.Duration, bool) {
	fi, err := os.Stat(path(name))
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}

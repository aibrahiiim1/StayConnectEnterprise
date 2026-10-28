package main

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/livez"
)

// reexecSelf replaces the process image (same PID, so the supervisor sees no crash) to re-read identity and assignment.
func reexecSelf() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		os.Exit(3)
	}
}

// waitForAssignment parks accounting until a signed assignment appears: an appliance with no customer must
// not attribute usage to anyone. assigned must be the VERIFIED check, never the raw file.
func waitForAssignment(ctx context.Context, assigned func() bool) {
	// Keep beating while paused so the health supervisor sees healthy-idle, not degraded.
	livez.Touch("acctd")
	beat := time.NewTicker(5 * time.Second)
	defer beat.Stop()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			livez.Touch("acctd")
		case <-t.C:
			if assigned() {
				slog.Info("acctd: a verified assignment arrived; re-executing to start accounting")
				reexecSelf()
			}
		}
	}
}

// watchBaseline is the version the watcher compares against. It is the version acctd resolved its scope from
// (audit P6 L13): reading the store again after resolving would adopt a change made in between as the
// baseline, and acctd would keep billing the previous customer. Only with no verified version (a development
// fallback) is the stored record read.
func watchBaseline(resolved int64, store *assignment.Store) int64 {
	if resolved > 0 {
		return resolved
	}
	if r, err := store.Load(); err == nil && r != nil {
		return r.Version
	}
	return 0
}

// watchAssignmentReexec re-execs on an assignment version change so usage is never billed to the previous customer.
func watchAssignmentReexec(ctx context.Context, store *assignment.Store, resolvedVersion int64) {
	baseline := watchBaseline(resolvedVersion, store)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r, err := store.Load()
			if err != nil || r == nil {
				continue
			}
			if r.Version != baseline {
				slog.Info("acctd: assignment changed; re-executing to adopt it",
					"from_version", baseline, "to_version", r.Version)
				reexecSelf()
			}
		}
	}
}

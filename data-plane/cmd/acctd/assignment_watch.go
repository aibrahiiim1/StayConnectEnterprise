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

// reexecSelf replaces the process image so acctd re-reads the appliance's
// identity + signed assignment. Same PID, so the supervisor sees a normal
// application transition rather than a crash.
func reexecSelf() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		os.Exit(3)
	}
}

// waitForAssignment parks an unassigned appliance's accounting daemon until a
// signed assignment appears, then re-execs into the normal path. An appliance
// with no customer must not attribute usage to anyone.
func waitForAssignment(ctx context.Context, store *assignment.Store) {
	// Beat the liveness heartbeat while paused: acctd IS alive and doing its job
	// (waiting for an assignment), so the health supervisor must see it as
	// healthy-idle, not degraded. Beat immediately, then on a short cadence.
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
			if ten, _, _, _ := store.Resolved(); ten != "" {
				slog.Info("acctd: assignment arrived; re-executing to start accounting", "tenant_id", ten)
				reexecSelf()
			}
		}
	}
}

// watchAssignmentReexec re-execs acctd when the assignment version changes, so a
// re-assigned appliance immediately bills the NEW customer instead of continuing
// to attribute usage to the previous one.
func watchAssignmentReexec(ctx context.Context, store *assignment.Store) {
	baseline := int64(0)
	if r, err := store.Load(); err == nil && r != nil {
		baseline = r.Version
	}
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

package main

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// watchAssignmentReexec re-execs edged when the locally persisted assignment
// version changes, so a new/changed tenant/site (written by scd's assignment
// agent) is adopted with no manual restart. Re-exec keeps the same PID, so the
// systemd supervisor treats it as a normal application transition, not a crash.
func watchAssignmentReexec(ctx context.Context, store *assignment.Store) {
	baseline := int64(0)
	if d, err := store.Load(); err == nil && d != nil {
		baseline = d.Version
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d, err := store.Load()
			if err != nil || d == nil {
				continue
			}
			if d.Version != baseline {
				slog.Info("assignment changed on disk; re-executing edged to adopt it",
					"from_version", baseline, "to_version", d.Version)
				exe, e := os.Executable()
				if e != nil {
					os.Exit(3)
				}
				if e := syscall.Exec(exe, os.Args, os.Environ()); e != nil {
					os.Exit(3)
				}
			}
		}
	}
}

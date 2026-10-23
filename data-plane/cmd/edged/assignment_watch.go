package main

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// watchAssignmentReexec re-execs edged (same PID, so systemd sees no crash) when the persisted assignment
// version changes.
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
				slog.Info("assignment changed on disk; re-executing edged to adopt it",
					"from_version", baseline, "to_version", r.Version)
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

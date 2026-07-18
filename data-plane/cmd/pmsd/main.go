// pmsd — dedicated read-only PMS connector daemon (Phase 3, ADR-0001).
//
// Owns each PMS Interface connection under a DB advisory single-owner lock, one independent supervised
// worker per Interface, persisting the three interface-level freshness axes to iam_v2.pms_interface_runtime.
// Reuses the accepted FIAS protocol layer (internal/pms); emits no financial Posting (PS) record.
//
// DARK by default: with STAYCONNECT_PHASE3_PMS_CONNECTOR (and its master) OFF, pmsd opens no database
// connection, constructs no repository, creates no worker, and opens no PMS socket, then exits cleanly.
// The systemd unit uses Restart=on-failure so a clean flags-OFF exit does not cause a restart storm.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := iamv2.LoadPMSConfigFromEnv(os.Getenv)
	if err != nil {
		// malformed / incoherent flag set -> fail closed at startup.
		log.Error("pmsd: config fail-closed", "err", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("PMSD_DB_URL")
	deps := pmsd.Deps{
		OpenRepo:  func(ctx context.Context) (pmsd.Repo, error) { return pmsd.NewPgRepo(ctx, dsn) },
		NewLocker: func(ctx context.Context) (pmsd.Locker, error) { return pmsd.NewPgLocker(ctx, dsn) },
		Dial:      pmsd.DialFIAS,
		Log:       log,
	}

	if err := pmsd.Run(ctx, cfg, deps); err != nil {
		log.Error("pmsd: exiting on error", "err", err.Error())
		os.Exit(1)
	}
	log.Info("pmsd: stopped cleanly")
}

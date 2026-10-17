package main

// GUEST-DATA RETENTION RUN (migration 0127; GDPR remediation plan C1; Product-Owner decisions D-C1, D-C1a, D-C3).
//
// Hourly, scd asks the database to anonymise the guest personal data that has outlived the site's retention
// settings (Hotel Admin -> Backups -> Data retention). All the deciding happens in the two definer functions --
// what is old enough, what is still live and must be left alone, what each column becomes -- so this loop only
// calls them in batches until a pass changes nothing, and logs what changed. Same shape as the sign-in-attempt
// purge beside it: the owning service runs its own bounded job on a ticker.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	dataRetentionEvery     = time.Hour
	dataRetentionBatch     = 500
	dataRetentionMaxPasses = 20 // per tick: a large backlog is worked off over several hours, never in one lock-heavy burst
)

func (s *server) dataRetentionLoop(ctx context.Context) {
	t := time.NewTicker(dataRetentionEvery)
	defer t.Stop()
	for {
		s.runDataRetention(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *server) runDataRetention(ctx context.Context) {
	if s.db == nil || s.tenID == "" || s.siteID == "" {
		return
	}
	for pass := 0; pass < dataRetentionMaxPasses; pass++ {
		changed := 0
		for _, fn := range []string{"iam_v2.run_data_retention", "public.run_data_retention_public"} {
			n, err := s.dataRetentionPass(ctx, fn)
			if err != nil {
				if ctx.Err() == nil {
					var pgErr *pgconn.PgError
					if errors.As(err, &pgErr) && pgErr.Code == "42883" {
						slog.Warn("data retention unavailable: this database predates migration 0127")
					} else {
						slog.Warn("data retention run failed", "fn", fn, "err", err)
					}
				}
				return
			}
			changed += n
		}
		if changed == 0 {
			return
		}
	}
}

// dataRetentionPass runs one batch of fn in its own transaction and logs each category it changed.
func (s *server) dataRetentionPass(ctx context.Context, fn string) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rows, err := s.db.Query(cctx, `SELECT o_category, o_cutoff, o_anonymised FROM `+fn+`($1::uuid,$2::uuid,$3)`,
		s.tenID, s.siteID, dataRetentionBatch)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var category string
		var cutoff time.Time
		var n int
		if err := rows.Scan(&category, &cutoff, &n); err != nil {
			return total, err
		}
		total += n
		slog.Info("guest data anonymised by retention", "category", category, "cutoff", cutoff, "records", n)
	}
	return total, rows.Err()
}

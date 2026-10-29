package main

// Hourly, scd asks the database to anonymise guest personal data that has outlived the site's retention settings. The definer
// functions decide what is old enough and what is live; this loop only calls them in batches until a pass changes nothing.

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
	_, _ = s.retentionPasses(ctx, dataRetentionMaxPasses)
	s.runStaffRetention(ctx)
	s.runLeftovers(ctx)
	s.pruneIdleDevices(ctx)
}

var dataRetentionPassFn = func(s *server, ctx context.Context, fn string) (int, error) { return s.dataRetentionPass(ctx, fn) }

// retentionPasses runs the three retention functions in batches until a pass changes nothing or maxPasses passes have run (<= 0: only ctx ends it).
// One failing function does not stop the others: it is left out for the rest of the run and logged.
func (s *server) retentionPasses(ctx context.Context, maxPasses int) (complete bool, err error) {
	failed := map[string]bool{}
	var failures []string
	for pass := 0; maxPasses <= 0 || pass < maxPasses; pass++ {
		changed := 0
		for _, fn := range []string{"iam_v2.run_data_retention", "public.run_data_retention_public", "iam_v2.run_staff_note_retention", "public.run_event_table_trim"} {
			if failed[fn] {
				continue
			}
			n, perr := dataRetentionPassFn(s, ctx, fn)
			if perr != nil {
				if ctx.Err() != nil {
					return false, errors.Join(append([]error{ctx.Err()}, errorsFrom(failures)...)...)
				}
				var pgErr *pgconn.PgError
				if errors.As(perr, &pgErr) && pgErr.Code == "42883" {
					slog.Warn("data retention unavailable: this database predates the migration that adds it", "fn", fn)
				} else {
					slog.Warn("data retention run failed", "fn", fn, "err", perr)
				}
				failed[fn] = true
				failures = append(failures, fn+": "+perr.Error())
				continue
			}
			changed += n
		}
		if changed == 0 {
			return len(failures) == 0, errors.Join(errorsFrom(failures)...)
		}
	}
	return false, errors.Join(errorsFrom(failures)...)
}

func errorsFrom(msgs []string) []error {
	out := make([]error, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, errors.New(m))
	}
	return out
}

// runStaffRetention anonymises staff accounts disabled longer than the setting and clears staff addresses older than the activity-log period.
func (s *server) runStaffRetention(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var out map[string]int
	err := s.db.QueryRow(cctx, `SELECT public.run_staff_retention($1::uuid,$2::uuid)`, s.tenID, s.siteID).Scan(&out)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42883" {
			slog.Warn("staff retention unavailable: this database predates migration 0138")
		} else if ctx.Err() == nil {
			slog.Warn("staff retention run failed", "err", err)
		}
		return
	}
	if out["staff_accounts_anonymised"]+out["audit_entries"]+out["staff_addresses_cleared"]+out["assisted_requests_expired"] > 0 {
		slog.Info("staff data anonymised by retention", "accounts", out["staff_accounts_anonymised"],
			"audit_entries", out["audit_entries"], "addresses", out["staff_addresses_cleared"],
			"assisted_requests_expired", out["assisted_requests_expired"])
	}
}

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

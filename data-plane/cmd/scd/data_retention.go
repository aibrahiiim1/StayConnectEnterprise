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
	_, _ = s.retentionPasses(ctx, dataRetentionMaxPasses)
	s.runStaffRetention(ctx)
	s.runLeftovers(ctx)
}

// dataRetentionPassFn is the pass the loops call; a var so a test can stand in for the database.
var dataRetentionPassFn = func(s *server, ctx context.Context, fn string) (int, error) { return s.dataRetentionPass(ctx, fn) }

// retentionPasses runs the three retention functions in batches until a whole pass changes nothing, or maxPasses passes have run
// (maxPasses <= 0: no limit, only ctx ends it). complete says the backlog is gone; err lists the functions that failed. ONE FAILING
// FUNCTION DOES NOT STOP THE OTHERS (GDPR audit C10): it is left out for the rest of the run and logged.
func (s *server) retentionPasses(ctx context.Context, maxPasses int) (complete bool, err error) {
	failed := map[string]bool{}
	var failures []string
	for pass := 0; maxPasses <= 0 || pass < maxPasses; pass++ {
		changed := 0
		for _, fn := range []string{"iam_v2.run_data_retention", "public.run_data_retention_public", "iam_v2.run_staff_note_retention"} {
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

// runStaffRetention anonymises staff accounts disabled for longer than the appliance's setting and clears staff
// addresses older than the activity-log period (migration 0138, GDPR audit B4). One call: it is bounded itself.
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

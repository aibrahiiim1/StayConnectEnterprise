// Command sitemigrate copies one tenant+site's guest-domain data from the
// central cloud database (which today holds ALL data) into an isolated
// site-local (edge) database whose schema is migrations/0001_edge_init.up.sql.
//
// Usage:
//
//	sitemigrate --source <central DSN> --dest <site DSN> \
//	    --tenant <tenant uuid> --site <site uuid> \
//	    [--dry-run] [--rollback-dir <dir>] [--force-append]
//
// Guarantees / behavior:
//
//   - FK-safe copy order (see migrationTables in tables.go).
//   - Idempotent: every INSERT on a table with a primary key uses
//     ON CONFLICT (<pk>) DO NOTHING; re-running copies zero new rows.
//   - Hypertables (accounting_records, audit_log) have NO primary key, so
//     ON CONFLICT cannot dedupe them. Strategy: if the destination already
//     holds >0 scoped rows for such a table, the table is skipped entirely
//     and reported as "skipped (dest non-empty)" unless --force-append is
//     passed, in which case rows are appended verbatim.
//   - Column lists are read from the catalogs of BOTH databases at runtime
//     and intersected, so schema drift on either side (e.g. the edge-only
//     tenants.branding column, or central-only columns the edge lacks) does
//     not break the copy. Values round-trip through text casts, preserving
//     timestamps and every other type exactly.
//   - Orphan rows (FK target missing in the scoped set, e.g. a session
//     referencing another tenant's voucher) are detected, counted, logged
//     and skipped cleanly; they never abort the run.
//   - --dry-run prints the plan (source_count / already_in_dest /
//     would_copy) without writing anything.
//   - --rollback-dir dumps the DEST database's current scoped rows per
//     table as CSV (COPY TO STDOUT) plus manifest.json BEFORE any write.
//   - Never TRUNCATEs or DELETEs anything, in either database.
//   - Exit codes: 0 success, 1 fatal error, 2 post-copy verification
//     mismatch.
//
// Multi-site caveat: vouchers, ticket_templates, guests, voucher_batches,
// auth_otps, pms_attempts and the provider/config tables are tenant-scoped
// (no site_id column). For today's single-site tenants copying all tenant
// rows is correct; multi-site tenants must be split by site manually where
// the schema lacks site_id.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5"
)

const usageLine = `sitemigrate --source <central DSN> --dest <site DSN> --tenant <tenant uuid> --site <site uuid> [--dry-run] [--rollback-dir <dir>] [--force-append]`

type config struct {
	SourceDSN   string
	DestDSN     string
	Tenant      string
	Site        string
	DryRun      bool
	RollbackDir string
	ForceAppend bool
	BatchRows   int
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

// redactDSN removes the password from a URL-style or keyword-style DSN so it
// can be logged / written into the rollback manifest safely.
func redactDSN(dsn string) string {
	// URL form: scheme://user:password@host...
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			cred := rest[:at]
			if colon := strings.Index(cred, ":"); colon >= 0 {
				return dsn[:i+3] + cred[:colon] + ":***@" + rest[at+1:]
			}
		}
		return dsn
	}
	// keyword form: password=secret
	return regexp.MustCompile(`(?i)(password\s*=\s*)\S+`).ReplaceAllString(dsn, "${1}***")
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout))
}

func run(ctx context.Context, args []string, out io.Writer) int {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	fs := flag.NewFlagSet("sitemigrate", flag.ContinueOnError)
	cfg := config{BatchRows: 1000}
	fs.StringVar(&cfg.SourceDSN, "source", "", "central (cloud) Postgres DSN to copy FROM")
	fs.StringVar(&cfg.DestDSN, "dest", "", "site-local (edge) Postgres DSN to copy INTO")
	fs.StringVar(&cfg.Tenant, "tenant", "", "tenant UUID to migrate")
	fs.StringVar(&cfg.Site, "site", "", "site UUID to migrate")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "count and print the plan without writing")
	fs.StringVar(&cfg.RollbackDir, "rollback-dir", "", "dump DEST's current scoped rows as CSV + manifest.json here before writing")
	fs.BoolVar(&cfg.ForceAppend, "force-append", false, "append into non-empty PK-less hypertables (accounting_records, audit_log) instead of skipping them")
	fs.IntVar(&cfg.BatchRows, "batch-rows", 1000, "rows per INSERT batch")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n  %s\n\n", usageLine)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}

	switch {
	case cfg.SourceDSN == "" || cfg.DestDSN == "":
		log.Error("--source and --dest are required")
		fs.Usage()
		return 1
	case !isUUID(cfg.Tenant):
		log.Error("--tenant must be a valid UUID", "got", cfg.Tenant)
		return 1
	case !isUUID(cfg.Site):
		log.Error("--site must be a valid UUID", "got", cfg.Site)
		return 1
	case cfg.SourceDSN == cfg.DestDSN:
		log.Error("--source and --dest must not be the same DSN")
		return 1
	case cfg.BatchRows < 1:
		log.Error("--batch-rows must be >= 1")
		return 1
	}

	src, err := connect(ctx, cfg.SourceDSN)
	if err != nil {
		log.Error("connect source failed", "dsn", redactDSN(cfg.SourceDSN), "err", err)
		return 1
	}
	defer src.Close(ctx)
	dst, err := connect(ctx, cfg.DestDSN)
	if err != nil {
		log.Error("connect dest failed", "dsn", redactDSN(cfg.DestDSN), "err", err)
		return 1
	}
	defer dst.Close(ctx)

	if err := validateScope(ctx, src, cfg); err != nil {
		log.Error("scope validation failed", "err", err)
		return 1
	}
	log.Info("scope validated", "tenant", cfg.Tenant, "site", cfg.Site,
		"source", redactDSN(cfg.SourceDSN), "dest", redactDSN(cfg.DestDSN))

	plan, err := gatherPlan(ctx, src, dst, cfg)
	if err != nil {
		log.Error("planning failed", "err", err)
		return 1
	}

	orphans, err := scanOrphans(ctx, src, cfg)
	if err != nil {
		log.Error("orphan pre-scan failed", "err", err)
		return 1
	}

	if cfg.DryRun {
		printPlan(out, plan)
		printOrphans(out, orphans)
		fmt.Fprintln(out, "\nDRY RUN — nothing was written.")
		return 0
	}

	if cfg.RollbackDir != "" {
		if err := dumpRollback(ctx, dst, cfg, log); err != nil {
			log.Error("rollback dump failed; aborting before any write", "err", err)
			return 1
		}
		log.Info("rollback package written", "dir", cfg.RollbackDir)
	}

	results := make([]copyResult, 0, len(migrationTables))
	for _, spec := range migrationTables {
		res, err := copyTable(ctx, src, dst, spec, cfg, log)
		if err != nil {
			log.Error("copy failed", "table", spec.Name, "err", err)
			return 1
		}
		results = append(results, res)
	}

	printOrphans(out, orphans)
	ok, err := verifyAndPrint(ctx, out, dst, cfg, results)
	if err != nil {
		log.Error("verification failed to run", "err", err)
		return 1
	}
	if !ok {
		log.Error("verification found mismatches")
		return 2
	}
	fmt.Fprintln(out, "\nMigration complete — all scoped counts match.")
	return 0
}

func connect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	// A consistent session timezone makes timestamptz text round-trips
	// byte-identical (the stored instant is preserved regardless).
	if _, err := c.Exec(ctx, "SET TIME ZONE 'UTC'"); err != nil {
		c.Close(ctx)
		return nil, fmt.Errorf("set timezone: %w", err)
	}
	return c, nil
}

// validateScope ensures the tenant and site exist in the SOURCE and belong
// together before anything else happens.
func validateScope(ctx context.Context, src *pgx.Conn, cfg config) error {
	var n int64
	if err := src.QueryRow(ctx, "SELECT count(*) FROM tenants WHERE id = $1", cfg.Tenant).Scan(&n); err != nil {
		return fmt.Errorf("tenant lookup: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("tenant %s not found in source", cfg.Tenant)
	}
	if err := src.QueryRow(ctx, "SELECT count(*) FROM sites WHERE id = $1 AND tenant_id = $2", cfg.Site, cfg.Tenant).Scan(&n); err != nil {
		return fmt.Errorf("site lookup: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("site %s not found in source (or does not belong to tenant %s)", cfg.Site, cfg.Tenant)
	}
	return nil
}

// planRow is one line of the --dry-run plan.
type planRow struct {
	Table         string
	SourceCount   int64
	AlreadyInDest int64
	WouldCopy     int64
	Note          string
}

func gatherPlan(ctx context.Context, src, dst *pgx.Conn, cfg config) ([]planRow, error) {
	plan := make([]planRow, 0, len(migrationTables))
	for _, spec := range migrationTables {
		srcN, err := scopedCount(ctx, src, spec, cfg)
		if err != nil {
			return nil, fmt.Errorf("%s: source count: %w", spec.Name, err)
		}
		dstN, err := scopedCount(ctx, dst, spec, cfg)
		if err != nil {
			return nil, fmt.Errorf("%s: dest count: %w", spec.Name, err)
		}
		row := planRow{Table: spec.Name, SourceCount: srcN, AlreadyInDest: dstN, Note: spec.Note}
		switch {
		case spec.PK == "" && dstN > 0 && !cfg.ForceAppend:
			row.WouldCopy = 0
			row.Note = "skipped (dest non-empty; no PK — pass --force-append to append)"
		case spec.PK == "":
			row.WouldCopy = srcN
		default:
			n, err := wouldCopyCount(ctx, src, dst, spec, cfg)
			if err != nil {
				return nil, fmt.Errorf("%s: would-copy count: %w", spec.Name, err)
			}
			row.WouldCopy = n
		}
		plan = append(plan, row)
	}
	return plan, nil
}

// wouldCopyCount computes, exactly, how many scoped source rows are absent
// from dest by primary key (all migrated PK tables have single-column PKs).
func wouldCopyCount(ctx context.Context, src, dst *pgx.Conn, spec tableSpec, cfg config) (int64, error) {
	rows, err := dst.Query(ctx,
		fmt.Sprintf("SELECT %s::text FROM %s WHERE %s", spec.PK, spec.Name, spec.Where),
		whereArgs(spec.Where, cfg.Tenant, cfg.Site)...)
	if err != nil {
		return 0, err
	}
	destPKs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	if len(destPKs) == 0 {
		return scopedCount(ctx, src, spec, cfg)
	}
	args := whereArgs(spec.Where, cfg.Tenant, cfg.Site)
	q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s AND NOT (%s::text = ANY($%d))",
		spec.Name, spec.Where, spec.PK, len(args)+1)
	args = append(args, destPKs)
	var n int64
	err = src.QueryRow(ctx, q, args...).Scan(&n)
	return n, err
}

func scopedCount(ctx context.Context, c *pgx.Conn, spec tableSpec, cfg config) (int64, error) {
	var n int64
	err := c.QueryRow(ctx,
		fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", spec.Name, spec.Where),
		whereArgs(spec.Where, cfg.Tenant, cfg.Site)...).Scan(&n)
	return n, err
}

func printPlan(out io.Writer, plan []planRow) {
	fmt.Fprintln(out, "\nMigration plan (scoped to tenant+site):")
	w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TABLE\tSOURCE_COUNT\tALREADY_IN_DEST\tWOULD_COPY\tNOTE")
	for _, r := range plan {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\n", r.Table, r.SourceCount, r.AlreadyInDest, r.WouldCopy, r.Note)
	}
	w.Flush()
}

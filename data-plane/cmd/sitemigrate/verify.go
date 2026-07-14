package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5"
)

// orphanCount reports scoped source rows whose FK target falls outside the
// scoped set (e.g. a session referencing a voucher of another tenant, or a
// voucher_batch created_by a platform-admin-only operator that is not
// migrated). Such rows are skipped cleanly during the copy.
type orphanCount struct {
	Table    string
	Col      string
	RefTable string
	Count    int64
}

// scanOrphans runs the FK pre-scan against the SOURCE. It is a per-edge scan
// (not cascading): the copy itself is the authority — it validates against
// the live dest, so cascaded orphans are also caught and skipped there.
func scanOrphans(ctx context.Context, src *pgx.Conn, cfg config) ([]orphanCount, error) {
	var out []orphanCount
	for _, spec := range migrationTables {
		for _, fk := range spec.FKs {
			ref, ok := specFor(fk.RefTable)
			if !ok {
				return nil, fmt.Errorf("%s.%s references unknown table %s", spec.Name, fk.Col, fk.RefTable)
			}
			q := fmt.Sprintf(
				"SELECT count(*) FROM %s WHERE %s AND %s IS NOT NULL AND %s NOT IN (SELECT %s FROM %s WHERE %s)",
				spec.Name, spec.Where, fk.Col, fk.Col, fk.RefCol, fk.RefTable, ref.Where)
			var n int64
			if err := src.QueryRow(ctx, q, whereArgs(q, cfg.Tenant, cfg.Site)...).Scan(&n); err != nil {
				return nil, fmt.Errorf("orphan scan %s.%s: %w", spec.Name, fk.Col, err)
			}
			if n > 0 {
				out = append(out, orphanCount{spec.Name, fk.Col, fk.RefTable, n})
			}
		}
	}
	return out, nil
}

func printOrphans(out io.Writer, orphans []orphanCount) {
	if len(orphans) == 0 {
		fmt.Fprintln(out, "\nOrphan pre-scan: none — every scoped FK resolves inside the scoped set.")
		return
	}
	fmt.Fprintln(out, "\nOrphan pre-scan (rows whose FK target is outside the scoped set; skipped cleanly):")
	w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TABLE\tCOLUMN\tMISSING_IN\tROWS")
	for _, o := range orphans {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", o.Table, o.Col, o.RefTable, o.Count)
	}
	w.Flush()
}

// verifyAndPrint recounts every table's scoped rows in dest and compares
// against what the copy says should be there. Returns ok=false if any table
// mismatches (caller exits non-zero).
//
// Expectations per table:
//   - PK tables: dest == source - skipped_orphans (conflicts fold in — they
//     were already present).
//   - PK-less hypertable, dest was empty: dest == source - skipped.
//   - PK-less hypertable, skipped (dest non-empty, no --force-append):
//     reported as SKIPPED, not a mismatch — rerun with --force-append or
//     reconcile manually.
//   - PK-less hypertable with --force-append: dest == pre-existing + copied.
func verifyAndPrint(ctx context.Context, out io.Writer, dst *pgx.Conn, cfg config, results []copyResult) (bool, error) {
	fmt.Fprintln(out, "\nVerification (scoped rows, source vs dest):")
	w := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TABLE\tSOURCE\tDEST\tCOPIED\tALREADY\tSKIPPED\tSTATUS")
	ok := true
	for _, r := range results {
		spec, _ := specFor(r.Table)
		destN, err := scopedCount(ctx, dst, spec, cfg)
		if err != nil {
			return false, fmt.Errorf("%s: dest recount: %w", r.Table, err)
		}

		var status string
		switch {
		case r.SkippedTable:
			status = "SKIPPED (dest non-empty; no PK — use --force-append)"
		case r.Appended:
			if destN == r.PreDestRows+r.Copied {
				status = fmt.Sprintf("OK (appended onto %d pre-existing)", r.PreDestRows)
			} else {
				status = fmt.Sprintf("MISMATCH (expected %d)", r.PreDestRows+r.Copied)
				ok = false
			}
		default:
			expected := r.SourceRows - r.Skipped
			if destN == expected {
				status = "OK"
				if r.Skipped > 0 {
					status = fmt.Sprintf("OK (%d orphans skipped)", r.Skipped)
				}
			} else {
				status = fmt.Sprintf("MISMATCH (expected %d)", expected)
				ok = false
			}
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			r.Table, r.SourceRows, destN, r.Copied, r.Conflicts, r.Skipped, status)
	}
	w.Flush()
	if !ok {
		fmt.Fprintln(out, "\nVERIFICATION FAILED — at least one table mismatched (flagged above).")
	}
	return ok, nil
}

// sanity guard referenced by tests: no statement anywhere in this program may
// mutate destructively. Kept as a compile-time-visible constant list so the
// unit test can assert the invariant over the SQL builders' output.
var forbiddenSQL = []string{"TRUNCATE", "DELETE FROM", "DROP TABLE"}

func containsForbidden(sql string) bool {
	up := strings.ToUpper(sql)
	for _, f := range forbiddenSQL {
		if strings.Contains(up, f) {
			return true
		}
	}
	return false
}

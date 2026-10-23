package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5"
)

// Scoped source rows whose FK target falls outside the scoped set; skipped cleanly during the copy.
type orphanCount struct {
	Table    string
	Col      string
	RefTable string
	Count    int64
}

// A per-edge pre-scan of the SOURCE, not cascading: the copy validates against the live dest, which catches cascaded orphans.
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

// Recounts each table scoped rows in dest. PK tables: dest == source - skipped orphans. PK-less hypertables: dest == source - skipped when dest was empty,
// SKIPPED (not a mismatch) when non-empty without --force-append, pre-existing + copied with it. ok=false on any mismatch.
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

// No statement in this program may mutate destructively; kept visible so the unit test can assert it over the SQL builders output.
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

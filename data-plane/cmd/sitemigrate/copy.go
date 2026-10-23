package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// maxBindParams keeps batches under the Postgres 65535 bind-parameter limit.
const maxBindParams = 60000

type copyResult struct {
	Table        string
	SourceRows   int64 // scoped rows in source at copy time
	PreDestRows  int64 // scoped rows already in dest before the copy
	Copied       int64 // rows actually inserted
	Conflicts    int64 // rows already present (ON CONFLICT DO NOTHING hits)
	Skipped      int64 // orphan/integrity rows skipped cleanly
	NulledFK     int64 // rows kept by nulling a nullable FK to a skipped target
	SkippedTable bool  // PK-less hypertable skipped (dest non-empty)
	Appended     bool  // PK-less hypertable appended via --force-append
}

func columnTypes(ctx context.Context, c *pgx.Conn, table string) ([]string, map[string]string, error) {
	rows, err := c.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = $1
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, table)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var names []string
	types := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, nil, err
		}
		names = append(names, name)
		types[name] = typ
	}
	return names, types, rows.Err()
}

// Values travel as text with per-column casts back to the destination types, so nothing is lossy; ON CONFLICT (<pk>) DO NOTHING makes it idempotent
// when a PK exists.
func buildInsertSQL(table string, cols, types []string, pk string, nrows int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(table)
	b.WriteString(" (")
	b.WriteString(strings.Join(cols, ", "))
	b.WriteString(") VALUES ")
	p := 1
	for r := 0; r < nrows; r++ {
		if r > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for i := range cols {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "$%d::%s", p, types[i])
			p++
		}
		b.WriteByte(')')
	}
	if pk != "" {
		b.WriteString(" ON CONFLICT (")
		b.WriteString(pk)
		b.WriteString(") DO NOTHING")
	}
	return b.String()
}

func batchSizeFor(configured, ncols int) int {
	if ncols <= 0 {
		return configured
	}
	if m := maxBindParams / ncols; m < configured {
		if m < 1 {
			return 1
		}
		return m
	}
	return configured
}

// Values are SELECTed with ::text and INSERTed with casts back to the destination types (both sessions run in UTC), an exact round trip. A batch that
// trips an integrity error (class 23xxx) is retried row by row and offending rows are skipped; checking against the live dest lets cascaded orphans
// resolve because parents were copied first.
func copyTable(ctx context.Context, src, dst *pgx.Conn, spec tableSpec, cfg config, log *slog.Logger) (copyResult, error) {
	res := copyResult{Table: spec.Name}

	var err error
	if res.SourceRows, err = scopedCount(ctx, src, spec, cfg); err != nil {
		return res, fmt.Errorf("source count: %w", err)
	}
	if res.PreDestRows, err = scopedCount(ctx, dst, spec, cfg); err != nil {
		return res, fmt.Errorf("dest count: %w", err)
	}

	// PK-less hypertables cannot dedupe via ON CONFLICT, so a non-empty dest would duplicate on rerun: skip unless --force-append.
	if spec.PK == "" && res.PreDestRows > 0 {
		if !cfg.ForceAppend {
			res.SkippedTable = true
			log.Warn("skipping PK-less table: dest already has scoped rows (pass --force-append to append)",
				"table", spec.Name, "dest_rows", res.PreDestRows)
			return res, nil
		}
		res.Appended = true
		log.Warn("appending into non-empty PK-less table (--force-append)", "table", spec.Name)
	}

	srcCols, _, err := columnTypes(ctx, src, spec.Name)
	if err != nil {
		return res, fmt.Errorf("source columns: %w", err)
	}
	dstCols, dstTypes, err := columnTypes(ctx, dst, spec.Name)
	if err != nil {
		return res, fmt.Errorf("dest columns: %w", err)
	}
	cols := intersectColumns(srcCols, dstCols)
	if len(cols) == 0 {
		return res, fmt.Errorf("no common columns between source and dest for %s", spec.Name)
	}
	if dropped := len(srcCols) - len(cols); dropped > 0 {
		log.Info("schema drift: source columns absent in dest are not copied",
			"table", spec.Name, "dropped", dropped)
	}
	types := make([]string, len(cols))
	sel := make([]string, len(cols))
	for i, c := range cols {
		types[i] = dstTypes[c]
		sel[i] = c + "::text"
	}

	batchRows := batchSizeFor(cfg.BatchRows, len(cols))
	fullSQL := buildInsertSQL(spec.Name, cols, types, spec.PK, batchRows)
	oneSQL := buildInsertSQL(spec.Name, cols, types, spec.PK, 1)

	// Nullable FK column indexes: last-resort retry for rows whose FK target is legitimately outside the scoped set.
	nullableFK, err := nullableFKCols(ctx, dst, spec.Name)
	if err != nil {
		return res, fmt.Errorf("fk introspection: %w", err)
	}
	var nullableIdx []int
	for i, c := range cols {
		if nullableFK[c] {
			nullableIdx = append(nullableIdx, i)
		}
	}

	rows, err := src.Query(ctx,
		fmt.Sprintf("SELECT %s FROM %s WHERE %s", strings.Join(sel, ", "), spec.Name, spec.Where),
		whereArgs(spec.Where, cfg.Tenant, cfg.Site)...)
	if err != nil {
		return res, fmt.Errorf("source select: %w", err)
	}
	defer rows.Close()

	flush := func(batch [][]*string) error {
		if len(batch) == 0 {
			return nil
		}
		sql := fullSQL
		if len(batch) != batchRows {
			sql = buildInsertSQL(spec.Name, cols, types, spec.PK, len(batch))
		}
		args := make([]any, 0, len(batch)*len(cols))
		for _, r := range batch {
			for _, v := range r {
				args = append(args, v)
			}
		}
		tag, err := dst.Exec(ctx, sql, args...)
		if err == nil {
			res.Copied += tag.RowsAffected()
			res.Conflicts += int64(len(batch)) - tag.RowsAffected()
			return nil
		}
		if !isIntegrityErr(err) {
			return err
		}
		for _, r := range batch {
			args := make([]any, len(r))
			for i, v := range r {
				args[i] = v
			}
			tag, err := dst.Exec(ctx, oneSQL, args...)
			switch {
			case err == nil:
				res.Copied += tag.RowsAffected()
				res.Conflicts += 1 - tag.RowsAffected()
			case isFKErr(err) && len(nullableIdx) > 0:
				// The FK target was excluded from the scoped set (e.g. a skipped platform-admin operator): null the nullable FKs and retry once.
				retry := make([]any, len(args))
				copy(retry, args)
				for _, i := range nullableIdx {
					retry[i] = nil
				}
				if tag2, err2 := dst.Exec(ctx, oneSQL, retry...); err2 == nil {
					res.Copied += tag2.RowsAffected()
					res.NulledFK++
					log.Info("copied row with nulled FK columns (target outside scoped set)",
						"table", spec.Name)
				} else {
					res.Skipped++
					log.Warn("skipping row (FK retry failed)", "table", spec.Name, "err", err2)
				}
			case isIntegrityErr(err):
				res.Skipped++
				log.Warn("skipping row (integrity violation — FK target outside scoped set, or unique clash)",
					"table", spec.Name, "err", err)
			default:
				return err
			}
		}
		return nil
	}

	batch := make([][]*string, 0, batchRows)
	for rows.Next() {
		vals := make([]*string, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return res, fmt.Errorf("scan: %w", err)
		}
		batch = append(batch, vals)
		if len(batch) == batchRows {
			if err := flush(batch); err != nil {
				return res, fmt.Errorf("insert: %w", err)
			}
			batch = batch[:0]
		}
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("source read: %w", err)
	}
	if err := flush(batch); err != nil {
		return res, fmt.Errorf("insert: %w", err)
	}

	log.Info("table copied", "table", spec.Name, "source_rows", res.SourceRows,
		"copied", res.Copied, "already_present", res.Conflicts, "skipped", res.Skipped)
	return res, nil
}

func isIntegrityErr(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, "23")
}

func isFKErr(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// Rows whose FK target was legitimately excluded (e.g. a voucher_batch created_by a skipped platform admin) are copied with those columns nulled:
// provenance is lost, data is not.
func nullableFKCols(ctx context.Context, dst *pgx.Conn, table string) (map[string]bool, error) {
	rows, err := dst.Query(ctx, `
        SELECT DISTINCT a.attname
          FROM pg_constraint c
          JOIN unnest(c.conkey) AS k(attnum) ON true
          JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
         WHERE c.contype = 'f' AND c.conrelid = $1::regclass AND NOT a.attnotnull
    `, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		out[col] = true
	}
	return out, rows.Err()
}

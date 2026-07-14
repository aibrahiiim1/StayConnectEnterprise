package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

// rollbackManifest is written as manifest.json next to the per-table CSVs so
// an operator can see exactly what the dest held before this run touched it.
type rollbackManifest struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Tool        string               `json:"tool"`
	DestDSN     string               `json:"dest_dsn"` // password redacted
	TenantID    string               `json:"tenant_id"`
	SiteID      string               `json:"site_id"`
	Tables      []rollbackTableEntry `json:"tables"`
}

type rollbackTableEntry struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
	File string `json:"file"`
}

// dumpRollback snapshots the DEST database's current scoped rows, one CSV per
// table (COPY ... TO STDOUT WITH CSV HEADER), before anything is written.
// COPY does not take bind parameters, so the (already UUID-validated) tenant
// and site are inlined as literals.
func dumpRollback(ctx context.Context, dst *pgx.Conn, cfg config, log *slog.Logger) error {
	if err := os.MkdirAll(cfg.RollbackDir, 0o755); err != nil {
		return fmt.Errorf("create rollback dir: %w", err)
	}
	manifest := rollbackManifest{
		GeneratedAt: time.Now().UTC(),
		Tool:        "sitemigrate",
		DestDSN:     redactDSN(cfg.DestDSN),
		TenantID:    cfg.Tenant,
		SiteID:      cfg.Site,
	}
	for _, spec := range migrationTables {
		n, err := scopedCount(ctx, dst, spec, cfg)
		if err != nil {
			return fmt.Errorf("%s: count: %w", spec.Name, err)
		}
		fileName := spec.Name + ".csv"
		path := filepath.Join(cfg.RollbackDir, fileName)
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		copySQL := fmt.Sprintf("COPY (SELECT * FROM %s WHERE %s) TO STDOUT WITH (FORMAT csv, HEADER true)",
			spec.Name, inlineParams(spec.Where, cfg.Tenant, cfg.Site))
		_, err = dst.PgConn().CopyTo(ctx, f, copySQL)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("dump %s: %w", spec.Name, err)
		}
		manifest.Tables = append(manifest.Tables, rollbackTableEntry{Name: spec.Name, Rows: n, File: fileName})
		log.Info("rollback snapshot written", "table", spec.Name, "rows", n, "file", path)
	}
	buf, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cfg.RollbackDir, "manifest.json"), append(buf, '\n'), 0o644)
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

// Compliance archive written before a cross-customer purge: the departing tenant's financial and commercial rows are exported, the
// SHA-256 recorded in compliance_archives, and the purge is gated on that record. If the export fails the purge does not happen (fail closed).
// receipt_verified stays false: no external archival authority exists to countersign, and writing true would fabricate managed state.

// complianceArchiveDir sits under the backup root, not /etc: it is bulk data with a dump's retention and off-box-copy handling.
const complianceArchiveDir = "/var/backups/stayconnect/compliance"

const EnvComplianceArchiveDir = "STAYCONNECT_COMPLIANCE_ARCHIVE_DIR"

// archivedTables is the financial and commercial record, deliberately not the guest-identity tables a cross-tenant transition exists to remove.
var archivedTables = []string{
	"iam_v2.purchases",
	"iam_v2.settlements",
	"iam_v2.payment_transactions",
	"iam_v2.payment_transaction_events",
	"iam_v2.pms_postings",
	"iam_v2.posting_attempts",
	"iam_v2.posting_review_actions",
	"iam_v2.entitlements",
}

// archiveTenantBeforePurge exports the record and returns only once the database agrees an archive exists.
// It runs before and outside the purge transaction so the archive survives a failed purge.
func (s *server) archiveTenantBeforePurge(ctx context.Context, departing string) error {
	dir := os.Getenv(EnvComplianceArchiveDir)
	if dir == "" {
		dir = complianceArchiveDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("compliance archive directory: %w", err)
	}

	counts := map[string]int{}
	payload := map[string]any{
		"schema_version": 1,
		"tenant_id":      departing,
		"exported_at":    time.Now().UTC().Format(time.RFC3339),
		"note": "Compliance archive produced before a cross-customer purge (contract C35). Rows are " +
			"exported as JSON exactly as stored. This artefact has NOT been countersigned by an external " +
			"archival authority: no such service exists in this product.",
	}
	tables := map[string][]json.RawMessage{}
	for _, tbl := range archivedTables {
		rows, err := s.db.Query(ctx,
			`SELECT to_jsonb(t) FROM `+tbl+` t WHERE t.tenant_id = $1`, departing)
		if err != nil {
			// A table that does not exist on this chain is not a reason to skip the archive silently.
			return fmt.Errorf("archive %s: %w", tbl, err)
		}
		var out []json.RawMessage
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return fmt.Errorf("archive %s scan: %w", tbl, err)
			}
			out = append(out, json.RawMessage(raw))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("archive %s: %w", tbl, err)
		}
		tables[tbl] = out
		counts[tbl] = len(out)
	}
	payload["tables"] = tables
	payload["row_counts"] = counts

	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode compliance archive: %w", err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	// Encrypted like the backups, to the same key holders: it holds guest numbers and payment references. The digest is of the
	// document itself. With no holder set up it is written mode 0600 and the log says so.
	recipients, rerr := s.backupRecipients(ctx)
	if rerr != nil {
		return fmt.Errorf("compliance archive: %w", rerr)
	}
	name := fmt.Sprintf("tenant-%s-%s.json", departing, digest[:12])
	if len(recipients) > 0 {
		name += ".age"
	} else {
		slog.Warn("compliance archive written without encryption: no backup key holder is set up")
	}
	path := filepath.Join(dir, name)

	// Write to a temp file and rename so a crash cannot leave a truncated artefact whose digest is already recorded.
	tmp := path + ".partial"
	if err := writeArchiveFile(tmp, body, recipients); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write compliance archive: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("commit compliance archive: %w", err)
	}

	countsJSON, _ := json.Marshal(counts)
	var id string
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.p4_record_compliance_archive($1::uuid,$2::uuid,$3,$4,$5::jsonb)::text`,
		departing, s.siteID, digest, path, string(countsJSON)).Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("compliance archive was not recorded")
		}
		return fmt.Errorf("record compliance archive: %w", err)
	}
	return nil
}

func (s *server) departingTenants(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT tenant_id::text FROM iam_v2.purchases WHERE tenant_id <> $1
		 UNION SELECT DISTINCT id::text FROM tenants WHERE id <> $1`, s.tenID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// writeArchiveFile writes the document to path, encrypted to the recipients when there are any, and makes it durable.
func writeArchiveFile(path string, body []byte, recipients []age.Recipient) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	var sink io.WriteCloser = nopCloser{f}
	if len(recipients) > 0 {
		if sink, err = backupcrypt.Encrypt(f, recipients); err != nil {
			return err
		}
	}
	if _, err := sink.Write(body); err != nil {
		return err
	}
	if err := sink.Close(); err != nil {
		return err
	}
	return f.Sync()
}

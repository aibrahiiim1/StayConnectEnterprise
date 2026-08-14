package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

// C35 — the compliance archive that must exist before a cross-customer purge.
//
// MEASURED BEFORE THIS FILE EXISTED: reconcileTenantOwnership DELETEd the departing customer's rows
// outright. iam_v2.compliance_archives had existed since mg7 and nothing in the tree wrote it, so the
// contract's "archive before purge" was a table shape and nothing else.
//
// WHAT IS DELIVERED HERE. Before anything is deleted, the departing tenant's financial and commercial rows
// are exported to a file on the appliance, the file's SHA-256 is recorded in compliance_archives, and the
// purge is gated on that record existing. If the export fails, the purge does not happen and the appliance
// stays fail-closed -- which is the correct direction: keeping a departed customer's data one boot longer is
// recoverable, and deleting it with no archive is not.
//
// WHAT IS NOT DELIVERED, and why it is not silently faked. compliance_archives.receipt_verified records that
// an EXTERNAL archival authority has countersigned custody of the artefact. No such service exists in this
// product, no key has been issued for one, and there is no endpoint to send it to. Writing true there, or
// inventing a receipt document, would be fabricating managed state. So it stays false, the reason is
// recorded in the row itself, and the missing counter-signature is reported as an external blocker rather
// than as a completed requirement.

// complianceArchiveDir is where the artefact is written. It sits under the appliance's backup root rather
// than /etc, because it is bulk data with the same retention and off-box-copy handling as a database dump.
const complianceArchiveDir = "/var/backups/stayconnect/compliance"

// EnvComplianceArchiveDir lets a test point the archive somewhere disposable.
const EnvComplianceArchiveDir = "STAYCONNECT_COMPLIANCE_ARCHIVE_DIR"

// archivedTables is what a departing customer's export contains. It is deliberately the FINANCIAL and
// commercial record -- the rows a regulator or the customer themselves could later ask about -- and not the
// guest-identity tables, whose whole purpose in a cross-tenant transition is to stop existing.
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

// archiveTenantBeforePurge exports a departing tenant's financial record, records its digest, and returns
// only once the database agrees an archive exists.
//
// It runs BEFORE the purge transaction and outside it: the archive must survive even if the purge later
// fails, because the next attempt should not have to re-export, and because an archive is evidence in its
// own right.
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
	path := filepath.Join(dir, fmt.Sprintf("tenant-%s-%s.json", departing, digest[:12]))

	// Write to a temporary file and rename, so a crash cannot leave a truncated artefact whose digest was
	// already recorded as if it described a complete one.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write compliance archive: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
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

// departingTenants lists every tenant whose data is present but is not the current owner.
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

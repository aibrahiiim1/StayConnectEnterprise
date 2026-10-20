package main

// EVERY ERASURE IS COMMITTED TO THE LEDGER BEFORE IT IS REPORTED (GDPR final mission; migration 0148; scd ledger_service.go).
//
// An erasure, or the closing of a data-protection request, is one database transaction. Inside it the database appends an
// entry to the erasure ledger. Before that transaction commits, the new entries are made durable in the two copies kept
// outside the database (a file on the appliance and the hotel's secondary storage) and read back. Only then does the
// transaction commit and the person get their answer. If either copy cannot be written, the transaction is rolled back: nothing
// was erased, nothing was reported, and the administrator is told which storage to look at. Guest internet access is not
// touched by any of this.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// ledgerClient is the part of the appliance that owns the two copies: scd in production, the ledger package itself in tests.
type ledgerClient interface {
	// Prepare reconciles the copies and says whether an erasure may begin.
	Prepare(ctx context.Context) error
	// Commit makes the entries durable in both copies.
	Commit(ctx context.Context, entries []ledger.Entry) error
	// Abort takes entries back out of both copies after Commit, when the transaction then failed.
	Abort(ctx context.Context, entries []ledger.Entry) error
}

// ledgerRefusal is a ledger failure that stops a request, with the words and the status to answer with.
type ledgerRefusal struct {
	Status  int
	Code    string
	Message string
}

func (e *ledgerRefusal) Error() string { return e.Code + ": " + e.Message }

// scdLedger is the production ledgerClient: scd's private socket.
type scdLedger struct{ c *scdClient }

func (l *scdLedger) call(ctx context.Context, path string, body any) error {
	st, raw, err := l.c.call(ctx, http.MethodPost, path, body)
	if err != nil {
		return &ledgerRefusal{http.StatusServiceUnavailable, "ledger_unreachable",
			"The appliance's ledger service could not be reached, so nothing was erased. Try again in a moment."}
	}
	if st/100 == 2 {
		return nil
	}
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		State   string `json:"state"`
	}
	_ = json.Unmarshal(raw, &e)
	return refusalFor(st, e.Error, e.Message, e.State)
}

func refusalFor(status int, code, msg, state string) *ledgerRefusal {
	if code == "" {
		switch state {
		case "not_configured":
			code = "ledger_not_configured"
		case "recovery_needed":
			code = "ledger_recovery_needed"
		default:
			code = "ledger_unavailable"
		}
	}
	text := map[string]string{
		"ledger_not_configured":       "Set up the secondary storage for the erasure ledger first (Hotel Admin → Backups). Nothing was erased.",
		"ledger_external_unavailable": "The secondary storage for the erasure ledger could not be written, so nothing was erased. Check that it is connected, then try again.",
		"ledger_recovery_needed":      "The secondary storage holds newer erasures than this appliance has. Recover the ledger first (Hotel Admin → Backups). Nothing was erased.",
		"ledger_local_damaged":        "The erasure ledger on the appliance is being repaired, so nothing was erased. Try again in a moment.",
		"ledger_local_failed":         "The erasure ledger on the appliance could not be written, so nothing was erased.",
		"ledger_conflict":             "The erasure ledger changed while this request ran. Nothing was erased; try again.",
		"ledger_unavailable":          "The erasure ledger is not in step in both places, so nothing was erased. See Hotel Admin → Backups.",
		"ledger_unreachable":          "The appliance's ledger service could not be reached, so nothing was erased.",
	}[code]
	if text == "" {
		text = "The erasure ledger could not be written, so nothing was erased."
	}
	if status < 400 {
		status = http.StatusServiceUnavailable
	}
	_ = msg // the machine message names the storage; the guest-facing text stays plain, the detail is in Backups
	return &ledgerRefusal{Status: status, Code: code, Message: text}
}

func (l *scdLedger) Prepare(ctx context.Context) error {
	return l.call(ctx, "/v1/ledger/prepare", map[string]any{})
}
func (l *scdLedger) Commit(ctx context.Context, e []ledger.Entry) error {
	return l.call(ctx, "/v1/ledger/commit", map[string]any{"entries": e})
}
func (l *scdLedger) Abort(ctx context.Context, e []ledger.Entry) error {
	return l.call(ctx, "/v1/ledger/abort", map[string]any{"entries": e})
}

// ledgerQuery runs ONE statement that writes the ledger (an erasure, a closed request) and scans its single result into dest,
// committing only after the ledger entries are durable in both copies. Database errors are returned unchanged so callers keep
// their own handling; a ledger failure is a *ledgerRefusal.
func (s *server) ledgerQuery(ctx context.Context, dest any, sql string, args ...any) error {
	if s.ledgerC == nil {
		return &ledgerRefusal{http.StatusServiceUnavailable, "ledger_unavailable", "The erasure ledger is not available on this appliance, so nothing was erased."}
	}
	if err := s.ledgerC.Prepare(ctx); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit
	if err := tx.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
		return err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_pending()::text`).Scan(&raw); err != nil {
		return err
	}
	var entries []ledger.Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	if len(entries) == 0 {
		return tx.Commit(ctx)
	}
	if err := s.ledgerC.Commit(ctx, entries); err != nil {
		return err // the deferred rollback undoes the erasure
	}
	hashes := make([]string, len(entries))
	for i, e := range entries {
		hashes[i] = e.Hash
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.ledger_confirm($1)`, hashes); err != nil {
		_ = s.ledgerC.Abort(ctx, entries) // before the rollback, while this transaction still holds the ledger lock
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = s.ledgerC.Abort(ctx, entries)
		return err
	}
	return nil
}

// writeLedgerRefusal answers a ledger failure; false when err is anything else.
func writeLedgerRefusal(w http.ResponseWriter, err error) bool {
	var lr *ledgerRefusal
	if !errors.As(err, &lr) {
		return false
	}
	writeJSON(w, lr.Status, map[string]any{"error": lr.Code, "message": lr.Message})
	return true
}

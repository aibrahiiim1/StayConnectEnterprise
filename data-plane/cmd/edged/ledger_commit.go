package main

// An erasure (or closed request) is one transaction that appends a ledger entry; before it commits, the entries are made
// durable in both external copies (appliance file and secondary storage) and read back. If either copy cannot be written
// the transaction rolls back, so nothing is erased or reported.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// ledgerClient owns the two copies: scd in production, the ledger package itself in tests.
type ledgerClient interface {
	Prepare(ctx context.Context) error
	// Commit returns scd's durability receipts, one per entry (AD-3): the database confirms an entry only against its receipt.
	// An empty list means the appliance's ledger has no key (its database predates migration 0173).
	Commit(ctx context.Context, entries []ledger.Entry) ([]string, error)
	// Abort takes entries back out of both copies after Commit when the transaction then failed.
	Abort(ctx context.Context, entries []ledger.Entry) error
}

type ledgerRefusal struct {
	Status  int
	Code    string
	Message string
}

func (e *ledgerRefusal) Error() string { return e.Code + ": " + e.Message }

// scdLedger is the production ledgerClient: scd's private socket.
type scdLedger struct{ c *scdClient }

func (l *scdLedger) call(ctx context.Context, path string, body any) error {
	_, err := l.callRaw(ctx, path, body)
	return err
}

func (l *scdLedger) callRaw(ctx context.Context, path string, body any) ([]byte, error) {
	st, raw, err := l.c.call(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, &ledgerRefusal{http.StatusServiceUnavailable, "ledger_unreachable",
			"The appliance's ledger service could not be reached, so nothing was erased. Try again in a moment."}
	}
	if st/100 == 2 {
		return raw, nil
	}
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		State   string `json:"state"`
	}
	_ = json.Unmarshal(raw, &e)
	return nil, refusalFor(st, e.Error, e.Message, e.State)
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
		"ledger_not_configured":       "Set up the secondary storage for the erasure ledger first (Admin Console → Backups). Nothing was erased.",
		"ledger_external_unavailable": "The secondary storage for the erasure ledger could not be written, so nothing was erased. Check that it is connected, then try again.",
		"ledger_recovery_needed":      "The secondary storage holds newer erasures than this appliance has. Recover the ledger first (Admin Console → Backups). Nothing was erased.",
		"ledger_local_damaged":        "The erasure ledger on the appliance is being repaired, so nothing was erased. Try again in a moment.",
		"ledger_local_failed":         "The erasure ledger on the appliance could not be written, so nothing was erased.",
		"ledger_conflict":             "The erasure ledger changed while this request ran. Nothing was erased; try again.",
		"ledger_unavailable":          "The erasure ledger is not in step in both places, so nothing was erased. See Admin Console → Backups.",
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
func (l *scdLedger) Commit(ctx context.Context, e []ledger.Entry) ([]string, error) {
	raw, err := l.callRaw(ctx, "/v1/ledger/commit", map[string]any{"entries": e})
	if err != nil {
		return nil, err
	}
	var out struct {
		Receipts []string `json:"receipts"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Receipts, nil
}
func (l *scdLedger) Abort(ctx context.Context, e []ledger.Entry) error {
	return l.call(ctx, "/v1/ledger/abort", map[string]any{"entries": e})
}

// ledgerQuery runs one ledger-writing statement, committing only after the entries are durable in both copies.
// Database errors are returned unchanged; a ledger failure is a *ledgerRefusal.
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
	// scd holds the ledger lock while checking the copies against slow secondary storage; the usual 5s lock wait would
	// fail an erasure that merely queued behind it. Waiting is safe: nothing has changed yet.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '45s'`); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
		return asLedgerBusy(err)
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
	receipts, err := s.ledgerC.Commit(ctx, entries)
	if err != nil {
		return err // the deferred rollback undoes the erasure
	}
	hashes := make([]string, len(entries))
	for i, e := range entries {
		hashes[i] = e.Hash
	}
	// The database confirms an entry only against scd's receipt (AD-3); edged has no grant to confirm a bare hash. With no
	// receipts the appliance's database has no ledger key yet (it predates migration 0173) and the old confirmation applies.
	confirm := func() error {
		if len(receipts) > 0 {
			_, err := tx.Exec(ctx, `SELECT iam_v2.ledger_confirm_checked($1,$2)`, hashes, receipts)
			return err
		}
		_, err := tx.Exec(ctx, `SELECT iam_v2.ledger_confirm($1)`, hashes)
		return err
	}
	if err := confirm(); err != nil {
		_ = s.ledgerC.Abort(ctx, entries) // before the rollback, while this transaction still holds the ledger lock
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = s.ledgerC.Abort(ctx, entries)
		return err
	}
	return nil
}

func asLedgerBusy(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
		return &ledgerRefusal{http.StatusServiceUnavailable, "ledger_busy",
			"The erasure ledger is busy (it is being checked against its secondary storage). Nothing was erased; try again in a moment."}
	}
	return err
}

func writeLedgerRefusal(w http.ResponseWriter, err error) bool {
	var lr *ledgerRefusal
	if !errors.As(err, &lr) {
		return false
	}
	writeJSON(w, lr.Status, map[string]any{"error": lr.Code, "message": lr.Message})
	return true
}

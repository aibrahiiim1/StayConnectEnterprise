//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// Fixtures commit through the real ledger package; the database ledger is shared across the run, so the files are rebuilt from it per fixture.

var (
	sharedLedgerOnce  sync.Once
	sharedLedgerStore *ledger.Store
)

func sharedLedger(t testing.TB, pool *pgxpool.Pool) *ledger.Store {
	t.Helper()
	sharedLedgerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "edged-ledger-")
		if err != nil {
			t.Fatal(err)
		}
		ext := filepath.Join(dir, "secondary")
		if err := os.MkdirAll(ext, 0o700); err != nil {
			t.Fatal(err)
		}
		sharedLedgerStore = ledger.NewStore(ledger.Config{LocalPath: filepath.Join(dir, "primary", "erasure-ledger.jsonl"), Dest: &ledger.DirDest{Root: ext}})
	})
	ctx := context.Background()
	var doc []byte
	if err := pool.QueryRow(ctx, `SELECT iam_v2.ledger_export(0)::text`).Scan(&doc); err != nil {
		t.Fatalf("export the ledger: %v", err)
	}
	var entries []ledger.Entry
	if err := json.Unmarshal(doc, &entries); err != nil {
		t.Fatal(err)
	}
	if err := sharedLedgerStore.RewriteLocal(entries); err != nil {
		t.Fatalf("rebuild the primary copy: %v", err)
	}
	if err := sharedLedgerStore.ForceExternal(ctx); err != nil {
		t.Fatalf("rebuild the secondary copy: %v", err)
	}
	return sharedLedgerStore
}

type storeLedger struct {
	st         *ledger.Store
	prepareErr error
}

func (l *storeLedger) Prepare(context.Context) error { return l.prepareErr }

func (l *storeLedger) Commit(ctx context.Context, e []ledger.Entry) error {
	return ledgerRefusalOf(l.st.Commit(ctx, e))
}

func (l *storeLedger) Abort(ctx context.Context, e []ledger.Entry) error { return l.st.Abort(ctx, e) }

func ledgerRefusalOf(err error) error {
	if err == nil {
		return nil
	}
	var ee *ledger.ExternalError
	switch {
	case errors.Is(err, ledger.ErrNotConfigured):
		return refusalFor(http.StatusConflict, "ledger_not_configured", "", "")
	case errors.Is(err, ledger.ErrExternalAhead), errors.Is(err, ledger.ErrExternalFork):
		return refusalFor(http.StatusConflict, "ledger_recovery_needed", "", "")
	case errors.As(err, &ee):
		return refusalFor(http.StatusServiceUnavailable, "ledger_external_unavailable", "", "")
	}
	return refusalFor(http.StatusInternalServerError, "ledger_local_failed", "", "")
}

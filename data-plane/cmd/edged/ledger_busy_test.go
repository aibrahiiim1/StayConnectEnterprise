package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// An erasure that could not get the ledger's lock in time is refused plainly (nothing was erased), not reported as a failure.
func TestALockTimeoutOnTheLedgerIsABusyRefusal(t *testing.T) {
	err := asLedgerBusy(&pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"})
	var lr *ledgerRefusal
	if !errors.As(err, &lr) || lr.Status != http.StatusServiceUnavailable || lr.Code != "ledger_busy" {
		t.Fatalf("lock timeout = %#v", err)
	}
	other := errors.New("something else")
	if asLedgerBusy(other) != other {
		t.Fatal("an unrelated error was rewritten")
	}
	if asLedgerBusy(&pgconn.PgError{Code: "23505"}) == nil {
		t.Fatal("a unique violation vanished")
	}
}

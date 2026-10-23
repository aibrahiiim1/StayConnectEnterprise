package main

// The restore-rollback detector (migration 0023) notices that this database is older than the appliance
// expects. It must run before any financial worker starts, and edged is the only process that builds a
// financial engine, so it runs at startup before any route is mounted.
//
// Failure posture: while Phase 4 is dark a failed reconcile is logged loudly and startup continues (no money
// can move); once transmitting it exits, because serving without knowing whether the data was restored lets a
// worker drain an outbox against it. This path only ever causes more holding; the database rules decide.

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

// reconcileFinancialEpochAtStartup returns the outcome, or "" when it could not be established.
func (s *server) reconcileFinancialEpochAtStartup(ctx context.Context) string {
	if s.tenantID == "" || s.siteID == "" {
		// No signed assignment means no site history to have been restored; not a fault.
		slog.Info("financial epoch reconcile skipped: no tenant/site assignment yet")
		return ""
	}

	eng, err := payment.NewProductionEngine(s.db, nil)
	if err != nil {
		return s.financialEpochUnavailable("financial engine could not be constructed", err)
	}

	// Startup must not hang on a slow database.
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	outcome, err := eng.ReconcileEpoch(rctx, s.tenantID, s.siteID)
	if err != nil {
		return s.financialEpochUnavailable("financial epoch could not be reconciled", err)
	}

	switch outcome {
	case "RECOVERY_ENTERED":
		// Money movement is already held by the database triggers; this process neither adds nor lifts the hold.
		slog.Warn("FINANCIAL RECOVERY ENTERED: this site's financial history looks restored, and money "+
			"movement is held until an operator reconciles what already happened. Guest access is "+
			"unaffected.",
			"outcome", outcome, "tenant", s.tenantID, "site", s.siteID)
	case "RECOVERY_ACTIVE":
		slog.Warn("financial recovery is ALREADY active for this site; money movement remains held",
			"outcome", outcome, "tenant", s.tenantID, "site", s.siteID)
	case "INITIALIZED":
		slog.Info("financial epoch initialized for this site", "outcome", outcome)
	default:
		slog.Info("financial epoch reconciled", "outcome", outcome)
	}
	return outcome
}

func (s *server) financialEpochUnavailable(what string, err error) string {
	if s.financialCfg.Dark() {
		slog.Error(what+" -- Phase 4 is DARK so nothing can move regardless, but the restore-rollback "+
			"detector did NOT run and this startup therefore proves nothing about whether this database "+
			"was restored", "err", err)
		return ""
	}
	slog.Error("REFUSING TO SERVE: "+what+", and Phase 4 is NOT dark. A financial worker must not run "+
		"without knowing whether this database was restored.", "err", err)
	os.Exit(2)
	return ""
}

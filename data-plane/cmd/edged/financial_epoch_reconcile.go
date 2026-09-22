package main

// THE RESTORE-ROLLBACK DETECTOR HAD NO CALLER.
//
// Migration 0023 exists to notice one thing: that this database is OLDER than the appliance knows it should
// be, because it was restored. The mechanism is two independent signals, and the reasoning is written into
// the migration:
//
//	the management marker   catches the SUPPORTED restore. `pg_restore -d stayconnect_site <dump>` into the
//	                        existing cluster changes nothing about the cluster, so an identity check sees
//	                        nothing at all. The marker lives OUTSIDE the database, in /etc/stayconnect, and
//	                        keeps counting forward across a restore.
//	the system identity     catches what the marker cannot: a dump restored into a NEW cluster, a promoted
//	                        replica, a cloned appliance -- where the marker may have travelled with
//	                        everything else.
//
// payment.ReconcileEpoch is the process half of that, and its own comment states the ordering rule: "it runs
// BEFORE any financial worker starts. A worker that begins draining an outbox and only then discovers it is
// running on restored data has already sent whatever it sent."
//
// NOTHING CALLED IT. A grep for ReconcileEpoch outside tests returned its own definition and two comments
// about itself. So the detector was delivered, unit- and integration-proven, and never invoked by any
// running service -- and the marker it reads is uninitialised, which governance records as a
// PRE-FINANCIAL-ENABLE PREREQUISITE. Both halves of restore-rollback detection were inert.
//
// WHY edged, AND WHY AT STARTUP. edged is the only process in this system that constructs a financial engine
// at all (payment.NewProductionEngine, per request, in resources_phase4_finops.go), so it is the only
// process with a financial worker to run before. Calling it here -- after the flags are loaded and before
// any route is mounted -- satisfies the ordering rule for every financial path that exists today.
//
// THE FAILURE POSTURE MIRRORS THE PHASE-3 PRECEDENT rather than inventing one. Phase 3's controlled-writer
// boundary is verified by every writing service before it serves anything, and the service EXITS if it does
// not hold -- but only WITH THE FLAGS ON. The same rule applies here:
//
//	Phase 4 DARK (always, today)  a failed reconcile is logged loudly and startup continues. There is
//	                              nothing to hold: transmission is refused by the dark guard, no provider
//	                              adapter exists, and no money can move whatever this function returns.
//	Phase 4 transmitting          a failed reconcile EXITS. At that point money can move, and not knowing
//	                              whether this is restored data is precisely the condition 0023 exists to
//	                              stop. Refusing to serve is the only honest answer.
//
// It can only ever cause MORE holding. Nothing in this path releases a hold or clears an epoch: the
// database rules decide, this reports what they decided.

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

// reconcileFinancialEpochAtStartup runs migration 0023's detector before any route is mounted.
//
// Returns the outcome string for the caller to record, or "" when it could not be established.
func (s *server) reconcileFinancialEpochAtStartup(ctx context.Context) string {
	if s.tenantID == "" || s.siteID == "" {
		// An appliance with no signed assignment has no site whose financial history could have been
		// restored. This is the ordinary pre-assignment state, not a fault.
		slog.Info("financial epoch reconcile skipped: no tenant/site assignment yet")
		return ""
	}

	eng, err := payment.NewProductionEngine(s.db, nil)
	if err != nil {
		return s.financialEpochUnavailable("financial engine could not be constructed", err)
	}

	// Bounded: startup must not hang on a database that is slow to accept connections. The reconcile is a
	// single function call, so a generous timeout is still short.
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	outcome, err := eng.ReconcileEpoch(rctx, s.tenantID, s.siteID)
	if err != nil {
		return s.financialEpochUnavailable("financial epoch could not be reconciled", err)
	}

	switch outcome {
	case "RECOVERY_ENTERED":
		// The detector fired. Money movement is ALREADY held by the database triggers from 0019 -- this
		// process does not need to do anything to make that true, and could not undo it if it tried.
		slog.Warn("FINANCIAL RECOVERY ENTERED: this site's financial history looks restored, and money "+
			"movement is held until an operator reconciles what already happened. Guest access is "+
			"unaffected.",
			"outcome", outcome, "tenant", s.tenantID, "site", s.siteID)
	case "RECOVERY_ACTIVE":
		slog.Warn("financial recovery is ALREADY active for this site; money movement remains held",
			"outcome", outcome, "tenant", s.tenantID, "site", s.siteID)
	case "INITIALIZED":
		// First run for this site: the epoch is seeded from what it finds. Worth an Info line because it
		// happens exactly once and is the moment the detector starts being able to detect anything.
		slog.Info("financial epoch initialized for this site", "outcome", outcome)
	default:
		slog.Info("financial epoch reconciled", "outcome", outcome)
	}
	return outcome
}

// financialEpochUnavailable applies the failure posture: loud in the dark, fatal when money can move.
func (s *server) financialEpochUnavailable(what string, err error) string {
	if s.financialCfg.Dark() {
		slog.Error(what+" -- Phase 4 is DARK so nothing can move regardless, but the restore-rollback "+
			"detector did NOT run and this startup therefore proves nothing about whether this database "+
			"was restored", "err", err)
		return ""
	}
	// Phase 4 is transmitting. Not knowing whether this is restored data is the exact condition migration
	// 0023 exists to stop, and serving anyway would mean a worker could drain an outbox against it.
	slog.Error("REFUSING TO SERVE: "+what+", and Phase 4 is NOT dark. A financial worker must not run "+
		"without knowing whether this database was restored.", "err", err)
	os.Exit(2)
	return ""
}

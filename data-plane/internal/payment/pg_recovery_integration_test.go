//go:build integration

package payment

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FINANCIAL_RECOVERY_MODE, exercised by simulating the event it exists for.
//
// A real restore cannot be performed inside a test, but the SIGNAL a restore produces can be reproduced
// exactly: the stored system identity stops matching the running one. Every assertion below drives the same
// code path a genuine restore would, through the same function the runtime calls at startup.
//
// The behaviour under test is mostly negative, and deliberately so. After a restore the safe system is the
// one that does LESS: no new charges, no outbox drain, no automatic replay of anything.

func recoverySite(t *testing.T, p *pgxpool.Pool) scope {
	t.Helper()
	s := seedPaidChain(t, p)
	// establish the initial epoch, as a first-ever startup would
	e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})
	got, err := e.ReconcileEpoch(context.Background(), s.tenant, s.site)
	if err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if got != "INITIALIZED" {
		t.Fatalf("first reconcile returned %s", got)
	}
	return s
}

// simulateRestore reproduces the signal a restore leaves behind: the recorded identity no longer matches
// the running database's own.
func simulateRestore(t *testing.T, p *pgxpool.Pool, s scope) {
	t.Helper()
	if _, err := p.Exec(context.Background(),
		`UPDATE iam_v2.financial_epochs SET system_identity = 'identity-from-a-different-cluster'
		  WHERE tenant_id=$1 AND site_id=$2`, s.tenant, s.site); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationRecovery_RestartIsIdempotentAndChangesNothing(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})

	for i := 0; i < 5; i++ {
		got, err := e.ReconcileEpoch(ctx, s.tenant, s.site)
		if err != nil {
			t.Fatalf("restart %d: %v", i, err)
		}
		if got != "UNCHANGED" {
			t.Fatalf("an ordinary restart reported %s; only a restore may change the epoch", got)
		}
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.financial_epochs
		WHERE tenant_id=$1 AND site_id=$2`, s.tenant, s.site); n != 1 {
		t.Fatalf("five restarts produced %d epochs", n)
	}
	active, err := e.RecoveryActive(ctx, s.tenant, s.site)
	if err != nil || active {
		t.Fatalf("a healthy site is in recovery (active=%v err=%v)", active, err)
	}
}

// The whole point: after a restore, nothing moves money and nothing replays.
func TestIntegrationRecovery_RestoreHoldsEverythingAndReplaysNothing(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})

	// a charge in flight when the backup was taken
	in, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err != nil {
		t.Fatal(err)
	}
	simulateRestore(t, p, s)

	got, err := e.ReconcileEpoch(ctx, s.tenant, s.site)
	if err != nil {
		t.Fatalf("reconcile after restore: %v", err)
	}
	if got != "RECOVERY_ENTERED" {
		t.Fatalf("a restore was not detected: %s", got)
	}
	if active, _ := e.RecoveryActive(ctx, s.tenant, s.site); !active {
		t.Fatal("recovery is not active after a detected restore")
	}

	// the in-flight charge and its settlement are HELD, with their state as it was
	holds, err := e.OpenHolds(ctx, s.tenant, s.site, 50)
	if err != nil {
		t.Fatal(err)
	}
	var sawPayment, sawSettlement bool
	for _, h := range holds {
		switch h.Kind {
		case "PAYMENT_TRANSACTION":
			sawPayment = h.WorkID == in.ID && h.HeldStatus == "CREATED"
		case "SETTLEMENT":
			sawSettlement = h.WorkID == s.settlement
		}
	}
	if !sawPayment || !sawSettlement {
		t.Fatalf("the in-flight work was not held: payment=%v settlement=%v (%d holds)",
			sawPayment, sawSettlement, len(holds))
	}

	// NOTHING may be started while held
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); CodeOf(err) != ErrRecoveryHeld {
		t.Fatalf("execution was permitted during recovery: %v", err)
	}
	if _, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "2")); CodeOf(err) != ErrRecoveryHeld {
		t.Fatalf("a new charge was admitted during recovery: %v", err)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.payment_transactions WHERE id=$1`, in.ID); st != "CREATED" {
		t.Fatalf("the held payment moved to %s during recovery", st)
	}

	// repeated startups while held do not open further epochs, and do not release anything
	for i := 0; i < 3; i++ {
		if got, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil || got != "RECOVERY_ACTIVE" {
			t.Fatalf("restart during recovery reported %s (%v)", got, err)
		}
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.financial_epochs
		WHERE tenant_id=$1 AND site_id=$2`, s.tenant, s.site); n != 2 {
		t.Fatalf("restarts during recovery produced %d epochs", n)
	}
}

// Guest access is independent. A restore is our problem, not the guest's.
func TestIntegrationRecovery_GuestAccessIsUnaffected(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), realGranter(t))

	// a guest who paid and was granted access before the backup
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if _, err := e.Execute(ctx, s.tenant, s.site, in.ID); err != nil {
		t.Fatal(err)
	}
	before := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.entitlements
		WHERE purchase_id=$1 AND status='ACTIVE'`, s.purchase)
	if before != 1 {
		t.Fatalf("setup: expected one active entitlement, got %d", before)
	}

	simulateRestore(t, p, s)
	if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
		t.Fatal(err)
	}

	// recovery holds MONEY, not access
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.entitlements
		WHERE purchase_id=$1 AND status='ACTIVE'`, s.purchase); n != before {
		t.Fatalf("entering recovery changed the guest's access: %d -> %d", before, n)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.financial_recovery_holds h
		WHERE h.tenant_id=$1 AND h.site_id=$2 AND h.work_kind NOT IN
		      ('POSTING_OUTBOX','PAYMENT_TRANSACTION','SETTLEMENT')`, s.tenant, s.site); n != 0 {
		t.Fatalf("recovery held %d non-financial items", n)
	}
}

// Release is earned, not requested.
func TestIntegrationRecovery_ReleaseRequiresEveryHoldReconciled(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	_ = in
	simulateRestore(t, p, s)
	if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
		t.Fatal(err)
	}
	actor := seedOperator(t, p, s.tenant)

	if _, err := e.ReleaseRecovery(ctx, s.tenant, s.site, actor, "everything looks fine to me"); CodeOf(err) != ErrRecoveryHeld {
		t.Fatalf("recovery was released with unresolved holds: %v", err)
	}

	holds, _ := e.OpenHolds(ctx, s.tenant, s.site, 50)
	if len(holds) == 0 {
		t.Fatal("no holds to reconcile")
	}
	// a decision needs an author and an account of how it was established
	if err := e.ResolveHold(ctx, holds[0].ID, "CONFIRMED_COMPLETED", actor, "short"); CodeOf(err) != ErrUntrustedInput {
		t.Fatalf("a decision without a real note was accepted: %v", err)
	}
	if err := e.ResolveHold(ctx, holds[0].ID, "INVENTED_OUTCOME", actor,
		"checked the provider dashboard directly"); err == nil {
		t.Fatal("an invented resolution was accepted")
	}
	for _, h := range holds {
		if err := e.ResolveHold(ctx, h.ID, "CONFIRMED_NOT_COMPLETED", actor,
			"checked the provider dashboard and the folio; nothing was posted"); err != nil {
			t.Fatalf("resolve %s: %v", h.ID, err)
		}
	}
	// a conclusion about money is not revised in place
	if err := e.ResolveHold(ctx, holds[0].ID, "CONFIRMED_COMPLETED", actor,
		"actually I changed my mind about this one"); CodeOf(err) != ErrNotExecutable {
		t.Fatalf("a resolved hold was rewritten: %v", err)
	}

	epoch, err := e.ReleaseRecovery(ctx, s.tenant, s.site, actor,
		"all held items reconciled against the provider dashboard and the PMS folio")
	if err != nil {
		t.Fatalf("release after full reconciliation: %v", err)
	}
	if epoch == 0 {
		t.Fatal("release returned no epoch")
	}
	if active, _ := e.RecoveryActive(ctx, s.tenant, s.site); active {
		t.Fatal("recovery is still active after release")
	}
	// and money moves again
	if _, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "3")); CodeOf(err) == ErrRecoveryHeld {
		t.Fatal("money movement is still held after release")
	}
}

// An operator may hold money movement without a restore, and repeating the declaration is a no-op.
func TestIntegrationRecovery_OperatorCanDeclareAndTheDeclarationIsIdempotent(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})
	actor := seedOperator(t, p, s.tenant)

	if _, err := e.DeclareRecovery(ctx, s.tenant, s.site, actor, "short"); CodeOf(err) != ErrUntrustedInput {
		t.Fatalf("a declaration without a reason was accepted: %v", err)
	}
	first, err := e.DeclareRecovery(ctx, s.tenant, s.site, actor,
		"provider dashboard disagrees with our settlement record")
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.DeclareRecovery(ctx, s.tenant, s.site, actor,
		"provider dashboard disagrees with our settlement record")
	if err != nil || again != first {
		t.Fatalf("a repeated declaration opened a second epoch: %d then %d (%v)", first, again, err)
	}
	if active, _ := e.RecoveryActive(ctx, s.tenant, s.site); !active {
		t.Fatal("an operator declaration did not hold money movement")
	}
	if _, err := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1")); CodeOf(err) != ErrRecoveryHeld {
		t.Fatalf("a charge was admitted during a declared hold: %v", err)
	}
}

// An outcome for work that was ALREADY in flight must still be recordable during recovery. Refusing it
// would leave a capture that really happened permanently unrecorded, which is worse than the disease.
func TestIntegrationRecovery_AnOutcomeForInFlightWorkIsStillRecorded(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	e := NewEngine(liveCfg, p, NewScriptedProvider(Result{Outcome: OutcomeCaptured}), &fakeGranter{})
	in, _ := e.CreateChargeIntent(ctx, s.tenant, s.site, s.settlement, idem(t, "1"))
	if err := beginExecution(t, p, in.ID); err != nil {
		t.Fatal(err)
	}
	simulateRestore(t, p, s)
	if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
		t.Fatal(err)
	}
	// the provider answers about the charge we sent BEFORE the restore
	if _, err := e.HandleProviderNotification(ctx,
		BuildNotification(in.ClientRef, "evt-inflight", OutcomeCaptured, "prv_"+in.ClientRef)); err != nil {
		t.Fatalf("an outcome for pre-restore work was refused: %v", err)
	}
	if st := scan1[string](t, p, `SELECT status FROM iam_v2.payment_transactions WHERE id=$1`, in.ID); st != "CAPTURED" {
		t.Fatalf("the pre-restore outcome was not recorded: %s", st)
	}
}

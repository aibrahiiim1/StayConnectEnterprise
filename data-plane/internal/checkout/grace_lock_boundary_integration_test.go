//go:build integration

package checkout

// THE LOCK HAD TO SURVIVE LOSING THE PRIVILEGE THAT USED TO TAKE IT.
//
// The Checkout Converter locks the site grace-config row so a departure and a concurrent grace-policy
// publication cannot interleave into a mixed snapshot. It used to do that with SELECT ... FOR UPDATE on the
// table, which PostgreSQL only permits with UPDATE privilege — and granting UPDATE to svc_pmsd collided with
// Gate-P's D32 invariant, which refuses to complete while any runtime role can mutate grace policy.
//
// iam_v2.p3_lock_grace_config takes the lock as its owner instead. The privilege question is settled elsewhere;
// what these tests establish is that the SERIALISATION is genuinely unchanged, because a boundary that quietly
// stopped locking would replace a loud privilege error with a silent race — a checkout reading half of one
// policy and half of the next, once in a while, under load.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A second transaction must WAIT for the lock the function took, and must proceed once it is released. Both
// halves matter: a function that never locked would fail the first, and one that took a lock nobody released
// would fail the second and deadlock every checkout after the first.
func TestIntegration_LockGraceConfigStillSerialisesAgainstPublication(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true})

	// Transaction A: the converter's path. Takes the row lock and holds it.
	txA, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()

	var cfgVersion int64
	if err := txA.QueryRow(ctx,
		`SELECT config_version FROM iam_v2.p3_lock_grace_config($1,$2)`, f.tenant, f.site).Scan(&cfgVersion); err != nil {
		t.Fatalf("p3_lock_grace_config did not return the locked row: %v", err)
	}

	// Transaction B: a publication contending for the same row.
	blocked := make(chan error, 1)
	go func() {
		txB, berr := p.Begin(context.Background())
		if berr != nil {
			blocked <- berr
			return
		}
		defer func() { _ = txB.Rollback(context.Background()) }()
		var v int64
		blocked <- txB.QueryRow(context.Background(),
			`SELECT config_version FROM iam_v2.site_checkout_grace_config
			  WHERE tenant_id=$1 AND site_id=$2 FOR UPDATE`, f.tenant, f.site).Scan(&v)
	}()

	select {
	case err := <-blocked:
		t.Fatalf("a concurrent FOR UPDATE completed (%v) while the converter held the lock: the function is "+
			"not locking, and a checkout can now read a policy that is being replaced underneath it", err)
	case <-time.After(700 * time.Millisecond):
		// Still waiting, which is the point.
	}

	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("the waiter failed after the lock was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never acquired the row after the holder committed — the lock is not being released " +
			"with the transaction, which would stall every checkout after the first")
	}
}

// The lock belongs to the CALLER's transaction, not to the function. A SECURITY DEFINER function gets no
// transaction of its own, and if that ever stopped being true the lock would be released the instant the
// function returned — leaving the converter to read the row it thought it had pinned.
func TestIntegration_LockGraceConfigLockOutlivesTheFunctionCall(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true})

	txA, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()

	var v int64
	if err := txA.QueryRow(ctx, `SELECT config_version FROM iam_v2.p3_lock_grace_config($1,$2)`,
		f.tenant, f.site).Scan(&v); err != nil {
		t.Fatalf("lock: %v", err)
	}
	// The function has returned. Do unrelated work in the same transaction, then prove the row is still held.
	if _, err := txA.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("interleaved statement: %v", err)
	}
	if !rowIsLocked(t, p, f.tenant, f.site) {
		t.Fatal("the row was not locked after the function returned: the lock is scoped to the function rather " +
			"than to the caller's transaction, so it protects nothing the converter goes on to do")
	}
}

// An unconfigured site returns no row rather than an error — the converter distinguishes "no policy" from
// "policy present but incomplete", and both take different paths. Collapsing them would silently move sites
// onto Emergency Grace or off it.
func TestIntegration_LockGraceConfigReturnsNoRowForAnUnconfiguredSite(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()

	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.p3_lock_grace_config(gen_random_uuid(), gen_random_uuid())`).
		Scan(&n); err != nil {
		t.Fatalf("unconfigured site must not raise: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no rows for an unconfigured site, got %d", n)
	}
}

// rowIsLocked answers without blocking: a separate connection tries to take the same lock with NOWAIT and
// reports whether it was refused.
func rowIsLocked(t *testing.T, p *pgxpool.Pool, tenant, site string) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("probe begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var v int64
	err = tx.QueryRow(ctx, `SELECT config_version FROM iam_v2.site_checkout_grace_config
		WHERE tenant_id=$1 AND site_id=$2 FOR UPDATE NOWAIT`, tenant, site).Scan(&v)
	return err != nil // refused == still locked by the other transaction
}

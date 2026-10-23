//go:build integration

package checkout

// The Checkout Converter locks the site grace-config row so a departure and a grace-policy publication cannot
// interleave into a mixed snapshot. A row lock needs UPDATE privilege, which Gate-P's D32 invariant forbids for
// svc_pmsd, so iam_v2.p3_lock_grace_config takes it as owner. These tests prove the serialisation is intact; a
// silent loss of locking would be a rare mixed-policy race instead of a loud privilege error.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A second transaction must wait for the lock and proceed once it is released: never locking fails the first
// half, never releasing deadlocks every later checkout.
func TestIntegration_LockGraceConfigStillSerialisesAgainstPublication(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedBase(t, p, seedOpts{configureTypedPolicy: true, pinGracePackage: true})

	// Transaction A is the converter's path and holds the row lock.
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

	// Transaction B is a publication contending for the same row.
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

// The lock belongs to the caller's transaction: a SECURITY DEFINER function has none of its own, and were that
// to change the lock would be released on return.
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
	if _, err := txA.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("interleaved statement: %v", err)
	}
	if !rowIsLocked(t, p, f.tenant, f.site) {
		t.Fatal("the row was not locked after the function returned: the lock is scoped to the function rather " +
			"than to the caller's transaction, so it protects nothing the converter goes on to do")
	}
}

// An unconfigured site returns no row, not an error: the converter treats "no policy" and "policy incomplete"
// differently, and collapsing them would silently move sites onto or off Emergency Grace.
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

// rowIsLocked probes with FOR UPDATE NOWAIT on a separate connection.
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
	return err != nil
}

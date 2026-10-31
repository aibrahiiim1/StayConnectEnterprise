//go:build integration

package writerguard

// Migration 0192: the janitor inside iam_v2.begin_controlled_operation (every guest sign-in, accounting and enforcement write calls it, about 140 times a second at
// 300 sign-ins a minute) deleted every row older than an hour with an unindexed DELETE. Concurrent callers collided on the same expiring rows and the
// lab saw 148 deadlocks in one run. These tests hold the new janitor to: never wait for or deadlock with another transaction, keep what is fresh, remove
// what is expired, bounded, and leave the authorisation decision exactly as it was.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rawExec runs a statement as the owner with triggers off: the scope table refuses every write that does not come through the controlled functions, and a
// test has to seed rows with a past opened_at, which no controlled function writes.
func rawExec(ctx context.Context, p *pgxpool.Pool, sql string, args ...any) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		ct, err := tx.Exec(ctx, sql, args...)
		n = ct.RowsAffected()
		return err
	})
	return n, err
}

func TestIntegration_JanitorNeverWaitsForAnotherTransactionsRows(t *testing.T) {
	owner := ownerPool(t)
	ctx := context.Background()
	// 300 rows that expired two hours ago, under a txid range no real transaction can produce.
	if _, err := rawExec(ctx, owner, `DELETE FROM iam_v2.controlled_operation_scope WHERE txid >= 9000000000000000`); err != nil {
		t.Fatal(err)
	}
	if _, err := rawExec(ctx, owner, `INSERT INTO iam_v2.controlled_operation_scope (txid, family, token, opened_at)
	    SELECT 9000000000000000 + g, 'stay', gen_random_uuid(), now() - interval '2 hours' FROM generate_series(1,300) g`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = rawExec(context.Background(), owner, `DELETE FROM iam_v2.controlled_operation_scope WHERE txid >= 9000000000000000`)
	})

	// Another transaction is in the middle of deleting those very rows (the old janitor of another caller) and stays open.
	holder, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `DELETE FROM iam_v2.controlled_operation_scope WHERE txid >= 9000000000000000`); err != nil {
		t.Fatal(err)
	}

	// A caller with a short lock timeout must still get its scope at once: it skips what is held, it does not wait.
	conn, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET lock_timeout = '400ms'`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var open bool
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
			return err
		}
		// The decision the guard makes is unchanged: this transaction's own scope is open, and a family it never opened is not.
		if err := tx.QueryRow(ctx, `SELECT iam_v2.p3_controlled_operation_open('stay')`).Scan(&open); err != nil {
			return err
		}
		var other bool
		if err := tx.QueryRow(ctx, `SELECT iam_v2.p3_controlled_operation_open('alert')`).Scan(&other); err != nil {
			return err
		}
		if other {
			t.Error("a family that was never opened reports open")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("begin_controlled_operation waited for or collided with another transaction's rows: %v", err)
	}
	if !open {
		t.Fatal("the scope the call just opened is not open")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatalf("the call took %v while another transaction held the expired rows", time.Since(start))
	}
	// An unknown family is still refused.
	if _, err := conn.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('not_a_family')`); err == nil {
		t.Fatal("a family outside the allowlist was accepted")
	}
}

func TestIntegration_JanitorKeepsFreshRowsRemovesExpiredOnesInBoundedBatches(t *testing.T) {
	owner := ownerPool(t)
	ctx := context.Background()
	const base = 9100000000000000
	// Everything happens inside ONE transaction that is rolled back. The rows this test seeds are therefore invisible to every other
	// transaction, so a writer in another package (the gates run packages in parallel against one database) cannot run the janitor
	// over them and make the count wrong; and the call under test sees them, because it runs in the same transaction.
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.controlled_operation_scope (txid, family, token, opened_at)
	    SELECT $1::bigint + g, 'stay', gen_random_uuid(), now() - interval '3 hours' FROM generate_series(1,250) g`, int64(base)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.controlled_operation_scope (txid, family, token, opened_at)
	    SELECT $1::bigint + 1000 + g, 'stay', gen_random_uuid(), now() - interval '10 minutes' FROM generate_series(1,50) g`, int64(base)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = origin`); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := tx.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	expired := func() int {
		return count(`SELECT count(*) FROM iam_v2.controlled_operation_scope WHERE txid >= 9100000000000000 AND opened_at < now() - interval '1 hour'`)
	}
	fresh := func() int {
		return count(`SELECT count(*) FROM iam_v2.controlled_operation_scope WHERE txid >= 9100000000000000 AND opened_at >= now() - interval '1 hour'`)
	}
	call := func() {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
			t.Fatal(err)
		}
	}
	call()
	if got := expired(); got != 150 {
		t.Fatalf("after one call %d expired rows remain, want 150 (a batch of 100 removed)", got)
	}
	call()
	call()
	if got := expired(); got != 0 {
		t.Fatalf("after three calls %d expired rows remain, want 0", got)
	}
	if got := fresh(); got != 50 {
		t.Fatalf("%d fresh rows remain, want all 50: the janitor removed a row that is not expired", got)
	}
}

// Many writers at once while rows are expiring: no error of any kind (a deadlock or a lock timeout would surface here), every one gets its own scope.
func TestIntegration_ManyWritersWhileRowsExpireNeverDeadlock(t *testing.T) {
	owner := ownerPool(t)
	ctx := context.Background()
	_, _ = rawExec(ctx, owner, `DELETE FROM iam_v2.controlled_operation_scope WHERE txid >= 9000000000000000`)
	if _, err := rawExec(ctx, owner, `INSERT INTO iam_v2.controlled_operation_scope (txid, family, token, opened_at)
	    SELECT 9200000000000000 + g, 'stay', gen_random_uuid(), now() - interval '2 hours' FROM generate_series(1,5000) g`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = rawExec(context.Background(), owner, `DELETE FROM iam_v2.controlled_operation_scope WHERE txid >= 9000000000000000`)
	})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for w := 0; w < 24; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				err := pgx.BeginFunc(ctx, owner, func(tx pgx.Tx) error {
					// The caller's own other locks are held across the call, as a sign-in holds its entitlement row.
					if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
						return err
					}
					var open bool
					if err := tx.QueryRow(ctx, `SELECT iam_v2.p3_controlled_operation_open('stay')`).Scan(&open); err != nil {
						return err
					}
					if !open {
						return errNotOpen
					}
					return nil
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a writer failed: %v", err)
	}
}

type notOpen string

func (e notOpen) Error() string { return string(e) }

const errNotOpen = notOpen("the scope a call just opened is not open")

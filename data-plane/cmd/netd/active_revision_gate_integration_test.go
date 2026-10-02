//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ONLY AN APPLIED, HEALTH-CHECKED CONFIGURATION CAN BECOME THE ACTIVE ONE.
//
// This is the behavioural half of active_revision_provenance_test.go, against real DDL and the real constraint.
// `pending_confirmation` is reachable only through applier.Apply, which renders the bundle, puts it on the wire
// and passes every health check before MarkPending -- so refusing every other state is what makes "active" mean
// "this appliance applied this and kept it", which is the claim netd's boot reconcile relies on.

func gateTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping the active-revision gate integration")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestIntegration_OnlyAPendingRevisionCanBecomeActive(t *testing.T) {
	db := gateTestPool(t)
	ctx := context.Background()
	st := &store{db: db}

	// Nothing else may hold the single in-flight slot while this test uses it.
	if _, err := db.Exec(ctx, `UPDATE network_config_revisions SET state='superseded'
		 WHERE state IN ('applying','pending_confirmation')`); err != nil {
		t.Fatal(err)
	}

	newRevision := func(state string) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, `INSERT INTO network_config_revisions (state, summary)
			VALUES ($1,'active-revision gate test') RETURNING id::text`, state).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// EVERY state that is not pending_confirmation is refused. 'draft' and 'validated' are the ones that matter
	// most: a revision in either has never been applied, and the retired adopt route created exactly that and
	// then declared it active.
	for _, state := range []string{"draft", "validated", "failed", "rolled_back", "superseded", "active"} {
		id := newRevision(state)
		err := st.MarkActive(ctx, id, "")
		if !errors.Is(err, errNotPending) {
			t.Errorf("a %s revision was activated (err=%v); only an applied, health-checked one may be", state, err)
		}
		var after string
		if qerr := db.QueryRow(ctx, `SELECT state FROM network_config_revisions WHERE id=$1`, id).Scan(&after); qerr != nil {
			t.Fatal(qerr)
		}
		if after != state {
			t.Errorf("a refused activation changed the %s revision to %s", state, after)
		}
	}

	// 'applying' is refused too: the bundle is on the wire but the health checks have not been passed yet, so
	// there is nothing anybody has confirmed. It is kept separate because it is the only non-terminal state the
	// retired route produced, and because it holds the in-flight slot.
	applying := newRevision("applying")
	if err := st.MarkActive(ctx, applying, ""); !errors.Is(err, errNotPending) {
		t.Errorf("a revision still mid-apply was activated (err=%v)", err)
	}
	if _, err := db.Exec(ctx, `UPDATE network_config_revisions SET state='superseded' WHERE id=$1`, applying); err != nil {
		t.Fatal(err)
	}

	// ...and the one state that IS allowed still works, so the gate did not simply break confirmation.
	pending := newRevision("pending_confirmation")
	if err := st.MarkActive(ctx, pending, ""); err != nil {
		t.Fatalf("a pending revision could not be confirmed: %v", err)
	}
	var after string
	if err := db.QueryRow(ctx, `SELECT state FROM network_config_revisions WHERE id=$1`, pending).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != "active" {
		t.Fatalf("a confirmed revision is %s, not active", after)
	}
	// Exactly one active revision survives, which is what boot reconcile reads.
	var actives int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM network_config_revisions WHERE state='active'`).Scan(&actives); err != nil {
		t.Fatal(err)
	}
	if actives != 1 {
		t.Fatalf("%d active revisions after a confirm; the predecessor must have been superseded", actives)
	}
}

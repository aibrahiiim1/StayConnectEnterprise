//go:build integration

package main

import (
	"context"
	"testing"
	"time"
)

// A sign-in check (issue_or_return_pms_context) running while a grant on the same stay holds the interface runtime row must wait for
// it, never deadlock: both take the runtime row before the Stay (0214). The grant's two locks are taken here exactly as authctx
// ConsumeTx takes them.
func TestIntegration_ASignInCheckWaitsForAGrantOnTheSameStayInsteadOfDeadlocking(t *testing.T) {
	f := newAuthFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	device := mustDevice(t, f)

	grant, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = grant.Rollback(ctx) }()
	if _, err := grant.Exec(ctx, `SELECT iam_v2.lock_pms_interface_runtime($1,$2,$3::uuid)`, f.tenant, f.site, f.iface); err != nil {
		t.Fatal(err)
	}

	issued := make(chan error, 1)
	go func() {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			issued <- err
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var id string
		if err := tx.QueryRow(ctx, `SELECT context_id::text FROM iam_v2.issue_or_return_pms_context(
			$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,gen_random_uuid(),60)`,
			f.tenant, f.site, f.iface, f.revision, f.stay, device, f.network).Scan(&id); err != nil {
			issued <- err
			return
		}
		issued <- tx.Commit(ctx)
	}()
	time.Sleep(500 * time.Millisecond)

	if _, err := grant.Exec(ctx, `SELECT iam_v2.lock_stay($1,$2,$3::uuid)`, f.tenant, f.site, f.stay); err != nil {
		t.Fatalf("the grant could not take the stay after the runtime row: %v", err)
	}
	if err := grant.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-issued; err != nil {
		t.Fatalf("the sign-in check failed instead of waiting for the grant: %v", err)
	}
}

package api

import (
	"net/http"
	"sync"
	"testing"
)

// Audit Phase 1 L1: two concurrent removals of the only two platform administrators cannot both pass.
func TestLastAdministratorGuardIsAtomic(t *testing.T) {
	f := newFixture(t)
	// The fixture's owner is the acting session; two more platform admins are the targets, and the owner is
	// disabled so those two are the only active administrators.
	ids := make([]string, 2)
	for i, email := range []string{"a1@central.test", "a2@central.test"} {
		if err := f.pool.QueryRow(f.ctx, `INSERT INTO operators (email, password_hash, status) VALUES ($1,'x','active') RETURNING id::text`, email).
			Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'platform_admin')`, ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE operators SET status='disabled' WHERE id=$1`, f.session.OperatorID); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i := range ids {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = f.operatorCall(f.base.patchTeam, http.MethodPatch, ids[i], map[string]any{"status": "disabled"}).Code
			}(i)
		}
		wg.Wait()
		if n := f.count(`SELECT count(*) FROM operators WHERE id = ANY($1::uuid[]) AND status='active'`, ids); n != 1 {
			t.Fatalf("round %d: exactly one platform administrator must stay active, got %d (codes %v)", round, n, codes)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE operators SET status='active' WHERE id = ANY($1::uuid[])`, ids); err != nil {
			t.Fatal(err)
		}
	}
}

//go:build integration

package pmsd

import (
	"context"
	"testing"
)

// A DRAINING interface is listed for a worker only while it has room charges to finish; an ACTIVE one always is.
func TestIntegration_DrainingWithoutQueuedChargesIsNotListed(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	if _, err := pool.Exec(context.Background(), `UPDATE iam_v2.pms_interfaces SET lifecycle_state='DRAINING' WHERE id=$1`, s.iface); err != nil {
		t.Fatalf("wind down: %v", err)
	}
	if n := scalarInt(t, pool, `SELECT iam_v2.pms_interface_has_undrained_postings($1,$2,$3)::int`, s.tenant, s.site, s.iface); n != 0 {
		t.Fatalf("an interface with no postings reports undrained work")
	}
	got, err := repo.ListActiveInterfaces(context.Background(), s.tenant, s.site)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range got {
		if i.ID == s.iface {
			t.Fatal("a DRAINING interface with nothing to send was listed for a worker")
		}
	}
}

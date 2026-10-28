//go:build integration

package staygrant

import (
	"context"
	"testing"
	"time"
)

// P4 L15: the activation instant is the database clock of the granting transaction, the clock its eligibility checks
// use, not the Go process clock.
func TestIntegration_ActivationUsesTheDatabaseClock(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seedIncluded(t, p)
	acID := issue(t, p, f)
	r, err := New(p).Grant(ctx, f.tenant, f.site, Request{AuthContextID: acID, Presenter: presenter(f), PackageRevID: f.pkgRev})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	var effective, recorded time.Time
	if err := p.QueryRow(ctx, `SELECT effective_at, recorded_at FROM iam_v2.entitlement_state_transitions
		WHERE entitlement_id=$1 AND seq=1`, r.EntitlementID).Scan(&effective, &recorded); err != nil {
		t.Fatal(err)
	}
	if !effective.Equal(recorded) {
		t.Fatalf("activation %v is not the transaction's database time %v", effective, recorded)
	}
}

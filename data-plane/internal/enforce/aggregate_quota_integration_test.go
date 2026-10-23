//go:build integration

package enforce

// The entitlement owns the quota, not the session: every session under it spends one allowance, while
// per-session detail still survives.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// secondSession attaches another device (own device and address) to an existing entitlement.
func secondSession(t *testing.T, p *pgxpool.Pool, f fixture, ent, ip, mac string, at time.Time) string {
	t.Helper()
	ctx := context.Background()
	var dev string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),($3::text)::macaddr) RETURNING id::text`,
		f.tenant, f.site, mac).Scan(&dev); err != nil {
		t.Fatalf("second device: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,$3)`, ent, dev, at); err != nil {
		t.Fatalf("authorize second device: %v", err)
	}
	var sess string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,state,started,ip,mac)
		VALUES ($1,$2,$3,$4,'active',$5,($6::text)::inet,($7::text)::macaddr) RETURNING id::text`,
		f.tenant, f.site, ent, dev, at, ip, mac).Scan(&sess); err != nil {
		t.Fatalf("second session: %v", err)
	}
	return sess
}

// meter writes through the table ingest writes, so the asserted sum is the one the quota rule reads.
func meter(t *testing.T, p *pgxpool.Pool, f fixture, sess string, seq int, up, down int64, at time.Time) {
	t.Helper()
	// sample_seq is part of the record's identity, for checkpoint reconciliation.
	if err := ownerExec(t, p, `INSERT INTO iam_v2.accounting_records
		(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down,sampled_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, f.tenant, f.site, sess, seq, up, down, at); err != nil {
		t.Fatalf("meter %s: %v", sess, err)
	}
}

// Neither session alone crosses the quota; together they do. A per-session rule would double the allowance.
func TestIntegration_AggregateQuota_TwoSessionsShareOneAllowance(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	const quota = 100_000_000
	f := seed(t, p, 8000, 3000, quota)
	at := time.Now().Add(-time.Hour)
	ent, first := grant(t, p, f, nil, at)
	second := secondSession(t, p, f, ent, "10.20.30.41", "02:00:00:00:30:02", at)

	// 60 MB on the phone: under the allowance on its own.
	meter(t, p, f, first, 1, 10_000_000, 50_000_000, at.Add(time.Minute))
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("access ended at 60MB of a %d-byte allowance: %+v", quota, due)
	}

	// 50 MB more on the laptop. Neither session crossed it; the ACCOUNT has.
	meter(t, p, f, second, 1, 10_000_000, 40_000_000, at.Add(2*time.Minute))
	due, err = New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("the aggregate crossing did not end access: %+v — a second device must not buy a second "+
			"allowance", due)
	}
	if due[0].EntitlementID != ent || due[0].Reason != "DATA" {
		t.Fatalf("expiry = %+v, want the entitlement ended for DATA", due[0])
	}

	// BOTH sessions end, because both were access under the entitlement that ended.
	var live int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sessions
		WHERE entitlement_id=$1 AND ended IS NULL`, ent).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d session(s) still live after the entitlement ended", live)
	}
}

// Usage is the sum, and per-session detail survives.
func TestIntegration_AggregateQuota_UsageSumsAcrossSessionsWithoutLosingDetail(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 8000, 3000, 0) // no quota: nothing may end, only be counted
	at := time.Now().Add(-time.Hour)
	ent, first := grant(t, p, f, nil, at)
	second := secondSession(t, p, f, ent, "10.20.30.42", "02:00:00:00:30:03", at)

	meter(t, p, f, first, 1, 1_000, 2_000, at.Add(time.Minute))
	meter(t, p, f, second, 1, 3_000, 4_000, at.Add(2*time.Minute))

	var up, down int64
	if err := p.QueryRow(ctx,
		`SELECT bytes_up, bytes_down FROM iam_v2.entitlement_usage_bytes($1, now())`, ent).Scan(&up, &down); err != nil {
		t.Fatalf("entitlement usage: %v", err)
	}
	if up != 4_000 || down != 6_000 {
		t.Fatalf("entitlement usage = %d up / %d down, want the sum across both sessions (4000/6000)", up, down)
	}

	// ...and the rows are still attributable to the session that produced them.
	var perSession int
	if err := p.QueryRow(ctx, `SELECT count(DISTINCT session_id) FROM iam_v2.accounting_records
		WHERE session_id IN ($1,$2)`, first, second).Scan(&perSession); err != nil {
		t.Fatal(err)
	}
	if perSession != 2 {
		t.Fatalf("per-session accounting detail was lost: %d distinct sessions", perSession)
	}
}

// A device changing address retires the old attachment but keeps the entitlement and its usage.
func TestIntegration_AggregateQuota_MovingDeviceKeepsItsUsage(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 8000, 3000, 100_000_000)
	at := time.Now().Add(-time.Hour)
	ent, old := grant(t, p, f, nil, at)
	meter(t, p, f, old, 1, 5_000_000, 20_000_000, at.Add(time.Minute))

	// The same device returns at a new address, under the SAME entitlement — no new purchase.
	var dev string
	if err := p.QueryRow(ctx, `SELECT device_id::text FROM iam_v2.sessions WHERE id=$1`, old).Scan(&dev); err != nil {
		t.Fatal(err)
	}
	var moved string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id,site_id,entitlement_id,device_id,state,started,ip,mac)
		SELECT $1,$2,$3,$4,'active',$5,'10.20.30.99'::inet, mac FROM iam_v2.devices WHERE id=$4
		RETURNING id::text`, f.tenant, f.site, ent, dev, at.Add(30*time.Minute)).Scan(&moved); err != nil {
		t.Fatalf("moved session: %v", err)
	}

	plan, err := New(p).PlanForSite(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Shape) != 1 || plan.Shape[0].SessionID != moved {
		t.Fatalf("the current attachment is not the only one enforced: %+v", plan.Shape)
	}
	var retired bool
	for _, s := range plan.Tear {
		if s.SessionID == old && s.EndReason == EndReasonDeviceMoved {
			retired = true
		}
	}
	if !retired {
		t.Fatalf("the abandoned attachment was not retired: %+v", plan.Tear)
	}

	// The account is unchanged: one purchase, one entitlement, and the usage it had before the move.
	var purchases int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`, f.stay).Scan(&purchases); err != nil {
		t.Fatal(err)
	}
	if purchases != 1 {
		t.Fatalf("purchases = %d after a device moved address, want 1", purchases)
	}
	var down int64
	if err := p.QueryRow(ctx,
		`SELECT bytes_down FROM iam_v2.entitlement_usage_bytes($1, now())`, ent).Scan(&down); err != nil {
		t.Fatal(err)
	}
	if down != 20_000_000 {
		t.Fatalf("usage = %d after the move, want the 20000000 it had before — a move must not reset it", down)
	}
}

//go:build integration

package enforce

// A rollup window that cannot be read inside acctd's statement_timeout must not repeat forever (PR #272 review). The
// role's 30 s is scaled down here (a few hundred milliseconds against a few hundred thousand rows) so the test is quick;
// the mechanism is identical: the statement is cancelled, the transaction rolls back, the watermark stays, and an
// unchanged window would be cancelled again. The Enforcer must shrink the window until it fits, and in the end the
// per-minute and per-hour totals must equal the raw samples exactly.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func timeoutPool(t *testing.T, role, statementTimeout string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		if _, err := c.Exec(ctx, "SET ROLE "+role); err != nil {
			return err
		}
		_, err := c.Exec(ctx, "SET statement_timeout = '"+statementTimeout+"'")
		return err
	}
	rp, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rp.Close)
	return rp
}

func TestIntegration_UsageRollup_APoisonWindowShrinksUntilItFits(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	var tenant, site string
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&tenant, &site); err != nil {
		t.Fatal(err)
	}
	const rows = 200000
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	// 200,000 samples over two hours (28/s) from 5 h ago, 1,500 sessions: one window of two hours holds them all.
	if _, err := tx.Exec(ctx, `
	  INSERT INTO iam_v2.accounting_records (tenant_id, site_id, session_id, sample_seq, bytes_up, bytes_down, sampled_at, ingested_at)
	  SELECT $1::uuid, $2::uuid, md5((g % 1500)::text || $2)::uuid, g, (g % 1000) + 1, (g % 5000) + 1,
	         now() - interval '5 hours' + make_interval(secs => 7200.0 * g / $3) - interval '2 seconds',
	         now() - interval '5 hours' + make_interval(secs => 7200.0 * g / $3)
	    FROM generate_series(1, $3::int) g`, tenant, site, rows); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// A tight statement_timeout stands in for the role's 30 s: the full two-hour window cannot finish inside it.
	e := New(timeoutPool(t, "svc_acctd", "300ms"))
	saved := rollupQuick
	rollupQuick = 100 * time.Millisecond // scaled: a doubled window must still finish inside 300 ms
	t.Cleanup(func() { rollupQuick = saved })
	var rolled int64
	var failures int
	for i := 0; i < 300 && rolled < rows; i++ {
		n, err := e.RollupAccountingUsage(ctx, tenant, site)
		if err != nil {
			failures++
			if !isStatementTimeout(err) {
				t.Fatalf("rollup: %v", err)
			}
			continue
		}
		rolled += n
	}
	if rolled != rows {
		t.Fatalf("rolled up %d of %d samples in 300 calls (%d cancelled): the window never shrank to one that fits", rolled, rows, failures)
	}
	if failures == 0 {
		t.Skip("the two-hour window fit inside the scaled timeout on this machine; nothing was proven")
	}
	var rawUp, rawDown, minUp, minDown, hourUp, hourDown, minN, hourN int64
	if err := p.QueryRow(ctx, `SELECT sum(bytes_up), sum(bytes_down) FROM iam_v2.accounting_records WHERE site_id=$1`, site).Scan(&rawUp, &rawDown); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT sum(bytes_up), sum(bytes_down), sum(samples) FROM iam_v2.accounting_usage_minute WHERE site_id=$1`, site).Scan(&minUp, &minDown, &minN); err != nil {
		t.Fatal(err)
	}
	if err := p.QueryRow(ctx, `SELECT sum(bytes_up), sum(bytes_down), sum(samples) FROM iam_v2.accounting_usage_hour WHERE site_id=$1`, site).Scan(&hourUp, &hourDown, &hourN); err != nil {
		t.Fatal(err)
	}
	if minUp != rawUp || minDown != rawDown || hourUp != rawUp || hourDown != rawDown || minN != rows || hourN != rows {
		t.Fatalf("totals differ after %d cancelled windows: raw %d/%d, minute %d/%d (%d), hour %d/%d (%d)",
			failures, rawUp, rawDown, minUp, minDown, minN, hourUp, hourDown, hourN)
	}
	t.Logf("%d cancelled statements, then %d samples rolled up exactly", failures, rolled)
}

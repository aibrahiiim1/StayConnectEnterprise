//go:build integration

package enforce

// Migration 0181: accounting integrity closure. Real PostgreSQL, the real ingestion operation, the real roll-up, purge and
// expiry sweep. The scenarios the PO named: interrupted writes, replays, retroactive bindings, reboots (epoch changes), long
// sessions spanning many purges, transfers, and termination when part of the usage lives only in the folded table.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// aiAge makes every raw sample older than three hours look ingested two seconds after it was taken, as in production.
func aiAge(t *testing.T, p *pgxpool.Pool, f fixture) {
	t.Helper()
	uaSuper(t, p, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.accounting_records SET ingested_at = sampled_at + interval '2 seconds'
			WHERE site_id=$1 AND sampled_at < now() - interval '3 hours'`, f.site)
		return err
	})
}

func aiMaxSeq(t *testing.T, p *pgxpool.Pool, sess string) int64 {
	t.Helper()
	return scalar[int64](t, p, `SELECT COALESCE(max(sample_seq),0) FROM iam_v2.accounting_records WHERE session_id=$1`, sess)
}

func aiEvents(t *testing.T, p *pgxpool.Pool, f fixture, kind string) int {
	t.Helper()
	return count(t, p, `SELECT count(*) FROM iam_v2.accounting_integrity_events WHERE site_id=$1 AND kind=$2`, f.site, kind)
}

func aiTotal(t *testing.T, p *pgxpool.Pool, ent string) int64 {
	t.Helper()
	u := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", ent, time.Now().Add(time.Minute))
	return u.up + u.down
}

// ---------------------------------------------------------------------------------------------------------
// 1. THE QUOTA HOLE. A session whose samples were all purged (idle past raw_retention_days) restarted its sample_seq at 1, below the
//    crossing checkpoint, so its new traffic was never counted toward the data quota. Real ingestion throughout.
// ---------------------------------------------------------------------------------------------------------
func TestIntegration_AccountingIntegrity_AnIdleSessionPastTheRetentionStillCrossesItsQuota(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	const quota = 10_000_000
	f := quotaSeed(t, p, int64(quota), "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-5 * 24 * time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-ai1")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 1_500_000, 1_500_000, at.Add(time.Hour))     // +3 MB
	submit(t, p, f, m, 1, 3_000_000, 3_000_000, at.Add(2*time.Hour))   // +3 MB = 6 MB, under the 10 MB quota
	if due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil || len(due) != 0 {
		t.Fatalf("under the quota: %v %v", due, err)
	}
	examined := scalar[int64](t, p, `SELECT max(examined_seq) FROM iam_v2.data_crossing_progress WHERE entitlement_id=$1`, ent)
	if examined != 2 {
		t.Fatalf("fixture: examined_seq = %d, want 2", examined)
	}

	// three idle days: every raw sample of the session is rolled up and purged
	aiAge(t, p, f)
	uaRetention(t, p, f, 1)
	if left := uaRollAndPurge(t, p, f, 500); left != 0 {
		t.Fatalf("%d raw samples left; the whole history should have been folded", left)
	}
	if got := aiTotal(t, p, ent); got != 6_000_000 {
		t.Fatalf("usage after the purge = %d, want 6000000 (folded)", got)
	}

	// the guest comes back: +3 MB (9 MB, still under), then +3 MB (12 MB, over)
	submit(t, p, f, m, 1, 4_500_000, 4_500_000, now.Add(-time.Hour))
	if s := aiMaxSeq(t, p, sess); s <= examined {
		t.Fatalf("the new sample got sample_seq %d, at or below the crossing checkpoint %d: the quota can no longer be reached", s, examined)
	}
	if due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil || len(due) != 0 {
		t.Fatalf("9 MB of 10 MB: %v %v", due, err)
	}
	crossing := now.Add(-30 * time.Minute)
	submit(t, p, f, m, 1, 6_000_000, 6_000_000, crossing)
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" {
		t.Fatalf("expiry = %+v, want one DATA termination: the idle session passed its quota", due)
	}
	if due[0].At.Sub(crossing).Abs() > time.Millisecond {
		t.Fatalf("ended at %s, want the crossing sample %s", due[0].At, crossing)
	}
	if s := status(t, p, ent); s != "TERMINATED" {
		t.Fatalf("entitlement is %s", s)
	}
	// the maintained counter and the derivation (folded + raw) agree to the byte
	agree(t, p, ent, 12_000_000)
	// (entitlement_usage_bytes attributes by the half-open binding interval, so the sample that ends the binding at the
	// termination instant is the closing-instant arm of p3_entitlement_data_usage, not of usage_bytes: 12 MB less that sample.)
	if got := aiTotal(t, p, ent); got != 9_000_000 {
		t.Fatalf("usage_bytes = %d, want 9000000 (folded 6 MB + the 3 MB sample before the closing one)", got)
	}
}

// The same hole for sessions the defect had ALREADY hit when 0181 was applied: the migration's reconcile lifts the checkpoint.
func TestIntegration_AccountingIntegrity_AnUnderstatedCheckpointIsLiftedNeverLowered(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(50_000), "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-5 * 24 * time.Hour)
	ent, sess := grant(t, p, f, nil, base)
	var ts []time.Time
	var up, down []int64
	for i := 0; i < 48; i++ {
		ts = append(ts, base.Add(time.Duration(i)*time.Hour+time.Minute))
		up, down = append(up, 0), append(down, 1_000)
	}
	uaLoad(t, p, f, sess, 1, ts, up, down) // 48 KB: just under the quota
	// the state the defect left: examined_seq at 48 but nothing counted
	bid := scalar[string](t, p, `SELECT id::text FROM iam_v2.session_entitlement_bindings WHERE session_id=$1`, sess)
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.data_crossing_progress (binding_id,tenant_id,site_id,entitlement_id,session_id,examined_seq,counted_bytes)
		VALUES ($1,$2,$3,$4,$5,49,1000)`, bid, f.tenant, f.site, ent, sess); err != nil {
		t.Fatal(err)
	}
	if lifted := scalar[int64](t, p, `SELECT iam_v2.p6_reconcile_crossing_checkpoint($1)`, ent); lifted != 47_000 {
		t.Fatalf("lifted %d, want 47000", lifted)
	}
	if c := scalar[int64](t, p, `SELECT sum(counted_bytes) FROM iam_v2.data_crossing_progress WHERE entitlement_id=$1`, ent); c != 48_000 {
		t.Fatalf("checkpoint = %d, want 48000", c)
	}
	if again := scalar[int64](t, p, `SELECT iam_v2.p6_reconcile_crossing_checkpoint($1)`, ent); again != 0 {
		t.Fatalf("a second reconcile lifted %d more", again)
	}
	if n := aiEvents(t, p, f, "CHECKPOINT_BELOW_EXACT"); n != 1 {
		t.Fatalf("%d CHECKPOINT_BELOW_EXACT events, want 1", n)
	}
	// an overstated checkpoint is NEVER lowered by the reconcile (the exact derivation does that, on its own path)
	if _, err := p.Exec(ctx, `UPDATE iam_v2.data_crossing_progress SET counted_bytes=900000 WHERE entitlement_id=$1`, ent); err != nil {
		t.Fatal(err)
	}
	if v := scalar[int64](t, p, `SELECT iam_v2.p6_reconcile_crossing_checkpoint($1)`, ent); v != 0 {
		t.Fatalf("an overstated checkpoint was changed by %d", v)
	}
}

// ---------------------------------------------------------------------------------------------------------
// 2. Replays, interrupted writes and reboots, after the history has been folded.
// ---------------------------------------------------------------------------------------------------------
func TestIntegration_AccountingIntegrity_ReplaysAndRebootsAfterThePurgeCountEachByteOnce(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-4 * 24 * time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-ai2")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 1_000, 2_000, at.Add(time.Hour))
	submit(t, p, f, m, 1, 5_000, 9_000, at.Add(2*time.Hour)) // 14,000 so far
	aiAge(t, p, f)
	uaRetention(t, p, f, 1)
	if left := uaRollAndPurge(t, p, f, 500); left != 0 {
		t.Fatalf("%d raw samples left", left)
	}
	agree(t, p, ent, 14_000)

	// a retry of the last accepted reading, long after its sample was purged: nothing new
	if got := submit(t, p, f, m, 1, 5_000, 9_000, at.Add(2*time.Hour)); got != "REPLAY:ACCEPTED" && got != "DUPLICATE" {
		t.Fatalf("replay answered %q", got)
	}
	agree(t, p, ent, 14_000)
	// a reading older than the checkpoint, or counters going backwards without a new epoch, are refused and count nothing
	if err := submitErr(t, p, f, m, 1, 1_000, 2_000, at.Add(time.Hour)); err == nil {
		t.Fatal("a stale reading was accepted")
	}
	if err := submitErr(t, p, f, m, 1, 4_000, 8_000, now.Add(-3*time.Hour)); err == nil {
		t.Fatal("a counter regression without a new epoch was accepted")
	}
	agree(t, p, ent, 14_000)

	// a reboot: the class is replaced (new epoch). The new absolutes are the baseline; only bytes after it count.
	if got := submit(t, p, f, m, 2, 300, 700, now.Add(-2*time.Hour)); got != "RESET_BASELINED" {
		t.Fatalf("epoch change answered %q", got)
	}
	agree(t, p, ent, 14_000)
	submit(t, p, f, m, 2, 1_300, 2_700, now.Add(-time.Hour)) // +3,000
	agree(t, p, ent, 17_000)
	// replays of the new epoch's reading, and of the old epoch's, change nothing
	submit(t, p, f, m, 2, 1_300, 2_700, now.Add(-time.Hour))
	if err := submitErr(t, p, f, m, 1, 9_000, 9_000, now.Add(-30*time.Minute)); err == nil {
		t.Fatal("a reading from the superseded epoch was accepted")
	}
	agree(t, p, ent, 17_000)
	if s := aiMaxSeq(t, p, sess); s != 3 {
		t.Fatalf("sample_seq %d: the sequence restarted", s)
	}
	// a purge repeated and one with a tiny batch change nothing either
	aiAge(t, p, f)
	uaRollAndPurge(t, p, f, 1)
	uaRollAndPurge(t, p, f, 1)
	if got := aiTotal(t, p, ent); got != 17_000 {
		t.Fatalf("usage_bytes = %d after repeated purges, want 17000", got)
	}
}

// ---------------------------------------------------------------------------------------------------------
// 3. A long session that is transferred between entitlements, with samples arriving over many purge rounds.
// ---------------------------------------------------------------------------------------------------------
func TestIntegration_AccountingIntegrity_ALongTransferredSessionReadsTheSameAfterEveryPurgeRound(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-7 * 24 * time.Hour).Truncate(time.Hour)
	e1, sess := grant(t, p, f, nil, base)
	e2 := uaExtraEntitlement(t, p, f, base, 1)
	boundary := base.Add(3*24*time.Hour + 17*time.Minute)
	uaSetBindings(t, p, f, sess, []uaBinding{{e1, base, &boundary}, {e2, boundary, nil}}, e2)
	uaRetention(t, p, f, 1)

	var want1, want2 int64
	seq := int64(1)
	for round := 0; round < 5; round++ {
		var ts []time.Time
		var up, down []int64
		for h := 0; h < 24; h++ {
			s := base.Add(time.Duration(round*24+h)*time.Hour + 20*time.Minute)
			ts, up, down = append(ts, s), append(up, int64(10+h)), append(down, int64(1_000+round*7+h))
			if s.Before(boundary) {
				want1 += up[len(up)-1] + down[len(down)-1]
			} else {
				want2 += up[len(up)-1] + down[len(down)-1]
			}
		}
		uaLoad(t, p, f, sess, seq, ts, up, down)
		seq += int64(len(ts))
		aiAge(t, p, f)
		uaRollAndPurge(t, p, f, 200+round)
		if got := aiTotal(t, p, e1); got != want1 {
			t.Fatalf("round %d: e1 usage %d, want %d", round, got, want1)
		}
		if got := aiTotal(t, p, e2); got != want2 {
			t.Fatalf("round %d: e2 usage %d, want %d", round, got, want2)
		}
		if g1, g2 := uaP3(t, p, "iam_v2.p3_entitlement_data_usage", e1), uaP3(t, p, "iam_v2.p3_entitlement_data_usage", e2); g1 != want1 || g2 != want2 {
			t.Fatalf("round %d: p3 usage %d/%d, want %d/%d", round, g1, g2, want1, want2)
		}
	}
	if folded := scalar[int64](t, p, `SELECT COALESCE(sum(records),0) FROM iam_v2.accounting_usage_entitlement WHERE site_id=$1`, f.site); folded == 0 {
		t.Fatal("nothing was folded: the test proves nothing")
	}
}

// ---------------------------------------------------------------------------------------------------------
// 4. Termination when part (or all) of the usage lives only in the folded table.
// ---------------------------------------------------------------------------------------------------------
func aiHourlySession(t *testing.T, p *pgxpool.Pool, quota any, days int) (fixture, string, string, []time.Time) {
	t.Helper()
	f := quotaSeed(t, p, quota, "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-time.Duration(days) * 24 * time.Hour).Truncate(time.Hour)
	ent, sess := grant(t, p, f, nil, base)
	var ts []time.Time
	var up, down []int64
	for h := 0; h < days*24-6; h++ {
		ts, up, down = append(ts, base.Add(time.Duration(h)*time.Hour+20*time.Minute)), append(up, 0), append(down, 1_000)
	}
	uaLoad(t, p, f, sess, 1, ts, up, down)
	return f, ent, sess, ts
}

func TestIntegration_AccountingIntegrity_TerminationAtTheExactQuotaWhenTheCrossingSampleIsStillRaw(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f, ent, _, ts := aiHourlySession(t, p, nil, 5)
	uaSetQuota(t, p, ent, 1_000_000_000) // far away while the history accumulates
	if due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil || len(due) != 0 {
		t.Fatalf("%v %v", due, err)
	}
	aiAge(t, p, f)
	uaRetention(t, p, f, 1)
	left := uaRollAndPurge(t, p, f, 300)
	if left == 0 || left == len(ts) {
		t.Fatalf("%d of %d raw samples left: the fixture needs a folded part and a raw part", left, len(ts))
	}
	// the quota is exactly the running total at a sample that is still raw: 1,000 bytes per hourly sample
	k := len(ts) - 10
	uaSetQuota(t, p, ent, int64(k+1)*1_000)
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" || !due[0].At.Equal(ts[k]) {
		t.Fatalf("expiry = %+v, want one DATA termination at the sample %s that reaches the quota exactly", due, ts[k])
	}
	if n := aiEvents(t, p, f, "CROSSING_INSTANT_APPROXIMATE"); n != 0 {
		t.Fatalf("%d approximate-crossing events for an exact raw crossing", n)
	}
	// one byte short of the quota is not a crossing
	f2, ent2, _, ts2 := aiHourlySession(t, p, nil, 5)
	uaSetQuota(t, p, ent2, 1_000_000_000)
	_, _ = New(p).EnforceExpiries(ctx, f2.tenant, f2.site)
	aiAge(t, p, f2)
	uaRetention(t, p, f2, 1)
	uaRollAndPurge(t, p, f2, 300)
	uaSetQuota(t, p, ent2, int64(len(ts2))*1_000+1)
	if due, err := New(p).EnforceExpiries(ctx, f2.tenant, f2.site); err != nil || len(due) != 0 {
		t.Fatalf("one byte under the quota terminated: %v %v", due, err)
	}
	if s := status(t, p, ent2); s != "ACTIVE" {
		t.Fatalf("entitlement is %s one byte under its quota", s)
	}
}

func TestIntegration_AccountingIntegrity_TerminationWhenTheQuotaWasReachedInsideTheFoldedPartIsRecorded(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f, ent, _, ts := aiHourlySession(t, p, nil, 5)
	uaSetQuota(t, p, ent, 1_000_000_000)
	if _, err := New(p).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	aiAge(t, p, f)
	uaRetention(t, p, f, 1)
	uaRollAndPurge(t, p, f, 300)
	// the quota is lowered (an administrator's decision) to a value the guest passed on day two: both the checkpoint and the
	// folded accumulator say so, and the raw samples no longer hold the crossing sample
	k := 40
	uaSetQuota(t, p, ent, int64(k+1)*1_000)
	due, err := New(p).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" {
		t.Fatalf("expiry = %+v, want one DATA termination", due)
	}
	// the instant is the latest sample of the crossing hour (the sample is gone), never earlier than the crossing sample, and never
	// beyond that hour
	if due[0].At.Before(ts[k]) || !due[0].At.Truncate(time.Hour).Equal(ts[k].Truncate(time.Hour)) {
		t.Fatalf("ended at %s; the crossing sample was %s", due[0].At, ts[k])
	}
	if n := aiEvents(t, p, f, "CROSSING_INSTANT_APPROXIMATE"); n != 1 {
		t.Fatalf("%d approximate-crossing events, want 1: the operator cannot see the crossing is only known to the hour", n)
	}
	var kinds int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.accounting_integrity_summary($1,$2) WHERE kind='CROSSING_INSTANT_APPROXIMATE' AND events=1`,
		f.tenant, f.site).Scan(&kinds)
	if kinds != 1 {
		t.Fatal("accounting_integrity_summary does not show the approximate crossing")
	}
}

// ---------------------------------------------------------------------------------------------------------
// 5. Retroactive bindings (checkout passes a boundary in the past) into history the purge has already folded.
// ---------------------------------------------------------------------------------------------------------
func TestIntegration_AccountingIntegrity_ARetroactiveRebindIntoFoldedHistoryNeverGrantsExtraQuota(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f, e1, sess, ts := aiHourlySession(t, p, nil, 6)
	e2 := uaExtraEntitlement(t, p, f, ts[0], 1)
	aiAge(t, p, f)
	uaRetention(t, p, f, 1)
	uaRollAndPurge(t, p, f, 300)
	pAt := ts[0].Add(2*24*time.Hour + 20*time.Minute) // not on an hour edge, two days into the stay, long past the retention
	var truth2 int64
	for _, s := range ts {
		if !s.Before(pAt) {
			truth2 += 1_000
		}
	}
	truth1 := int64(len(ts))*1_000 - truth2
	before1 := aiTotal(t, p, e1)
	if before1 != int64(len(ts))*1_000 {
		t.Fatalf("fixture: e1 = %d", before1)
	}

	aiRebind(t, p, f, sess, e2, pAt)
	got1, got2 := aiTotal(t, p, e1), aiTotal(t, p, e2)
	if got2 < truth2 {
		t.Fatalf("e2 reads %d, below the %d it truly used: the retroactive rebind granted extra quota", got2, truth2)
	}
	if got1 < truth1 {
		t.Fatalf("e1 reads %d, below the %d it truly used", got1, truth1)
	}
	// nothing is invented: e2 is over by at most the session's folded bytes of the hour holding pAt (the part before pAt)
	if got2 > truth2+1_000 {
		t.Fatalf("e2 reads %d for a truth of %d: more than the one hour of uncertainty", got2, truth2)
	}
	if n := aiEvents(t, p, f, "RETROACTIVE_BINDING_PAST_RAW_RETENTION"); n != 1 {
		t.Fatalf("%d retroactive-binding events, want 1", n)
	}
	var strad, inexact int64
	if err := p.QueryRow(context.Background(), `SELECT straddling_bytes, inexact_bytes FROM iam_v2.entitlement_usage_uncertainty($1,$2)`,
		e2, time.Now().Add(time.Minute)).Scan(&strad, &inexact); err != nil {
		t.Fatal(err)
	}
	if inexact == 0 {
		t.Fatal("the estimated charge is not visible as inexact bytes")
	}
	if n := scalar[int64](t, p, `SELECT COALESCE(sum(events),0) FROM iam_v2.accounting_integrity_summary($1,$2) WHERE kind IN ('RETROACTIVE_BINDING_PAST_RAW_RETENTION','FOLDED_BUCKET_INEXACT')`,
		f.tenant, f.site); n < 2 {
		t.Fatalf("summary shows %d entries; the operator cannot see the uncertainty", n)
	}
	// the checkpoint of e2 was lifted to what it was charged, so the crossing trigger is not blind to it
	if c := scalar[int64](t, p, `SELECT COALESCE(sum(counted_bytes),0) FROM iam_v2.data_crossing_progress WHERE entitlement_id=$1`, e2); c < got2-aiRawBytes(p, sess) {
		t.Fatalf("e2 checkpoint %d is below its folded charge", c)
	}
}

// aiRebind moves a session to another entitlement at p_at through the controlled operation (the device must be authorized on it).
func aiRebind(t *testing.T, p *pgxpool.Pool, f fixture, sess, ent string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,$3)`, ent, f.device, at); err != nil {
		t.Fatalf("authorize device: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.rebind_session_entitlement($1,$2,$3)`, sess, ent, at); err != nil {
		t.Fatalf("rebind: %v", err)
	}
}

// aiRawBytes is the raw bytes of the session still present (they are counted by the tail, not by the checkpoint).
func aiRawBytes(p *pgxpool.Pool, sess string) int64 {
	var n int64
	_ = p.QueryRow(context.Background(), `SELECT COALESCE(sum(bytes_up+bytes_down),0) FROM iam_v2.accounting_records WHERE session_id=$1`, sess).Scan(&n)
	return n
}

func TestIntegration_AccountingIntegrity_ARecentRebindChargesNothingAndMissingHistoryIsNeverInvented(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f, e1, sess, ts := aiHourlySession(t, p, nil, 6)
	e2 := uaExtraEntitlement(t, p, f, ts[0], 1)
	e3 := uaExtraEntitlement(t, p, f, ts[0], 2)
	aiAge(t, p, f)
	uaRetention(t, p, f, 1)
	uaRollAndPurge(t, p, f, 300)
	// within the retention: the raw samples are still there and re-attribute exactly; no event, no charge
	aiRebind(t, p, f, sess, e2, time.Now().Add(-2*time.Hour))
	if n := aiEvents(t, p, f, "RETROACTIVE_BINDING_PAST_RAW_RETENTION"); n != 0 {
		t.Fatalf("%d events for a rebind inside the retention", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_entitlement WHERE entitlement_id=$1`, e2); n != 0 {
		t.Fatalf("e2 was charged %d accumulator rows for a recent rebind", n)
	}
	// a session with NO folded history at all: an old boundary is recorded, nothing is charged (nothing fabricated)
	f3 := quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	old := time.Now().UTC().Add(-9 * 24 * time.Hour)
	_, sess3 := grant(t, p, f3, nil, old)
	x := uaExtraEntitlement(t, p, f3, old, 1)
	aiRebind(t, p, f3, sess3, x, old.Add(24*time.Hour))
	if n := aiEvents(t, p, f3, "RETROACTIVE_BINDING_PAST_RAW_RETENTION"); n != 1 {
		t.Fatalf("%d events for an old boundary, want 1", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_entitlement WHERE entitlement_id=$1`, x); n != 0 {
		t.Fatalf("usage was fabricated for a session with no history: %d rows", n)
	}
	if b := scalar[int64](t, p, `SELECT COALESCE(sum(bytes),0) FROM iam_v2.accounting_integrity_events WHERE site_id=$1`, f3.site); b != 0 {
		t.Fatalf("the event claims %d bytes for a session with no history", b)
	}
	_, _ = e1, e3
}

// ---------------------------------------------------------------------------------------------------------
// 6. Checkout's usage-at-instant read never reads less than the guest used.
// ---------------------------------------------------------------------------------------------------------
func TestIntegration_AccountingIntegrity_AnInstantInsideAFoldedHourIsCountedWholeAndSaysSo(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f, ent, _, ts := aiHourlySession(t, p, nil, 5)
	// two samples in one hour: 20 and 50 minutes past (the fixture has one per hour; add the second)
	extra := ts[10].Add(30 * time.Minute)
	sess := scalar[string](t, p, `SELECT session_id::text FROM iam_v2.session_entitlement_bindings WHERE entitlement_id=$1 LIMIT 1`, ent)
	uaLoad(t, p, f, sess, 1000, []time.Time{extra}, []int64{0}, []int64{1_000})
	aiAge(t, p, f)
	uaRetention(t, p, f, 1)
	uaRollAndPurge(t, p, f, 300)
	at := ts[10].Add(10 * time.Minute) // before the second sample of the hour, after the first
	truth := int64(11) * 1_000         // samples 0..10
	got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", ent, at)
	if got.up+got.down < truth {
		t.Fatalf("usage at the boundary reads %d, below the truth %d", got.up+got.down, truth)
	}
	if got.latest != nil && got.latest.After(at) {
		t.Fatalf("latest_sampled_at %v is after the boundary %v", got.latest, at)
	}
	var strad int64
	if err := p.QueryRow(context.Background(), `SELECT straddling_bytes FROM iam_v2.entitlement_usage_uncertainty($1,$2)`, ent, at).Scan(&strad); err != nil {
		t.Fatal(err)
	}
	if strad == 0 || got.up+got.down-truth > strad {
		t.Fatalf("over by %d but the uncertainty function says %d", got.up+got.down-truth, strad)
	}
	// at an instant whose hour is wholly past, the figure is exact and the uncertainty is zero
	exact := ts[20].Add(5 * time.Minute)
	g2 := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", ent, exact)
	if g2.up+g2.down != int64(22)*1_000 { // samples 0..20 plus the extra half-hour one
		t.Fatalf("exact read = %d", g2.up+g2.down)
	}
	if err := p.QueryRow(context.Background(), `SELECT straddling_bytes FROM iam_v2.entitlement_usage_uncertainty($1,$2)`, ent, exact).Scan(&strad); err != nil || strad != 0 {
		t.Fatalf("straddling bytes %d (%v) for an exact read", strad, err)
	}
}

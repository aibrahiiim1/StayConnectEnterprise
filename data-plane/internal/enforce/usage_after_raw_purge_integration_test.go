//go:build integration

package enforce

// Migration 0180: an Entitlement's usage survives the raw purge. Before it, three functions re-summed the RAW samples
// (entitlement_usage_bytes, p3_entitlement_data_usage, p6_data_crossing_full) and so read less once 0175's purge had
// removed the old samples. The tests keep a copy of the pre-0180 definitions inside the test (zz_old_*) and compare.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The definitions as they were before 0180 (0065, 0062, 0122), under other names, for comparison only.
const uaOldFunctions = `
CREATE OR REPLACE FUNCTION iam_v2.zz_old_usage_bytes(p_ent uuid, p_at timestamptz)
RETURNS TABLE(bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamptz)
LANGUAGE sql STABLE SET search_path = iam_v2, pg_temp AS $$
  SELECT COALESCE(sum(ar.bytes_up),0)::bigint, COALESCE(sum(ar.bytes_down),0)::bigint,
         count(*)::bigint, max(ar.sampled_at)
  FROM iam_v2.accounting_records ar
  JOIN iam_v2.sessions s ON s.id = ar.session_id
  WHERE ar.sampled_at <= p_at
    AND (
      EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b
              WHERE b.session_id = ar.session_id AND b.entitlement_id = p_ent
                AND b.bound_from <= ar.sampled_at AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at))
      OR (s.entitlement_id = p_ent
          AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = ar.session_id))
    );
$$;
CREATE OR REPLACE FUNCTION iam_v2.zz_old_p3_usage(p_entitlement uuid) RETURNS bigint
LANGUAGE sql STABLE SET search_path = iam_v2, pg_temp AS $usage$
  SELECT COALESCE(sum(ar.bytes_up + ar.bytes_down), 0)::bigint
    FROM iam_v2.accounting_records ar
    JOIN iam_v2.session_entitlement_bindings b
      ON b.session_id = ar.session_id AND b.entitlement_id = p_entitlement AND b.bound_from <= ar.sampled_at
     AND ( b.bound_until IS NULL OR b.bound_until > ar.sampled_at
        OR ( b.bound_until = ar.sampled_at
             AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2
                              WHERE b2.session_id = ar.session_id AND b2.bound_from <= ar.sampled_at
                                AND (b2.bound_until IS NULL OR b2.bound_until > ar.sampled_at)) ) )
$usage$;
CREATE OR REPLACE FUNCTION iam_v2.zz_old_p6_full(p_entitlement uuid) RETURNS timestamp with time zone
LANGUAGE sql STABLE SET search_path TO 'iam_v2', 'pg_temp' AS $$
  SELECT min(x.sampled_at)
    FROM iam_v2.entitlements e
    LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
    CROSS JOIN LATERAL (
      SELECT ar.sampled_at,
             sum(ar.bytes_up + ar.bytes_down) OVER (ORDER BY ar.sampled_at, ar.id) AS running
        FROM iam_v2.accounting_records ar
        JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
         AND b.entitlement_id = e.id AND b.bound_from <= ar.sampled_at
         AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
    ) x
   WHERE e.id = p_entitlement
     AND COALESCE(e.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
     AND x.running >= COALESCE(e.data_quota_bytes, spr.data_quota_bytes);
$$;`

func uaInstallOld(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	if _, err := p.Exec(context.Background(), uaOldFunctions); err != nil {
		t.Fatalf("install the pre-0180 definitions: %v", err)
	}
}

// uaSuper runs fixture statements that bypass the guards (the trigger-free replica role), as the superuser the harness is.
func uaSuper(t *testing.T, p *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("replica role: %v", err)
	}
	if err := fn(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

type uaBinding struct {
	ent   string
	from  time.Time
	until *time.Time
}

func uaSetBindings(t *testing.T, p *pgxpool.Pool, f fixture, sess string, chain []uaBinding, currentEnt string) {
	t.Helper()
	uaSuper(t, p, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.session_entitlement_bindings WHERE session_id=$1`, sess); err != nil {
			return err
		}
		for i, b := range chain {
			if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.session_entitlement_bindings
				(tenant_id,site_id,session_id,entitlement_id,seq,bound_from,bound_until) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				f.tenant, f.site, sess, b.ent, i+1, b.from, b.until); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET entitlement_id=$2 WHERE id=$1`, sess, currentEnt)
		return err
	})
}

// uaLoad inserts raw samples of one session; ingested_at is sampled_at + 2 s, as in production.
func uaLoad(t *testing.T, p *pgxpool.Pool, f fixture, sess string, seqStart int64, ts []time.Time, up, down []int64) {
	t.Helper()
	uaSuper(t, p, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.accounting_records
			(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down,sampled_at,ingested_at)
			SELECT $1,$2,$3,$4::bigint + x.o, x.u, x.d, x.s, x.s + interval '2 seconds'
			  FROM unnest($5::bigint[], $6::bigint[], $7::timestamptz[]) WITH ORDINALITY AS x(u,d,s,o)`,
			f.tenant, f.site, sess, seqStart, up, down, ts)
		return err
	})
}

type uaUsage struct {
	up, down, n int64
	latest      *time.Time
}

func (u uaUsage) String() string {
	l := "nil"
	if u.latest != nil {
		l = u.latest.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("{up %d down %d n %d latest %s}", u.up, u.down, u.n, l)
}

func uaSame(a, b uaUsage) bool {
	if a.up != b.up || a.down != b.down || a.n != b.n {
		return false
	}
	if (a.latest == nil) != (b.latest == nil) {
		return false
	}
	return a.latest == nil || a.latest.Equal(*b.latest)
}

func uaUsageAt(t *testing.T, p *pgxpool.Pool, fn, ent string, at time.Time) uaUsage {
	t.Helper()
	var u uaUsage
	if err := p.QueryRow(context.Background(),
		`SELECT bytes_up, bytes_down, records, latest_sampled_at FROM `+fn+`($1,$2)`, ent, at).
		Scan(&u.up, &u.down, &u.n, &u.latest); err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return u
}

func uaP3(t *testing.T, p *pgxpool.Pool, fn, ent string) int64 {
	t.Helper()
	return scalar[int64](t, p, `SELECT `+fn+`($1)`, ent)
}

func uaP6(t *testing.T, p *pgxpool.Pool, fn, ent string) *time.Time {
	t.Helper()
	return scalar[*time.Time](t, p, `SELECT `+fn+`($1)`, ent)
}

func uaSetQuota(t *testing.T, p *pgxpool.Pool, ent string, q int64) {
	t.Helper()
	uaSuper(t, p, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET data_quota_bytes=$2 WHERE id=$1`, ent, q)
		return err
	})
}

func uaRetention(t *testing.T, p *pgxpool.Pool, f fixture, rawDays int) {
	t.Helper()
	if _, err := rolePool(t, "svc_edged").Exec(context.Background(),
		`SELECT iam_v2.usage_rollup_settings_set($1,$2,7,$3,10,'admin@test','test raw retention')`, f.tenant, f.site, rawDays); err != nil {
		t.Fatalf("raw retention: %v", err)
	}
}

// uaRollAndPurge rolls everything up and purges until nothing more goes; returns the raw rows left.
func uaRollAndPurge(t *testing.T, p *pgxpool.Pool, f fixture, batch int) int {
	t.Helper()
	ctx := context.Background()
	acctd := New(rolePool(t, "svc_acctd"))
	// the rollup caps at now()-1 minute and stops when nothing is left; drain it
	for i := 0; i < 400; i++ {
		n, err := acctd.RollupAccountingUsage(ctx, f.tenant, f.site)
		if err != nil {
			t.Fatalf("rollup: %v", err)
		}
		if n == 0 {
			break
		}
	}
	for i := 0; i < 400; i++ {
		n, err := acctd.PurgeAccountingDetail(ctx, f.tenant, f.site, batch)
		if err != nil {
			t.Fatalf("purge: %v", err)
		}
		if n == 0 {
			break
		}
	}
	return count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1`, f.site)
}

// ----------------------------------------------------------------------------------------------------------
// 1. The demonstrated defect: a multi-day session with a rebind in the middle, raw purged.
// ----------------------------------------------------------------------------------------------------------
func TestIntegration_UsageAfterRawPurge_MultiDayRebindReadsTheSameAfterThePurge(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	uaInstallOld(t, p)
	f := quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-6*24*time.Hour - 37*time.Minute)
	first, sess := grant(t, p, f, nil, base)
	second := uaExtraEntitlement(t, p, f, base, 1)
	boundary := base.Add(3*24*time.Hour + 13*time.Minute)
	uaSetBindings(t, p, f, sess, []uaBinding{{first, base, &boundary}, {second, boundary, nil}}, second)

	// a sample every 10 minutes for 6 days, one exactly on the boundary
	var ts []time.Time
	var up, down []int64
	var tot int64
	for at := base.Add(time.Minute); at.Before(now.Add(-2 * time.Hour)); at = at.Add(10 * time.Minute) {
		ts = append(ts, at)
		up = append(up, 1_000+int64(len(ts)%7)*100)
		down = append(down, 6_000+int64(len(ts)%11)*50)
		tot += up[len(up)-1] + down[len(down)-1]
	}
	ts = append(ts, boundary)
	up = append(up, 5_000)
	down = append(down, 7_000)
	tot += 12_000
	uaLoad(t, p, f, sess, 1, ts, up, down)
	uaRetention(t, p, f, 1)

	wantFirst := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", first, now)
	wantSecond := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", second, now)
	if wantFirst.up+wantFirst.down+wantSecond.up+wantSecond.down != tot {
		t.Fatalf("fixture: %v + %v does not sum to %d", wantFirst, wantSecond, tot)
	}
	wantP3 := [2]int64{uaP3(t, p, "iam_v2.p3_entitlement_data_usage", first), uaP3(t, p, "iam_v2.p3_entitlement_data_usage", second)}
	before := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1`, f.site)

	left := uaRollAndPurge(t, p, f, 500)
	if left >= before/2 {
		t.Fatalf("the purge left %d of %d raw samples; the fixture should have lost more than half", left, before)
	}
	if got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", first, now); !uaSame(got, wantFirst) {
		t.Fatalf("entitlement_usage_bytes(first) = %v after the purge, was %v", got, wantFirst)
	}
	if got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", second, now); !uaSame(got, wantSecond) {
		t.Fatalf("entitlement_usage_bytes(second) = %v after the purge, was %v", got, wantSecond)
	}
	if got := uaP3(t, p, "iam_v2.p3_entitlement_data_usage", first); got != wantP3[0] {
		t.Fatalf("p3_entitlement_data_usage(first) = %d after the purge, was %d", got, wantP3[0])
	}
	if got := uaP3(t, p, "iam_v2.p3_entitlement_data_usage", second); got != wantP3[1] {
		t.Fatalf("p3_entitlement_data_usage(second) = %d after the purge, was %d", got, wantP3[1])
	}
	// the old entitlement's usage at its own boundary: every sample it has is at or before it, so it is exact
	if got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", first, boundary); !uaSame(got, wantFirst) {
		t.Fatalf("entitlement_usage_bytes(first, boundary) = %v, want %v", got, wantFirst)
	}
	// the accumulator holds only what the purge deleted: its records plus the raw left are every sample
	if folded := scalar[int64](t, p, `SELECT COALESCE(sum(records),0) FROM iam_v2.accounting_usage_entitlement WHERE site_id=$1`, f.site); folded+int64(left) != int64(before) {
		t.Fatalf("folded %d + raw %d != %d samples", folded, left, before)
	}
	_ = ctx
}

// ----------------------------------------------------------------------------------------------------------
// 2. Equivalence with the raw-based definitions, on random sessions, bindings, rebinds and edge instants.
// ----------------------------------------------------------------------------------------------------------
type uaScenario struct {
	f        fixture
	ents     []string
	sessions []string
	// instants worth probing: sample times, binding boundaries, hour edges
	probes []time.Time
	base   time.Time
}

func uaBuild(t *testing.T, p *pgxpool.Pool, rng *rand.Rand, days float64) uaScenario {
	t.Helper()
	var sc uaScenario
	sc.f = quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	sc.base = now.Add(-time.Duration(days*24) * time.Hour)
	base := sc.base
	e0, s0 := grant(t, p, sc.f, nil, base)
	sc.ents = []string{e0}
	for i := 1; i < 3; i++ {
		sc.ents = append(sc.ents, uaExtraEntitlement(t, p, sc.f, base, i))
	}
	sc.sessions = []string{s0}
	nSess := 2 + rng.Intn(3)
	for i := 1; i < nSess; i++ {
		m := meteredSession(t, p, sc.f, sc.ents[rng.Intn(len(sc.ents))], fmt.Sprintf("10.20.%d.%d", 40+i, 10+rng.Intn(200)),
			fmt.Sprintf("02:00:00:00:%02x:%02x", 0x60+i, rng.Intn(250)), "br-g-ua", base)
		sc.sessions = append(sc.sessions, m.id)
	}
	span := now.Add(-3 * time.Hour).Sub(base)
	rnd := func(lo, hi time.Duration) time.Duration { return lo + time.Duration(rng.Int63n(int64(hi-lo))) }
	for si, sess := range sc.sessions {
		start := base.Add(rnd(0, 2*time.Hour))
		var chain []uaBinding
		var cur string
		var edges []time.Time
		switch rng.Intn(5) {
		case 0: // no binding row at all: the fallback arm
			cur = sc.ents[rng.Intn(len(sc.ents))]
		default:
			nb := 1 + rng.Intn(4)
			at := start
			for b := 0; b < nb; b++ {
				ent := sc.ents[rng.Intn(len(sc.ents))]
				bb := uaBinding{ent: ent, from: at}
				last := b == nb-1
				if !last || rng.Intn(2) == 0 { // the last one may stay open or close (a terminal binding)
					var until time.Time
					switch rng.Intn(4) {
					case 0:
						until = at // zero length
					case 1:
						until = at.Truncate(time.Hour).Add(time.Hour) // exactly on an hour edge
					default:
						until = at.Add(rnd(time.Minute, span/time.Duration(nb)))
					}
					if until.Before(at) {
						until = at
					}
					bb.until = &until
					edges = append(edges, until)
					at = until
					if !last && rng.Intn(4) == 0 {
						at = until.Add(rnd(time.Second, 30*time.Minute)) // a gap: unattributed samples
					}
				}
				chain = append(chain, bb)
				cur = ent
			}
		}
		if chain != nil {
			uaSetBindings(t, p, sc.f, sess, chain, cur)
		} else {
			uaSetBindings(t, p, sc.f, sess, nil, cur)
		}
		// samples: random times (some on edges), some duplicated instants across sessions, all older than now-3h
		var ts []time.Time
		step := time.Duration(2+rng.Intn(25)) * time.Minute
		for at := start.Add(time.Second); at.Before(now.Add(-3 * time.Hour)); at = at.Add(step + time.Duration(rng.Intn(60))*time.Second) {
			ts = append(ts, at)
		}
		for _, e := range edges {
			if e.Before(now.Add(-3 * time.Hour)) {
				ts = append(ts, e, e.Add(-time.Microsecond), e.Add(time.Microsecond))
			}
		}
		// order does not matter for the insert but keep (session_id, sample_seq) unique and ordered
		sortTimes(ts)
		up := make([]int64, len(ts))
		down := make([]int64, len(ts))
		for i := range ts {
			up[i] = int64(rng.Intn(5_000))
			down[i] = int64(rng.Intn(50_000))
			if rng.Intn(10) == 0 {
				up[i], down[i] = 0, 0
			}
		}
		uaLoad(t, p, sc.f, sess, 1, ts, up, down)
		sc.probes = append(sc.probes, edges...)
		for i := 0; i < len(ts); i += 1 + len(ts)/6 {
			sc.probes = append(sc.probes, ts[i])
		}
		_ = si
	}
	for i := 0; i < 8; i++ {
		sc.probes = append(sc.probes, base.Add(rnd(0, span)))
	}
	return sc
}

func sortTimes(ts []time.Time) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].Before(ts[j-1]); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

// With every raw sample present the new functions return exactly what the old ones did.
func TestIntegration_UsageAfterRawPurge_EqualsTheRawFunctionsWhenRawIsComplete(t *testing.T) {
	p := pool(t)
	defer p.Close()
	uaInstallOld(t, p)
	for seed := int64(1); seed <= 8; seed++ {
		rng := rand.New(rand.NewSource(seed))
		sc := uaBuild(t, p, rng, 1+rng.Float64()*3)
		for _, ent := range sc.ents {
			if o, n := uaP3(t, p, "iam_v2.zz_old_p3_usage", ent), uaP3(t, p, "iam_v2.p3_entitlement_data_usage", ent); o != n {
				t.Fatalf("seed %d: p3 usage old %d new %d", seed, o, n)
			}
			for _, at := range append(sc.probes, time.Now()) {
				o, n := uaUsageAt(t, p, "iam_v2.zz_old_usage_bytes", ent, at), uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", ent, at)
				if !uaSame(o, n) {
					t.Fatalf("seed %d: usage_bytes(%s, %s) old %v new %v", seed, ent, at.Format(time.RFC3339Nano), o, n)
				}
			}
			total := uaP3(t, p, "iam_v2.zz_old_p3_usage", ent)
			for _, q := range []int64{1, total / 10, total / 2, total - 1, total, total + 1} {
				if q < 1 {
					continue
				}
				uaSetQuota(t, p, ent, q)
				o, n := uaP6(t, p, "iam_v2.zz_old_p6_full", ent), uaP6(t, p, "iam_v2.p6_data_crossing_full", ent)
				if (o == nil) != (n == nil) || (o != nil && !o.Equal(*n)) {
					t.Fatalf("seed %d: p6 crossing for quota %d: old %v new %v", seed, q, o, n)
				}
			}
		}
	}
}

// After a purge the totals, the usage at an instant inside the retained raw, and the crossing equal what the raw-based
// functions returned before it (the crossing is the latest sample of its hour when its sample was folded).
func TestIntegration_UsageAfterRawPurge_PurgeChangesNoTotal(t *testing.T) {
	p := pool(t)
	defer p.Close()
	uaInstallOld(t, p)
	purgedSomething := 0
	for seed := int64(101); seed <= 108; seed++ {
		rng := rand.New(rand.NewSource(seed))
		sc := uaBuild(t, p, rng, 3+rng.Float64()*3)
		uaRetention(t, p, sc.f, 1)
		now := time.Now().UTC()
		type want struct {
			now  uaUsage
			p3   int64
			at   map[time.Time]uaUsage
			full map[int64]*time.Time
		}
		wants := map[string]*want{}
		probes := []time.Time{now.Add(-20 * time.Hour), now.Add(-9 * time.Hour), now.Add(-4 * time.Hour), now}
		quotas := map[string][]int64{}
		for _, ent := range sc.ents {
			w := &want{at: map[time.Time]uaUsage{}, full: map[int64]*time.Time{}}
			w.now = uaUsageAt(t, p, "iam_v2.zz_old_usage_bytes", ent, now)
			w.p3 = uaP3(t, p, "iam_v2.zz_old_p3_usage", ent)
			for _, at := range probes {
				w.at[at] = uaUsageAt(t, p, "iam_v2.zz_old_usage_bytes", ent, at)
			}
			for _, q := range []int64{w.p3 / 20, w.p3 / 3, w.p3 * 2 / 3, w.p3 - 1, w.p3} {
				if q < 1 {
					continue
				}
				uaSetQuota(t, p, ent, q)
				w.full[q] = uaP6(t, p, "iam_v2.zz_old_p6_full", ent)
				quotas[ent] = append(quotas[ent], q)
			}
			wants[ent] = w
		}
		// which samples exist now, to tell folded from raw afterwards
		before := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1`, sc.f.site)
		left := uaRollAndPurge(t, p, sc.f, 300+rng.Intn(900))
		if left < before {
			purgedSomething++
		}
		for ent, w := range wants {
			if got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", ent, now); !uaSame(got, w.now) {
				t.Fatalf("seed %d: usage_bytes(now) after purge %v, before %v", seed, got, w.now)
			}
			if got := uaP3(t, p, "iam_v2.p3_entitlement_data_usage", ent); got != w.p3 {
				t.Fatalf("seed %d: p3 usage after purge %d, before %d", seed, got, w.p3)
			}
			// an instant after the purge horizon (the last 20 hours are raw, the bucket cut is 24 h ago): exact
			if got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", ent, probes[0]); !uaSame(got, w.at[probes[0]]) {
				t.Fatalf("seed %d: usage_bytes(now-20h) after purge %v, before %v", seed, got, w.at[probes[0]])
			}
			for _, at := range probes[1:] {
				if got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", ent, at); !uaSame(got, w.at[at]) {
					t.Fatalf("seed %d: usage_bytes(%s) after purge %v, before %v", seed, at, got, w.at[at])
				}
			}
			for _, q := range quotas[ent] {
				uaSetQuota(t, p, ent, q)
				got := uaP6(t, p, "iam_v2.p6_data_crossing_full", ent)
				exp := w.full[q]
				if exp == nil {
					if got != nil {
						t.Fatalf("seed %d: quota %d: no crossing before the purge, %v after", seed, q, got)
					}
					continue
				}
				if got == nil {
					t.Fatalf("seed %d: quota %d: crossing %v before the purge, none after", seed, q, exp)
				}
				// a crossing whose sample is still raw is exact; a folded one is the latest sample time of its hour
				rawLeft := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1 AND sampled_at=$2`, sc.f.site, *exp)
				if rawLeft > 0 {
					if !got.Equal(*exp) {
						t.Fatalf("seed %d: quota %d: crossing %v, was %v (the sample is still raw)", seed, q, got, exp)
					}
				} else if got.Before(*exp) || !got.Truncate(time.Hour).Equal(exp.Truncate(time.Hour)) {
					t.Fatalf("seed %d: quota %d: folded crossing %v is not in the hour of %v at or after it", seed, q, got, exp)
				}
			}
		}
		// no entitlement lost anything it had folded in an inconsistent state
		if bad := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_entitlement WHERE site_id=$1 AND NOT exact`, sc.f.site); bad != 0 {
			t.Fatalf("seed %d: %d inexact accumulator rows from a live purge", seed, bad)
		}
	}
	if purgedSomething == 0 {
		t.Fatal("no scenario purged anything: the test proves nothing")
	}
}

// A usage-at-instant read that lands inside a purged hour is an UPPER bound since 0181 (the straddling hour is counted whole), never
// a lower bound: the post-stay quota test must not read less than the guest used. entitlement_usage_uncertainty says by how much.
func TestIntegration_UsageAfterRawPurge_AnInstantInsideAPurgedHourIsAnUpperBound(t *testing.T) {
	p := pool(t)
	defer p.Close()
	uaInstallOld(t, p)
	rng := rand.New(rand.NewSource(7))
	sc := uaBuild(t, p, rng, 5)
	uaRetention(t, p, sc.f, 1)
	var asked []time.Time
	for i := 0; i < 12; i++ {
		asked = append(asked, sc.base.Add(time.Duration(rng.Int63n(int64(3*24*time.Hour)))))
	}
	type k struct {
		ent string
		at  time.Time
	}
	was := map[k]uaUsage{}
	for _, ent := range sc.ents {
		for _, at := range asked {
			was[k{ent, at}] = uaUsageAt(t, p, "iam_v2.zz_old_usage_bytes", ent, at)
		}
	}
	uaRollAndPurge(t, p, sc.f, 700)
	over := 0
	for key, w := range was {
		got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", key.ent, key.at)
		if got.up < w.up || got.down < w.down || got.n < w.n {
			t.Fatalf("an instant in a purged hour UNDERCOUNTS (grants quota): got %v, truth %v", got, w)
		}
		if got.latest != nil && got.latest.After(key.at) {
			t.Fatalf("latest_sampled_at %v is after the asked instant %v", got.latest, key.at)
		}
		var strad, inexact int64
		if err := p.QueryRow(context.Background(), `SELECT straddling_bytes, inexact_bytes FROM iam_v2.entitlement_usage_uncertainty($1,$2)`,
			key.ent, key.at).Scan(&strad, &inexact); err != nil {
			t.Fatal(err)
		}
		excess := (got.up + got.down) - (w.up + w.down)
		if excess > strad {
			t.Fatalf("over by %d but the uncertainty function only admits %d", excess, strad)
		}
		if excess == 0 && strad == 0 {
			continue
		}
		if excess > 0 {
			over++
		}
		if inexact != 0 {
			t.Fatalf("a live purge must fold exactly: %d inexact bytes", inexact)
		}
	}
	t.Logf("%d of %d probes inside a purged hour were counted conservatively", over, len(was))
}

// ----------------------------------------------------------------------------------------------------------
// 3. Interrupted and repeated roll-up and purge windows leave the accumulator consistent with the raw sums.
// ----------------------------------------------------------------------------------------------------------
func TestIntegration_UsageAfterRawPurge_InterruptedRollupAndPurgeLoseNothing(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	uaInstallOld(t, p)
	rng := rand.New(rand.NewSource(4242))
	sc := uaBuild(t, p, rng, 4)
	uaRetention(t, p, sc.f, 1)
	now := time.Now().UTC()
	want := map[string]uaUsage{}
	wantP3 := map[string]int64{}
	var allSamples int64
	for _, ent := range sc.ents {
		want[ent] = uaUsageAt(t, p, "iam_v2.zz_old_usage_bytes", ent, now)
		wantP3[ent] = uaP3(t, p, "iam_v2.zz_old_p3_usage", ent)
	}
	allSamples = int64(count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1`, sc.f.site))
	check := func(when string) {
		t.Helper()
		for _, ent := range sc.ents {
			if got := uaUsageAt(t, p, "iam_v2.entitlement_usage_bytes", ent, now); !uaSame(got, want[ent]) {
				t.Fatalf("%s: usage_bytes %v, want %v", when, got, want[ent])
			}
			if got := uaP3(t, p, "iam_v2.p3_entitlement_data_usage", ent); got != wantP3[ent] {
				t.Fatalf("%s: p3 usage %d, want %d", when, got, wantP3[ent])
			}
		}
		// raw + folded = every sample at every moment: nothing counted twice, nothing lost
		raw := int64(count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE site_id=$1`, sc.f.site))
		// samples attributed to nobody (gaps) are in neither; compare through the totals above, and through the folded count:
		folded := scalar[int64](t, p, `SELECT COALESCE(sum(records) FILTER (WHERE arm IN (0,2)),0) FROM iam_v2.accounting_usage_entitlement WHERE site_id=$1`, sc.f.site)
		if raw+int64(0) > allSamples || folded > allSamples-raw {
			t.Fatalf("%s: raw %d + folded %d exceeds the %d loaded", when, raw, folded, allSamples)
		}
	}
	// kill statements at assorted moments: a statement timeout and a backend termination, many times over
	acct := rolePool(t, "svc_acctd")
	killed := 0
	run := func(sql string, timeoutMs int) {
		c, err := acct.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Release()
		if _, err := c.Exec(ctx, fmt.Sprintf(`SET statement_timeout = '%dms'`, timeoutMs)); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(ctx, sql, sc.f.tenant, sc.f.site); err != nil { // the timeout is the point
			killed++
		}
		if _, err := c.Exec(ctx, `RESET statement_timeout`); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 12; i++ {
		run(`SELECT iam_v2.rollup_accounting_usage($1,$2,interval '90 minutes')`, 3+rng.Intn(60))
		check(fmt.Sprintf("rollup killed %d", i))
	}
	for i := 0; i < 400; i++ { // finish the rollup
		var n int64
		if err := p.QueryRow(ctx, `SELECT iam_v2.rollup_accounting_usage($1,$2,interval '2 hours')`, sc.f.tenant, sc.f.site).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	for i := 0; i < 15; i++ {
		run(`SELECT iam_v2.purge_accounting_detail($1,$2,200)`, 2+rng.Intn(40))
		check(fmt.Sprintf("purge killed %d", i))
	}
	// terminate the backend mid-purge
	for i := 0; i < 5; i++ {
		done := make(chan struct{})
		go func() {
			defer close(done)
			c, err := acct.Acquire(ctx)
			if err != nil {
				return
			}
			defer c.Release()
			_, _ = c.Exec(ctx, `SELECT set_config('application_name','og_usage_victim',false)`)
			_, _ = c.Exec(ctx, `SELECT iam_v2.purge_accounting_detail($1,$2,100)`, sc.f.tenant, sc.f.site)
		}()
		time.Sleep(time.Duration(2+rng.Intn(30)) * time.Millisecond)
		_, _ = p.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='og_usage_victim'`)
		<-done
		check(fmt.Sprintf("purge terminated %d", i))
	}
	if killed == 0 {
		t.Fatal("no statement was cancelled: the test proves nothing about interruption")
	}
	t.Logf("%d statements cancelled by a statement timeout", killed)
	// the same window twice: run everything again to completion, then once more
	left := uaRollAndPurge(t, p, sc.f, 250)
	check("completed")
	left2 := uaRollAndPurge(t, p, sc.f, 250)
	check("repeated")
	if left2 != left {
		t.Fatalf("a repeat purge changed the raw count %d -> %d", left, left2)
	}
	if left >= int(allSamples) {
		t.Fatal("nothing was purged: the test proves nothing")
	}
}

// ----------------------------------------------------------------------------------------------------------
// 4. The quota: a multi-day guest passes it unless the recount sees the purged days.
// ----------------------------------------------------------------------------------------------------------
func TestIntegration_UsageAfterRawPurge_AMultiDayGuestCannotPassTheirQuotaByWaiting(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(500_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-5*24*time.Hour - 20*time.Minute)
	ent, sess := grant(t, p, f, nil, base)
	acctd := rolePool(t, "svc_acctd")
	uaRetention(t, p, f, 1)
	day := func(d int) (ts []time.Time, up, down []int64) {
		for i := 0; i < 24; i++ { // 24 hourly samples of 100 MB / 24 each (sums to 100 MB a day)
			ts = append(ts, base.Add(time.Duration(d)*24*time.Hour+time.Duration(i)*time.Hour+time.Minute))
			up = append(up, 1_000_000)
			down = append(down, 3_166_667)
		}
		return
	}
	seq := int64(1)
	put := func(d int) {
		ts, up, down := day(d)
		uaLoad(t, p, f, sess, seq, ts, up, down)
		seq += int64(len(ts))
	}
	adv := func() {
		t.Helper()
		if _, err := acctd.Exec(ctx, `SELECT iam_v2.p6_advance_data_crossing($1,$2)`, f.tenant, f.site); err != nil {
			t.Fatalf("advance: %v", err)
		}
	}
	for d := 0; d < 3; d++ { // days 0..2: ~300 MB, under the quota
		put(d)
	}
	adv()
	if c := count(t, p, `SELECT count(*) FROM iam_v2.data_crossing_state WHERE entitlement_id=$1 AND crossed_at IS NOT NULL`, ent); c != 0 {
		t.Fatal("crossed at 300 MB of 500")
	}
	left := uaRollAndPurge(t, p, f, 1000) // days 0.. are older than the 1-day raw retention: purged
	if left >= 72 {
		t.Fatalf("fixture: nothing purged (%d raw left)", left)
	}
	put(3)
	put(4) // 500 MB by the end of day 4: the quota is reached
	adv()
	adv()
	crossed := scalar[*time.Time](t, p, `SELECT crossed_at FROM iam_v2.data_crossing_state WHERE entitlement_id=$1`, ent)
	if crossed == nil {
		t.Fatal("a guest who used 500 MB of a 500 MB quota over five days was never found to have crossed it: the recount lost the purged days")
	}
	// 4,166,667 bytes a sample: 119 samples are 495,833,373 and the 120th (day 4, hour 23) reaches 500,000,040
	wantAt := base.Add(4*24*time.Hour + 23*time.Hour + time.Minute)
	if !crossed.Equal(wantAt) {
		t.Fatalf("crossing found at %v, want %v", crossed, wantAt)
	}
	// the maintained derivation agrees with the folded + raw total
	if got := uaP3(t, p, "iam_v2.p3_entitlement_data_usage", ent); got != 120*4_166_667 {
		t.Fatalf("p3 usage %d, want %d", got, int64(120*4_166_667))
	}
}

// ----------------------------------------------------------------------------------------------------------
// 5. The purge of the accumulator follows the hour tier's rule; an active entitlement never loses its total.
// ----------------------------------------------------------------------------------------------------------
func TestIntegration_UsageAfterRawPurge_AccumulatorOutlivesAnActiveEntitlement(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	uaInstallOld(t, p)
	f := quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-60 * 24 * time.Hour)
	live, sess := grant(t, p, f, nil, base)
	var ts []time.Time
	var up, down []int64
	for at := base.Add(time.Minute); at.Before(now.Add(-2*time.Hour)); at = at.Add(3 * time.Hour) {
		ts = append(ts, at)
		up = append(up, 10)
		down = append(down, 90)
	}
	uaLoad(t, p, f, sess, 1, ts, up, down)
	uaRetention(t, p, f, 1)
	if _, err := rolePool(t, "svc_edged").Exec(ctx, `SELECT iam_v2.accounting_retention_set($1,$2,30,'admin@test','keep a month of detail')`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	want := uaP3(t, p, "iam_v2.zz_old_p3_usage", live)
	uaRollAndPurge(t, p, f, 500)
	if got := uaP3(t, p, "iam_v2.p3_entitlement_data_usage", live); got != want {
		t.Fatalf("the total of an ACTIVE entitlement changed %d -> %d past the usage-detail retention", want, got)
	}
	// once the entitlement is TERMINATED and its session ended before the cut, the rows go (the hour tier's rule)
	uaSuper(t, p, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=$2 WHERE id=$1`, sess, now.Add(-40*24*time.Hour)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET status='TERMINATED', terminal_reason='ADMIN' WHERE id=$1`, live)
		return err
	})
	uaRollAndPurge(t, p, f, 500)
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_entitlement WHERE entitlement_id=$1 AND bucket < now() - interval '30 days'`, live); c != 0 {
		t.Fatalf("%d accumulator rows older than the retention survive on a TERMINATED entitlement of an ended session", c)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_usage_entitlement WHERE entitlement_id=$1 AND bucket >= now() - interval '30 days'`, live); c == 0 {
		t.Fatal("rows younger than the retention were removed")
	}
}

// ----------------------------------------------------------------------------------------------------------
// 6. The migration: applies as iam_v2_owner, backfills what was purged before it, and rolls back.
// ----------------------------------------------------------------------------------------------------------
func uaMigration(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../migrations/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	s := string(b)
	// the file's own transaction is the test's: strip its BEGIN and COMMIT
	s = strings.Replace(s, "\nBEGIN;\n", "\n", 1)
	if i := strings.LastIndex(s, "COMMIT;"); i >= 0 {
		s = s[:i]
	}
	return s
}

func TestIntegration_UsageAfterRawPurge_MigrationBackfillsAndRollsBack(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	uaInstallOld(t, p)
	f := quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	now := time.Now().UTC().Truncate(time.Second)
	base := now.Add(-6*24*time.Hour - 41*time.Minute)
	first, sess := grant(t, p, f, nil, base)
	second := uaExtraEntitlement(t, p, f, base, 1)
	boundary := base.Add(3*24*time.Hour + 19*time.Minute)
	uaSetBindings(t, p, f, sess, []uaBinding{{first, base, &boundary}, {second, boundary, nil}}, second)
	var ts []time.Time
	var up, down []int64
	for at := base.Add(time.Minute); at.Before(now.Add(-2 * time.Hour)); at = at.Add(10 * time.Minute) {
		ts = append(ts, at)
		up = append(up, 100)
		down = append(down, 900)
	}
	uaLoad(t, p, f, sess, 1, ts, up, down)
	uaRetention(t, p, f, 1)
	wantFirst := uaP3(t, p, "iam_v2.zz_old_p3_usage", first)
	wantSecond := uaP3(t, p, "iam_v2.zz_old_p3_usage", second)

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE iam_v2_owner`); err != nil {
		t.Fatal(err)
	}
	// roll the schema back to 0179 (the down), purge the way 0175 did, then apply 0180: the backfill must restore the totals
	if b, err := os.ReadFile("../../migrations/0180_entitlement_usage_survives_the_raw_purge.down.sql"); err != nil {
		t.Fatal(err)
	} else {
		s := strings.Replace(string(b), "\nBEGIN;\n", "\n", 1)
		if i := strings.LastIndex(s, "COMMIT;"); i >= 0 {
			s = s[:i]
		}
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	var reg *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('iam_v2.accounting_usage_entitlement')::text`).Scan(&reg); err != nil || reg != nil {
		t.Fatalf("the down left the accumulator table (%v, %v)", reg, err)
	}
	// 0175-style: roll up and purge (no fold)
	for i := 0; i < 400; i++ {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT iam_v2.rollup_accounting_usage($1,$2,interval '2 hours')`, f.tenant, f.site).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	for i := 0; i < 400; i++ {
		var n int
		if err := tx.QueryRow(ctx, `SELECT iam_v2.purge_accounting_detail($1,$2,1000)`, f.tenant, f.site).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	var short int64
	if err := tx.QueryRow(ctx, `SELECT iam_v2.p3_entitlement_data_usage($1) + iam_v2.p3_entitlement_data_usage($2)`, first, second).Scan(&short); err != nil {
		t.Fatal(err)
	}
	if short >= wantFirst+wantSecond {
		t.Fatalf("the purge removed nothing the down-schema reads (%d vs %d)", short, wantFirst+wantSecond)
	}
	if _, err := tx.Exec(ctx, uaMigration(t, "0180_entitlement_usage_survives_the_raw_purge.up.sql")); err != nil {
		t.Fatalf("up as iam_v2_owner: %v", err)
	}
	var gotFirst, gotSecond int64
	if err := tx.QueryRow(ctx, `SELECT iam_v2.p3_entitlement_data_usage($1), iam_v2.p3_entitlement_data_usage($2)`, first, second).Scan(&gotFirst, &gotSecond); err != nil {
		t.Fatal(err)
	}
	// the hour with the boundary inside is best effort; everything else is exact. The sum must be exact: no byte lost, none twice.
	if gotFirst+gotSecond != wantFirst+wantSecond {
		t.Fatalf("after the backfill %d + %d != %d + %d", gotFirst, gotSecond, wantFirst, wantSecond)
	}
	var inexact int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.accounting_usage_entitlement WHERE site_id=$1 AND NOT exact`, f.site).Scan(&inexact); err != nil {
		t.Fatal(err)
	}
	if inexact > 2 {
		t.Fatalf("%d inexact backfilled rows; only the hour the session started in and the hour holding the rebind boundary can be", inexact)
	}
	if gotFirst != wantFirst || gotSecond != wantSecond {
		if inexact == 0 {
			t.Fatalf("split %d/%d, want %d/%d with no inexact row", gotFirst, gotSecond, wantFirst, wantSecond)
		}
	}
	// the migration applied twice is a no-op for the schema objects (idempotent re-run of the replace/create statements is not
	// required), but its down must run again cleanly:
	if b, err := os.ReadFile("../../migrations/0180_entitlement_usage_survives_the_raw_purge.down.sql"); err != nil {
		t.Fatal(err)
	} else {
		s := strings.Replace(string(b), "\nBEGIN;\n", "\n", 1)
		if i := strings.LastIndex(s, "COMMIT;"); i >= 0 {
			s = s[:i]
		}
		if _, err := tx.Exec(ctx, s); err != nil {
			t.Fatalf("down after up: %v", err)
		}
	}
	// everything above is rolled back with the transaction
}

// uaExtraEntitlement is secondEntitlement with a reservation id of its own, so several can share a site.
func uaExtraEntitlement(t *testing.T, p *pgxpool.Pool, f fixture, at time.Time, n int) string {
	t.Helper()
	ctx := context.Background()
	var stay string
	stx := opTx(t, p, "stay")
	defer func() { _ = stx.Rollback(ctx) }()
	if err := stx.QueryRow(ctx, `INSERT INTO iam_v2.stays
		(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,
		 lifecycle_version,last_applied_event_version)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,'IN_HOUSE',1,0) RETURNING id::text`,
		f.tenant, f.site, f.iface, fmt.Sprintf("UX%d", n), fmt.Sprintf("UY%d", n)).Scan(&stay); err != nil {
		t.Fatalf("extra stay: %v", err)
	}
	if err := stx.Commit(ctx); err != nil {
		t.Fatalf("extra stay: %v", err)
	}
	tx := opTx(t, p, "commerce_intent")
	defer func() { _ = tx.Rollback(ctx) }()
	var pur, ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, f.pkgRev, f.iface, stay).Scan(&pur); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,
		 package_revision_id,time_accounting_mode,end_mode,status)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE') RETURNING id::text`,
		f.tenant, f.site, stay, f.iface, pur, f.svcRev, f.pkgRev).Scan(&ent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',$2,'GRANT')`, ent, at); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return ent
}

//go:build integration

package main

// COMPOSITION-ROOT tests for the Phase-3 accounting PASS — the function the tick actually calls, driven with
// synthetic tc counters. The earlier tests exercised the controlled operation directly and therefore could not
// see that the pass was reading the wrong session domain; these drive the wiring itself.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// fakeCounters serves synthetic tc class readings keyed by interface and class minor.
type fakeCounters struct {
	// byIface[iface][minor] = cumulative bytes
	byIface map[string]map[int]uint64
	reads   int
}

func newFakeCounters() *fakeCounters { return &fakeCounters{byIface: map[string]map[int]uint64{}} }

func (f *fakeCounters) set(iface string, minor int, bytes uint64) {
	if f.byIface[iface] == nil {
		f.byIface[iface] = map[int]uint64{}
	}
	f.byIface[iface][minor] = bytes
}

func (f *fakeCounters) ReadClasses(ctx context.Context, iface string) (map[int]shape.ClassBytes, error) {
	f.reads++
	out := map[int]shape.ClassBytes{}
	for minor, b := range f.byIface[iface] {
		out[minor] = shape.ClassBytes{Bytes: b}
	}
	return out, nil
}

// setCounters points the fake at the right interface names for a session's bridge and IP.
func (f *fakeCounters) forSession(t *testing.T, bridge, ip string, up, down uint64) {
	t.Helper()
	minor, ok := shape.MinorForIP(mustIP(t, ip))
	if !ok {
		t.Fatalf("no class minor for %s", ip)
	}
	f.set(bridge, minor, down)              // download counts on the bridge
	f.set(shape.IFBName(bridge), minor, up) // upload counts on the bridge's IFB
}

func mustIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad ip %s", s)
	}
	return ip
}

// The pass reads the PHASE-3 session domain and ingests a real delta. This is the test that would have caught
// the previous wiring: a legacy session id can never resolve in iam_v2.
func TestIntegration_Pass_IngestsThroughTheRealTickPath(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-2 * time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET ingress_interface='br-guest' WHERE id=$1`, sess); err != nil {
		t.Fatal(err)
	}

	counters := newFakeCounters()
	counters.forSession(t, "br-guest", "10.9.0.1", 0, 0)

	// first pass = BASELINE only: nothing is written, because there is nothing to subtract from
	if n := f.p3.accountingPass(ctx, counters, "br-lan", time.Now()); n != 0 {
		t.Fatalf("the first observation ingested %d samples; a baseline must write nothing", n)
	}
	if got := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); got != 0 {
		t.Fatalf("baseline wrote %d rows", got)
	}

	// second pass with real traffic
	counters.forSession(t, "br-guest", "10.9.0.1", 1500, 2500)
	if n := f.p3.accountingPass(ctx, counters, "br-lan", time.Now()); n != 1 {
		t.Fatalf("ingested %d samples, want 1 — the pass is not reaching the Phase-3 session domain", n)
	}
	up, down := f.usage(t, ent)
	if up != 1500 || down != 2500 {
		t.Fatalf("attributed usage = %d/%d, want 1500/2500", up, down)
	}
}

// A restart loses the in-memory baseline but must NOT lose usage and must NOT double-count it. The identity is
// derived from the sample instant, so a re-run of the same tick collapses instead of being dropped as a
// duplicate of an unrelated counter value.
func TestIntegration_Pass_SurvivesRestartWithoutLosingOrDoubling(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-2 * time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET ingress_interface='br-guest' WHERE id=$1`, sess); err != nil {
		t.Fatal(err)
	}
	counters := newFakeCounters()
	counters.forSession(t, "br-guest", "10.9.0.1", 0, 0)
	f.p3.accountingPass(ctx, counters, "br-lan", time.Now())
	counters.forSession(t, "br-guest", "10.9.0.1", 100, 200)
	f.p3.accountingPass(ctx, counters, "br-lan", time.Now().Add(-2*time.Second))

	// RESTART: a brand-new arm with no baseline at all
	restarted := &phase3{cfg: f.p3.cfg, enf: f.p3.enf, tenant: f.tenant, site: f.site}
	if n := restarted.accountingPass(ctx, counters, "br-lan", time.Now().Add(-time.Second)); n != 0 {
		t.Fatalf("the first pass after a restart ingested %d samples; it must re-baseline", n)
	}
	// more traffic after the restart is measured normally
	counters.forSession(t, "br-guest", "10.9.0.1", 150, 260)
	if n := restarted.accountingPass(ctx, counters, "br-lan", time.Now()); n != 1 {
		t.Fatalf("post-restart pass ingested %d samples, want 1 — usage after a restart was lost", n)
	}
	up, down := f.usage(t, ent)
	if up != 150 || down != 260 {
		t.Fatalf("usage = %d/%d, want 150/260 (100/200 before the restart + 50/60 after)", up, down)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 2 {
		t.Fatalf("stored samples = %d, want 2", rows)
	}
}

// Re-running the SAME tick (a retry, or two passes inside one second) stores one sample, and the baseline
// still advances — the bytes are already persisted, so this is not lost usage.
func TestIntegration_Pass_SameTickIsIdempotent(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET ingress_interface='br-guest' WHERE id=$1`, sess); err != nil {
		t.Fatal(err)
	}
	counters := newFakeCounters()
	counters.forSession(t, "br-guest", "10.9.0.1", 0, 0)
	f.p3.accountingPass(ctx, counters, "br-lan", time.Now())

	tick := time.Now()
	counters.forSession(t, "br-guest", "10.9.0.1", 700, 300)
	f.p3.accountingPass(ctx, counters, "br-lan", tick)

	// the identical tick again, from a fresh arm (as a crash-and-retry would)
	retry := &phase3{cfg: f.p3.cfg, enf: f.p3.enf, tenant: f.tenant, site: f.site}
	retry.accountingPass(ctx, counters, "br-lan", tick) // baseline
	counters.forSession(t, "br-guest", "10.9.0.1", 700, 300)
	retry.accountingPass(ctx, counters, "br-lan", tick)

	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 1 {
		t.Fatalf("stored samples = %d, want exactly 1 for one tick", rows)
	}
	up, down := f.usage(t, ent)
	if up != 700 || down != 300 {
		t.Fatalf("usage = %d/%d after a replayed tick, want 700/300", up, down)
	}
}

// A re-created tc class (counters reset to a lower value) is counted from zero rather than stored as a
// negative delta, which the controlled operation would refuse outright.
func TestIntegration_Pass_CounterResetCountsFromZero(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET ingress_interface='br-guest' WHERE id=$1`, sess); err != nil {
		t.Fatal(err)
	}
	counters := newFakeCounters()
	counters.forSession(t, "br-guest", "10.9.0.1", 0, 0)
	f.p3.accountingPass(ctx, counters, "br-lan", time.Now())
	counters.forSession(t, "br-guest", "10.9.0.1", 5000, 5000)
	f.p3.accountingPass(ctx, counters, "br-lan", time.Now().Add(-3*time.Second))

	// class re-created: counters restart from a small value
	counters.forSession(t, "br-guest", "10.9.0.1", 40, 60)
	if n := f.p3.accountingPass(ctx, counters, "br-lan", time.Now()); n != 1 {
		t.Fatalf("a counter reset ingested %d samples, want 1", n)
	}
	up, down := f.usage(t, ent)
	if up != 5040 || down != 5060 {
		t.Fatalf("usage = %d/%d, want 5040/5060 (5000/5000 plus the post-reset 40/60)", up, down)
	}
	_ = sess
}

// While dark the pass does nothing at all: no session query, no counter read, no write.
func TestIntegration_Pass_DarkDoesNothing(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	started := time.Now().Add(-time.Hour)
	ent := f.grantEntitlement(t, started, nil)
	sess := f.openSession(t, ent, f.device, started, "10.9.0.1")

	counters := newFakeCounters()
	counters.forSession(t, "br-guest", "10.9.0.1", 9999, 9999)
	var dark *phase3
	if n := dark.accountingPass(ctx, counters, "br-lan", time.Now()); n != 0 {
		t.Fatalf("a dark pass ingested %d samples", n)
	}
	if counters.reads != 0 {
		t.Fatalf("a dark pass read tc counters %d times", counters.reads)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 0 {
		t.Fatalf("a dark pass wrote %d rows", rows)
	}
	if up, down := f.usage(t, ent); up != 0 || down != 0 {
		t.Fatalf("a dark pass attributed usage: %d/%d", up, down)
	}
}

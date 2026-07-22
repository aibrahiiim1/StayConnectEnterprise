//go:build integration

package main

// COMPOSITION-ROOT tests for Phase-3 accounting, driven through the function the acctd tick actually calls,
// against a real PostgreSQL 16 with synthetic absolute tc counters.
//
// These are written adversarially on purpose. Accounting is the subsystem where a plausible-looking bug is
// invisible until a guest is billed for traffic they did not use, or the hotel loses usage nobody can
// reconstruct — so each test states the failure it exists to prevent.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// fakeCounters serves synthetic tc class readings, and can be made to fail per interface.
type fakeCounters struct {
	byIface map[string]map[int]uint64
	failing map[string]error
	reads   int
}

func newFakeCounters() *fakeCounters {
	return &fakeCounters{byIface: map[string]map[int]uint64{}, failing: map[string]error{}}
}

func (f *fakeCounters) set(iface string, minor int, bytes uint64) {
	if f.byIface[iface] == nil {
		f.byIface[iface] = map[int]uint64{}
	}
	f.byIface[iface][minor] = bytes
}

func (f *fakeCounters) ReadClasses(ctx context.Context, iface string) (map[int]shape.ClassBytes, error) {
	f.reads++
	if err, bad := f.failing[iface]; bad {
		return nil, err
	}
	out := map[int]shape.ClassBytes{}
	for minor, b := range f.byIface[iface] {
		out[minor] = shape.ClassBytes{Bytes: b}
	}
	return out, nil
}

// absolutes sets a session's cumulative counters (upload lands on the bridge's IFB, download on the bridge).
func (f *fakeCounters) absolutes(t *testing.T, bridge, ip string, up, down uint64) {
	t.Helper()
	minor, ok := shape.MinorForIP(mustIP(t, ip))
	if !ok {
		t.Fatalf("no class minor for %s", ip)
	}
	f.set(bridge, minor, down)
	f.set(shape.IFBName(bridge), minor, up)
}

func mustIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad ip %s", s)
	}
	return ip
}

// fakeEpochs stands in for netd's managed-class generations.
type fakeEpochs struct {
	epochs map[string]int64
	err    error
}

func newEpochs() *fakeEpochs { return &fakeEpochs{epochs: map[string]int64{}} }

func (f *fakeEpochs) ClassEpochs(ctx context.Context) (map[string]int64, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]int64{}
	for k, v := range f.epochs {
		out[k] = v
	}
	return out, nil
}
func (f *fakeEpochs) set(bridge, session string, gen int64) { f.epochs[bridge+"|"+session] = gen }

// live builds an entitlement + session + shaped class, the ordinary starting point for these tests.
func (f *ingestFixture) live(t *testing.T, ip, bridge string, quotaEnt ...string) (ent, sess string, ep *fakeEpochs) {
	t.Helper()
	started := time.Now().Add(-3 * time.Hour)
	ent = f.grantEntitlement(t, started, nil)
	sess = f.openSession(t, ent, f.device, started, ip)
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.sessions SET ingress_interface=$2 WHERE id=$1`, sess, bridge); err != nil {
		t.Fatal(err)
	}
	ep = newEpochs()
	ep.set(bridge, sess, 1)
	return
}

func (f *ingestFixture) checkpoint(t *testing.T, sess string) (up, down int64, epoch int64, class string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT prev_bytes_up, prev_bytes_down, source_epoch, last_classification
		   FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sess).Scan(&up, &down, &epoch, &class); err != nil {
		t.Fatalf("no checkpoint for %s: %v", sess, err)
	}
	return
}

// ORDINARY: a first observation baselines (bills nothing), the next one bills exactly the difference.
func TestIntegration_Acct_OrdinaryAbsoluteAccounting(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()

	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 2000)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("the first observation billed %d samples; it must only baseline", n)
	}
	if up, down, _, class := f.checkpoint(t, sess); up != 1000 || down != 2000 || class != "BASELINED" {
		t.Fatalf("checkpoint = %d/%d %s", up, down, class)
	}
	if u, d := f.usage(t, ent); u != 0 || d != 0 {
		t.Fatalf("a baseline billed %d/%d", u, d)
	}

	c.absolutes(t, "br-guest", "10.9.0.1", 1400, 2600)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 1 {
		t.Fatalf("accepted %d observations, want 1", n)
	}
	if u, d := f.usage(t, ent); u != 400 || d != 600 {
		t.Fatalf("usage = %d/%d, want the difference 400/600", u, d)
	}
}

// RESTART WITH UNSAMPLED TRAFFIC — the case a memory-based baseline gets wrong. The checkpoint says 1000/2000,
// the kernel advanced to 1400/2600 while acctd was stopped, and a brand-new process must bill exactly 400/600:
// nothing lost, nothing counted twice, and no "fresh zero baseline" that silently discards the gap.
func TestIntegration_Acct_RestartBillsTheGapExactlyOnce(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 2000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()) // baseline 1000/2000

	// acctd is stopped; the guest keeps browsing
	c.absolutes(t, "br-guest", "10.9.0.1", 1400, 2600)

	// a NEW process, with no memory whatsoever
	restarted := &phase3{cfg: f.p3.cfg, enf: f.p3.enf, tenant: f.tenant, site: f.site}
	if n := restarted.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 1 {
		t.Fatalf("the first pass after a restart accepted %d observations, want 1", n)
	}
	if u, d := f.usage(t, ent); u != 400 || d != 600 {
		t.Fatalf("usage after restart = %d/%d, want exactly the 400/600 used while it was down", u, d)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 1 {
		t.Fatalf("stored records = %d, want 1", rows)
	}
}

// UNCERTAIN COMMIT: the database committed but the caller never learned it. The retry — later, in another
// wall-clock second, from a fresh process — must find the persisted state and bill nothing more.
func TestIntegration_Acct_UncertainCommitIsSafeToRetry(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 100, 100)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	c.absolutes(t, "br-guest", "10.9.0.1", 500, 700)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 1 {
		t.Fatal("the observation was not accepted")
	}
	// the caller "never saw" that result; a different process retries the SAME physical observation later
	time.Sleep(1100 * time.Millisecond) // deliberately a different wall-clock second
	retry := &phase3{cfg: f.p3.cfg, enf: f.p3.enf, tenant: f.tenant, site: f.site}
	if n := retry.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("the retry billed %d observations; the delta was already stored", n)
	}
	if u, d := f.usage(t, ent); u != 400 || d != 600 {
		t.Fatalf("usage = %d/%d after a retried uncertain commit, want 400/600", u, d)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 1 {
		t.Fatalf("stored records = %d, want exactly 1", rows)
	}
}

// TWO OBSERVATIONS IN ONE WALL-CLOCK SECOND are both real usage. A second-granularity identity would have
// silently dropped the second one.
func TestIntegration_Acct_TwoObservationsInOneSecond(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	at := time.Now()
	c.absolutes(t, "br-guest", "10.9.0.1", 0, 0)
	f.p3.accountingPass(ctx, c, ep, "br-lan", at)
	c.absolutes(t, "br-guest", "10.9.0.1", 100, 100)
	f.p3.accountingPass(ctx, c, ep, "br-lan", at)
	c.absolutes(t, "br-guest", "10.9.0.1", 250, 300)
	f.p3.accountingPass(ctx, c, ep, "br-lan", at) // same instant, genuinely more traffic

	if u, d := f.usage(t, ent); u != 250 || d != 300 {
		t.Fatalf("usage = %d/%d, want 250/300 — a same-second observation was dropped", u, d)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 2 {
		t.Fatalf("stored records = %d, want 2", rows)
	}
}

// A COUNTER DECREASE WITHOUT A NEW EPOCH is ambiguous and must fail closed; a TRUSTED NEW EPOCH establishes a
// safe new baseline and later deltas count normally.
func TestIntegration_Acct_ResetRequiresATrustedEpoch(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 0, 0)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())
	c.absolutes(t, "br-guest", "10.9.0.1", 5000, 5000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	// counters drop with the SAME generation: refused, checkpoint preserved
	c.absolutes(t, "br-guest", "10.9.0.1", 40, 60)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatal("an ambiguous regression was billed")
	}
	if f.p3.AccountingDegraded() == "" {
		t.Fatal("an ambiguous regression must be visible as degraded accounting")
	}
	if up, down, _, _ := f.checkpoint(t, sess); up != 5000 || down != 5000 {
		t.Fatalf("the checkpoint was disturbed by a refused observation: %d/%d", up, down)
	}
	if u, d := f.usage(t, ent); u != 5000 || d != 5000 {
		t.Fatalf("usage changed on a refused observation: %d/%d", u, d)
	}

	// the TC owner reports a replaced class: the new absolutes become the baseline
	ep.set("br-guest", sess, 2)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatal("a trusted reset billed usage instead of re-baselining")
	}
	if _, _, epoch, class := f.checkpoint(t, sess); epoch != 2 || class != "RESET_BASELINED" {
		t.Fatalf("checkpoint after reset: epoch=%d class=%s", epoch, class)
	}
	// and traffic after the reset counts normally
	c.absolutes(t, "br-guest", "10.9.0.1", 140, 160)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 1 {
		t.Fatal("post-reset traffic was not billed")
	}
	if u, d := f.usage(t, ent); u != 5100 || d != 5100 {
		t.Fatalf("usage = %d/%d, want 5100/5100 (5000 before the reset + 100 after)", u, d)
	}
}

// A TC READ FAILURE must not be treated as "no traffic": the session is skipped, its checkpoint preserved, and
// the next successful read bills only the real difference rather than replaying history.
func TestIntegration_Acct_ReadFailureIsNotZeroUsage(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 1000, 1000)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	c.failing["br-guest"] = errors.New("tc unavailable")
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatal("a failed read produced accounting")
	}
	if f.p3.AccountingDegraded() == "" {
		t.Fatal("a failed tc read must surface as degraded accounting")
	}
	if up, down, _, _ := f.checkpoint(t, sess); up != 1000 || down != 1000 {
		t.Fatalf("a failed read disturbed the checkpoint: %d/%d", up, down)
	}

	// recovery: only the genuine difference is billed
	delete(c.failing, "br-guest")
	c.absolutes(t, "br-guest", "10.9.0.1", 1250, 1400)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 1 {
		t.Fatal("recovery did not bill the accumulated traffic")
	}
	if u, d := f.usage(t, ent); u != 250 || d != 400 {
		t.Fatalf("usage = %d/%d, want 250/400 — recovery double-counted or lost history", u, d)
	}
	if f.p3.AccountingDegraded() != "" {
		t.Fatal("degraded accounting state did not clear after recovery")
	}
}

// A class minor is reused when a class is destroyed and recreated. A checkpoint must never be inherited by a
// different session that lands on the same minor, and a session's counters must belong to ITS device.
func TestIntegration_Acct_CheckpointsAreNotInheritedAcrossSessions(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sessA, _ := f.live(t, "10.9.0.1", "br-guest")

	// a DIFFERENT session on a different device, same address (hence same class minor) after the first ended
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason='TEST' WHERE id=$1`, sessA); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Hour)
	entB := f.grantEntitlementFor(t, f.device2, started)
	sessB := f.openSession(t, entB, f.device2, started, "10.9.0.1")
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET ingress_interface='br-guest' WHERE id=$1`, sessB); err != nil {
		t.Fatal(err)
	}
	ep := newEpochs()
	ep.set("br-guest", sessB, 1)
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 9999, 9999) // the previous guest's high counters

	// the new session must BASELINE against those counters, not bill them
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("a reused class minor billed %d observations of another guest's traffic", n)
	}
	if u, d := f.usage(t, entB); u != 0 || d != 0 {
		t.Fatalf("the new session was billed %d/%d for the previous guest's usage", u, d)
	}
	// the checkpoint belongs to the NEW session alone — the ended one never acquires or shares it
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sessB); n != 1 {
		t.Fatalf("the new session has %d checkpoints, want exactly its own", n)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sessA); n != 0 {
		t.Fatalf("the ended session acquired %d checkpoints", n)
	}
}

// Two acctd instances observing the same counters must not both bill the delta.
func TestIntegration_Acct_TwoInstancesBillOnce(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 0, 0)
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())

	instanceB := &phase3{cfg: f.p3.cfg, enf: f.p3.enf, tenant: f.tenant, site: f.site}
	c.absolutes(t, "br-guest", "10.9.0.1", 800, 900)
	n1 := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())
	n2 := instanceB.accountingPass(ctx, c, ep, "br-lan", time.Now())
	if n1+n2 != 1 {
		t.Fatalf("two instances billed %d observations for one delta", n1+n2)
	}
	if u, d := f.usage(t, ent); u != 800 || d != 900 {
		t.Fatalf("usage = %d/%d, want 800/900 billed exactly once", u, d)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); rows != 1 {
		t.Fatalf("stored records = %d, want 1", rows)
	}
}

// Fail-closed cases that must never produce accounting.
func TestIntegration_Acct_FailsClosedOnBadScopeAndBinding(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	_, sess, _ := f.live(t, "10.9.0.1", "br-guest")

	// wrong tenant/site
	other := newIngest(t, 0)
	if _, err := other.p3.ingestAbsolute(ctx, phase3Session{ID: sess, DeviceID: f.device}, "br-guest", 1, 1, 10, 10, time.Now()); err == nil {
		t.Fatal("a cross-scope session was accounted")
	}
	// wrong device for the session's counters
	if _, err := f.p3.ingestAbsolute(ctx, phase3Session{ID: sess, DeviceID: f.device2}, "br-guest", 1, 1, 10, 10, time.Now()); err == nil {
		t.Fatal("counters from another device were accepted for this session")
	}
	// an ended session whose binding interval has closed has no owner for later traffic
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now() - interval '1 minute' WHERE id=$1`, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p3.ingestAbsolute(ctx, phase3Session{ID: sess, DeviceID: f.device}, "br-guest", 1, 1, 10, 10, time.Now()); err == nil {
		t.Fatal("traffic after a session ended was attributed to it")
	}
}

// While dark the pass reads nothing, asks nothing and writes nothing.
func TestIntegration_Acct_DarkDoesNothing(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ent, sess, ep := f.live(t, "10.9.0.1", "br-guest")
	c := newFakeCounters()
	c.absolutes(t, "br-guest", "10.9.0.1", 9999, 9999)

	var dark *phase3
	if n := dark.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("a dark pass billed %d observations", n)
	}
	if c.reads != 0 {
		t.Fatalf("a dark pass read tc counters %d times", c.reads)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); n != 0 {
		t.Fatalf("a dark pass wrote %d rows", n)
	}
	if n := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_checkpoints WHERE session_id=$1`, sess); n != 0 {
		t.Fatalf("a dark pass wrote %d checkpoints", n)
	}
	if u, d := f.usage(t, ent); u != 0 || d != 0 {
		t.Fatalf("a dark pass attributed %d/%d", u, d)
	}
}

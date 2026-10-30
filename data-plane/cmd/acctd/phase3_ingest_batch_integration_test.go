//go:build integration

package main

// Capacity acceptance 2026-10-30: acctd sent one transaction per live session per tick, and for sessions whose counters had not moved each of them was still a
// row lock (a write and a commit) answered "replay". The pass now sends only what changed, and what changed in a transaction per chunk, with the
// one-by-one path as the fallback. The meaning of every observation is unchanged; these tests hold it to the existing one.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

func minorFor(t *testing.T, ip string) int {
	t.Helper()
	m, ok := shape.MinorForIP(mustIP(t, ip))
	if !ok {
		t.Fatalf("no class minor for %s", ip)
	}
	return m
}

type batchSess struct {
	ent, sess, ip string
}

// newStay adds a Stay: the fixture allows one live entitlement per Stay, and a batch needs several sessions with their own entitlements.
func (f *ingestFixture) newStay(t *testing.T, n int) string {
	t.Helper()
	ctx := context.Background()
	tx := opTx(t, f.pool, "stay")
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version)
	   VALUES (gen_random_uuid(), $1,$2,$3,'RB'||($4::int)::text||'-'||substr(gen_random_uuid()::text,1,6),'SB'||($4::int)::text||'-'||substr(gen_random_uuid()::text,1,6),'IN_HOUSE',1,0) RETURNING id::text`,
		f.tenant, f.site, f.iface, n).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *ingestFixture) liveMany(t *testing.T, n int, bridge string, ep *fakeEpochs) []batchSess {
	t.Helper()
	out := make([]batchSess, 0, n)
	for i := 0; i < n; i++ {
		ip := fmt.Sprintf("10.9.%d.%d", 1+i/200, 1+i%200)
		started := time.Now().Add(-3 * time.Hour)
		saved := f.stay
		f.stay = f.newStay(t, i)
		ent := f.grantEntitlement(t, started, nil)
		f.stay = saved
		sess := f.openSessionOn(t, ent, f.device, started, ip, bridge)
		ep.set(bridge, sess, 1)
		out = append(out, batchSess{ent: ent, sess: sess, ip: ip})
	}
	return out
}

func (f *ingestFixture) commits(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), `SELECT xact_commit FROM pg_stat_database WHERE datname=current_database()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Several sessions in one pass: baselines, then exact deltas, each billed to its own entitlement, however they are grouped into transactions.
func TestIntegration_Acct_ABatchBillsEverySessionExactly(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ep := newEpochs()
	ss := f.liveMany(t, 7, "br-guest", ep)
	c := newFakeCounters()
	for i, s := range ss {
		c.absolutes(t, "br-guest", s.ip, uint64(1000+i), uint64(2000+i))
	}
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("baselines billed %d", n)
	}
	for i, s := range ss {
		if up, down, _, class := f.checkpoint(t, s.sess); up != int64(1000+i) || down != int64(2000+i) || class != "BASELINED" {
			t.Fatalf("session %d checkpoint = %d/%d %s", i, up, down, class)
		}
	}
	for i, s := range ss {
		c.absolutes(t, "br-guest", s.ip, uint64(1000+i+100*(i+1)), uint64(2000+i+50*(i+1)))
	}
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != len(ss) {
		t.Fatalf("accepted %d, want %d", n, len(ss))
	}
	for i, s := range ss {
		if u, d := f.usage(t, s.ent); u != int64(100*(i+1)) || d != int64(50*(i+1)) {
			t.Fatalf("session %d billed %d/%d, want %d/%d", i, u, d, 100*(i+1), 50*(i+1))
		}
		if rows := f.countRows(t, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, s.sess); rows != 1 {
			t.Fatalf("session %d has %d records, want 1", i, rows)
		}
	}
}

// One session in the middle of the chunk is refused (it ended a moment ago): the chunk is rolled back and replayed one by one, the others are billed exactly,
// nothing is billed twice, and the refusal is reported (the pass is degraded) rather than lost.
func TestIntegration_Acct_AnEndedSessionInAChunkCostsItsNeighboursNothing(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ep := newEpochs()
	ss := f.liveMany(t, 7, "br-guest", ep)
	ended := map[int]bool{0: true, 3: true, 6: true} // first, middle and last position of the chunk
	c := newFakeCounters()
	for _, s := range ss {
		c.absolutes(t, "br-guest", s.ip, 1000, 1000)
	}
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now())
	for _, s := range ss {
		c.absolutes(t, "br-guest", s.ip, 1300, 1700)
	}
	// These sessions end after the pass listed them live and before the observation is sent: the controlled operation refuses them.
	for i := range ended {
		if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason='TEST' WHERE id=$1`, ss[i].sess); err != nil {
			t.Fatalf("end the session: %v", err)
		}
	}
	// Force it into the chunk anyway: a pass built from the inventory taken before the end.
	items := make([]ingestItem, 0, len(ss))
	for _, s := range ss {
		items = append(items, ingestItem{s: phase3Session{ID: s.sess, DeviceID: f.device}, bridge: "br-guest", minor: minorFor(t, s.ip), epoch: 1, up: 1300, down: 1700})
	}
	res := f.p3.ingestBatch(ctx, items, time.Now())
	for i, r := range res {
		if ended[i] {
			if r.err == nil {
				t.Fatalf("the ended session %d's observation was accepted", i)
			}
			continue
		}
		if r.err != nil || (r.class != "ACCEPTED" && r.class != "DELAYED") {
			t.Fatalf("neighbour %d: %q %v", i, r.class, r.err)
		}
	}
	for i, s := range ss {
		u, d := f.usage(t, s.ent)
		if ended[i] {
			if u != 0 || d != 0 {
				t.Fatalf("the refused session was billed %d/%d", u, d)
			}
			continue
		}
		if u != 300 || d != 700 {
			t.Fatalf("session %d billed %d/%d, want 300/700 exactly once", i, u, d)
		}
	}
	// Replaying the same batch bills nothing more (the replay classification), including for the neighbours.
	res = f.p3.ingestBatch(ctx, items, time.Now())
	for i, r := range res {
		if !ended[i] && (r.err != nil || (r.class != "REPLAY:ACCEPTED" && r.class != "DUPLICATE")) {
			t.Fatalf("replay %d: %q %v", i, r.class, r.err)
		}
	}
	for i, s := range ss {
		if ended[i] {
			continue
		}
		if u, d := f.usage(t, s.ent); u != 300 || d != 700 {
			t.Fatalf("after a replay session %d billed %d/%d, want 300/700", i, u, d)
		}
	}
}

// Counters that have not moved are not sent again: a pass over unchanged sessions commits (almost) nothing, and a pass over a few moving ones sends only those.
func TestIntegration_Acct_UnchangedCountersAreNotSentAgain(t *testing.T) {
	f := newIngest(t, 0)
	ctx := context.Background()
	ep := newEpochs()
	ss := f.liveMany(t, 12, "br-guest", ep)
	c := newFakeCounters()
	for _, s := range ss {
		c.absolutes(t, "br-guest", s.ip, 500, 500)
	}
	f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()) // baselines
	for _, s := range ss[:3] {
		c.absolutes(t, "br-guest", s.ip, 900, 800)
	}
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 3 {
		t.Fatalf("accepted %d, want the 3 that moved", n)
	}
	before := f.commits(t)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("an unchanged pass accepted %d", n)
	}
	if delta := f.commits(t) - before; delta > 4 {
		t.Fatalf("a pass over 12 unchanged sessions made %d commits; it should send nothing (a read or two only)", delta)
	}
	// Traffic moves again on one of them: exactly that one is sent and billed.
	c.absolutes(t, "br-guest", ss[7].ip, 600, 650)
	if n := f.p3.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 1 {
		t.Fatalf("accepted %d, want 1", n)
	}
	if u, d := f.usage(t, ss[7].ent); u != 100 || d != 150 {
		t.Fatalf("billed %d/%d, want 100/150", u, d)
	}
	// A restarted process has no memory: it asks again and the database answers with the replay, billing nothing twice.
	restarted := &phase3{cfg: f.p3.cfg, enf: f.p3.enf, tenant: f.tenant, site: f.site}
	if n := restarted.accountingPass(ctx, c, ep, "br-lan", time.Now()); n != 0 {
		t.Fatalf("a restarted pass accepted %d, want 0 (all replays)", n)
	}
	if u, d := f.usage(t, ss[7].ent); u != 100 || d != 150 {
		t.Fatalf("after a restart billed %d/%d, want 100/150", u, d)
	}
}

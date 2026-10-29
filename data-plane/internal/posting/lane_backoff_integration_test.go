//go:build integration

package posting

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Audit AD-41 (P6 M1): backoff on NOT_SENT and on a decline, order unchanged while the head backs off, parking after N
// declines, and the audited way back. Scripted transport only: nothing is sent.

type downTransport struct{ n int }

func (d *downTransport) SendPS(context.Context, string, int64, string) (*PA, error) {
	d.n++
	return nil, &handoffError{code: ErrTransportUnavailable, msg: "link down", reason: "LINK_DOWN", notSent: true}
}

// poisonCurrency makes one posting refuse at execute time (the pinned currency no longer matches the interface's)
// without touching its neighbours. The immutability triggers are bypassed in this one fixture transaction only.
func setPostingCurrency(t *testing.T, p *pgxpool.Pool, postingID, cur string) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.pms_postings SET currency=$2 WHERE id=$1`, postingID, cur); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func outboxRow(t *testing.T, p *pgxpool.Pool, postingID string) (state string, declines, notSent int, parked string) {
	t.Helper()
	var pr *string
	if err := p.QueryRow(context.Background(), `SELECT state, decline_count, not_sent_count, parked_reason
		FROM iam_v2.posting_outbox WHERE posting_id=$1`, postingID).Scan(&state, &declines, &notSent, &pr); err != nil {
		t.Fatal(err)
	}
	if pr != nil {
		parked = *pr
	}
	return
}

func makeDue(t *testing.T, p *pgxpool.Pool, iface string) {
	t.Helper()
	mustExec(t, p, `UPDATE iam_v2.posting_outbox SET not_before = now() - interval '1 second' WHERE pms_interface_id=$1 AND state='QUEUED'`, iface)
}

func TestLaneBackoff_NotSentBacksOffInsteadOfSpinning(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	tr := &downTransport{}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	ctx := context.Background()
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := e.RunOnce(ctx, s.tenant, s.site, s.iface); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if tr.n != 1 {
		t.Fatalf("ten passes inside a minute must make one attempt while the link is down, made %d", tr.n)
	}
	if n := attemptCount(t, p, id); n != 1 {
		t.Fatalf("one attempt row, got %d", n)
	}
	state, _, ns, _ := outboxRow(t, p, id)
	if state != "QUEUED" || ns != 1 {
		t.Fatalf("a NOT_SENT stays queued with its count: %s %d", state, ns)
	}
	// Once the wait has passed it is tried again, and the wait doubles.
	makeDue(t, p, s.iface)
	if _, err := e.RunOnce(ctx, s.tenant, s.site, s.iface); err != nil {
		t.Fatal(err)
	}
	if tr.n != 2 {
		t.Fatalf("a due row is tried again, attempts=%d", tr.n)
	}
	wait := scan1[float64](t, p, `SELECT extract(epoch FROM not_before - now()) FROM iam_v2.posting_outbox WHERE posting_id=$1`, id)
	if wait < 2 || wait > 5 {
		t.Fatalf("the second wait is about 4 s (doubling from 2), got %.1f", wait)
	}
	// NOT_SENT never parks: the link is not the row's fault.
	for i := 0; i < 15; i++ {
		makeDue(t, p, s.iface)
		_, _ = e.RunOnce(ctx, s.tenant, s.site, s.iface)
	}
	if state, _, _, _ := outboxRow(t, p, id); state != "QUEUED" {
		t.Fatalf("NOT_SENT must never park, state=%s", state)
	}
	if w := scan1[float64](t, p, `SELECT extract(epoch FROM not_before - now()) FROM iam_v2.posting_outbox WHERE posting_id=$1`, id); w > 301 {
		t.Fatalf("the wait is capped at 5 minutes, got %.0f s", w)
	}
}

func TestLaneBackoff_ARefusedHeadIsParkedAfterNAndTheLaneMovesOn(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	s2 := s.withCharge(t, p)
	tr := &stubTransport{}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	ctx := context.Background()

	head, err := e.CreatePosting(ctx, s.pinned(idem(t)+"-head"))
	if err != nil {
		t.Fatal(err)
	}
	next, err := e.CreatePosting(ctx, s2.pinned(idem(t)+"-next"))
	if err != nil {
		t.Fatal(err)
	}
	setPostingCurrency(t, p, head, "EUR")

	// A site-level setting of 3 keeps the test short; the default is 10.
	mustExec(t, p, `SELECT iam_v2.posting_lane_settings_set($1,$2,0,3,'test operator','shorter park for the test')`, s.tenant, s.site)

	out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if CodeOf(err) != ErrCurrencyMismatch || out.Result != "DECLINED" {
		t.Fatalf("the head is declined, got %+v %v", out, err)
	}
	// While the head backs off, the row behind it is NOT claimed past it: the lane's order is the contract.
	out, err = e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if err != nil || out.Claimed {
		t.Fatalf("nothing is claimed while the head backs off: %+v %v", out, err)
	}
	if tr.count() != 0 {
		t.Fatalf("nothing was sent: %d", tr.count())
	}
	makeDue(t, p, s.iface)
	if _, err := e.RunOnce(ctx, s.tenant, s.site, s.iface); CodeOf(err) != ErrCurrencyMismatch {
		t.Fatalf("second decline: %v", err)
	}
	makeDue(t, p, s.iface)
	out, err = e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if CodeOf(err) != ErrCurrencyMismatch || !out.Parked {
		t.Fatalf("the third decline parks the row: %+v %v", out, err)
	}
	state, declines, _, why := outboxRow(t, p, head)
	if state != "HELD_RECOVERY" || declines != 3 || !strings.Contains(why, "currency") {
		t.Fatalf("parked with the reason recorded: %s %d %q", state, declines, why)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_outbox_park_events WHERE posting_id=$1 AND event='PARKED'`, head); n != 1 {
		t.Fatalf("one PARKED ledger row, got %d", n)
	}
	// The lane moves on: the row behind is posted.
	out, err = e.RunOnce(ctx, s.tenant, s.site, s.iface)
	if err != nil || out.Result != "POSTED" || out.PostingID != next {
		t.Fatalf("the next row is posted once the head is parked: %+v %v", out, err)
	}
	if n := attemptCount(t, p, head); n != 0 {
		t.Fatalf("a refused row never produced an attempt or a P#: %d", n)
	}
}

func TestLaneBackoff_ParkSettingDefaultsToTenAndIsBounded(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	if got := scan1[int](t, p, `SELECT park_after_declines FROM iam_v2.posting_lane_settings_get($1,$2)`, s.tenant, s.site); got != 10 {
		t.Fatalf("default is 10, got %d", got)
	}
	for _, bad := range []int{0, 1001, -1} {
		if _, err := p.Exec(context.Background(), `SELECT iam_v2.posting_lane_settings_set($1,$2,0,$3,'op','reason ok')`, s.tenant, s.site, bad); err == nil {
			t.Fatalf("%d must be refused", bad)
		}
	}
	if _, err := p.Exec(context.Background(), `SELECT iam_v2.posting_lane_settings_set($1,$2,0,5,'','reason ok')`, s.tenant, s.site); err == nil {
		t.Fatal("a change without an operator is refused")
	}
	mustExec(t, p, `SELECT iam_v2.posting_lane_settings_set($1,$2,0,5,'op','raise it')`, s.tenant, s.site)
	if _, err := p.Exec(context.Background(), `SELECT iam_v2.posting_lane_settings_set($1,$2,0,6,'op','stale version')`, s.tenant, s.site); err == nil {
		t.Fatal("a stale version is refused")
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_lane_settings_changes WHERE tenant_id=$1 AND site_id=$2`, s.tenant, s.site); n != 1 {
		t.Fatalf("the change is audited, rows=%d", n)
	}
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.posting_lane_settings_changes SET reason='x'`); err == nil {
		t.Fatal("the change log is append-only")
	}
}

func TestLaneBackoff_RequeueParkedNeedsAnOperatorAndRefusesAnythingSent(t *testing.T) {
	p := pool(t)
	s := seedProperty(t, p, "USD", 2)
	tr := &stubTransport{}
	e := NewEngine(liveCfg, NewRepo(p), tr)
	ctx := context.Background()
	id, err := e.CreatePosting(ctx, s.pinned(idem(t)))
	if err != nil {
		t.Fatal(err)
	}
	setPostingCurrency(t, p, id, "EUR")
	mustExec(t, p, `SELECT iam_v2.posting_lane_settings_set($1,$2,0,1,'test operator','park at the first refusal')`, s.tenant, s.site)
	if out, _ := e.RunOnce(ctx, s.tenant, s.site, s.iface); !out.Parked {
		t.Fatalf("parked at the first refusal: %+v", out)
	}
	outbox := scan1[string](t, p, `SELECT id::text FROM iam_v2.posting_outbox WHERE posting_id=$1`, id)

	if _, err := p.Exec(ctx, `SELECT iam_v2.posting_outbox_requeue_parked($1,$2,$3,'','fixed the mapping')`, s.tenant, s.site, outbox); err == nil {
		t.Fatal("an operator is required")
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.posting_outbox_requeue_parked($1,$2,$3,'op','')`, s.tenant, s.site, outbox); err == nil {
		t.Fatal("a reason is required")
	}
	// A plain UPDATE out of HELD_RECOVERY is still refused by the recovery gate.
	if _, err := p.Exec(ctx, `UPDATE iam_v2.posting_outbox SET state='QUEUED' WHERE id=$1`, outbox); err == nil {
		t.Fatal("the recovery gate must still refuse an ordinary release")
	}
	setPostingCurrency(t, p, id, "USD") // repaired
	mustExec(t, p, `SELECT iam_v2.posting_outbox_requeue_parked($1,$2,$3,'front office','mapping corrected')`, s.tenant, s.site, outbox)
	if state, declines, _, why := outboxRow(t, p, id); state != "QUEUED" || declines != 0 || why != "" {
		t.Fatalf("released: %s %d %q", state, declines, why)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_outbox_park_events WHERE posting_id=$1`, id); n != 2 {
		t.Fatalf("PARKED and REQUEUED are both in the ledger, got %d", n)
	}
	if out, err := e.RunOnce(ctx, s.tenant, s.site, s.iface); err != nil || out.Result != "POSTED" {
		t.Fatalf("a released row posts: %+v %v", out, err)
	}
	// Anything that is HELD_RECOVERY for another reason is not releasable here.
	if _, err := p.Exec(ctx, `SELECT iam_v2.posting_outbox_requeue_parked($1,$2,$3,'op','try anyway')`, s.tenant, s.site, outbox); err == nil {
		t.Fatal("a DONE row is not a parked row")
	}
}

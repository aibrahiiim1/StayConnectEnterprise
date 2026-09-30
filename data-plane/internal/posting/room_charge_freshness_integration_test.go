//go:build integration

package posting

// ROOM-CHARGE FINANCIAL FRESHNESS (decision D48, migration 0103).
//
// One definition decides whether money may move, and every stage asks it: the offer, the posting's creation, the
// worker's claim, abort-before-send, the attempt's creation and pmsd's last check before the first byte. These
// tests put the interface in each state that matters and ask ALL SIX stages, so a stage that disagrees fails here.
//
// The states, and what they prove:
//   healthy quiet feed      no live guest event for 30 minutes (beyond the old 15-minute rule) on a connected,
//                           heartbeat-healthy, CONTINUOUS, IN_SYNC interface with a recent complete resync: fresh
//   live event, old mirror  a guest event a moment ago, but no complete resync within the mirror age: STALE -- a
//                           live event is not proof that the whole mirror has missed nothing
//   resync never succeeded  the last complete resync is past the 4-hour bound: STALE (fail closed)
//   custom bound            the per-interface setting is honoured
//   heartbeat stale, disconnected, gap detected, resync in progress, generation part-published, revision
//   mismatch               each STALE with its own code

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type freshCase struct {
	name  string
	apply func(t *testing.T, p *pgxpool.Pool, s rcScope)
	block string // "" = fresh
}

func setRuntime(t *testing.T, p *pgxpool.Pool, s rcScope, set string) {
	t.Helper()
	mustExec(t, p, `UPDATE iam_v2.pms_interface_runtime SET `+set+`, updated_at=now() WHERE pms_interface_id=$1`, s.iface)
}

func freshnessCases() []freshCase {
	return []freshCase{
		{"healthy quiet feed beyond 15 minutes", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			setRuntime(t, p, s, `last_valid_event_at = now() - interval '30 minutes', last_complete_sync_at = now() - interval '90 minutes'`)
		}, ""},
		{"live event just now but no complete resync within the mirror age", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			setRuntime(t, p, s, `last_valid_event_at = now(), last_complete_sync_at = now() - interval '5 hours'`)
		}, "FINANCIAL_MIRROR_STALE"},
		{"no complete resync succeeded before the 4-hour bound", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			setRuntime(t, p, s, `last_complete_sync_at = now() - interval '4 hours 1 minute'`)
		}, "FINANCIAL_MIRROR_STALE"},
		{"the per-interface bound is honoured", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			mustExec(t, p, `SELECT iam_v2.p4_set_financial_mirror_max_age($1,$2,$3,7200,'measured at this property',$4)`, s.tenant, s.site, s.iface, s.op)
			setRuntime(t, p, s, `last_complete_sync_at = now() - interval '3 hours'`)
		}, "FINANCIAL_MIRROR_STALE"},
		{"heartbeat stale", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			setRuntime(t, p, s, `last_heartbeat_at = now() - interval '10 minutes'`)
		}, "TRANSPORT_HEARTBEAT_STALE"},
		{"transport disconnected", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			setRuntime(t, p, s, `transport_status = 'DISCONNECTED'`)
		}, "TRANSPORT_DISCONNECTED"},
		{"gap detected", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			setRuntime(t, p, s, `continuity_status = 'GAP_DETECTED', discontinuity_detected_at = now(),
				sync_status = 'RESYNC_REQUIRED', resync_requested_at = now(), resync_started_at = NULL`)
		}, "CONTINUITY_GAP_DETECTED"},
		{"resync in progress", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			setRuntime(t, p, s, `sync_status = 'RESYNC_IN_PROGRESS', resync_requested_at = now() - interval '1 minute', resync_started_at = now()`)
		}, "SYNC_RESYNC_IN_PROGRESS"},
		{"resync generation part-published", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			setRuntime(t, p, s, `resync_generation_seq = published_resync_generation + 1`)
		}, "PIN_RESYNC_IN_FLIGHT"},
		{"revision mismatch", func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			other := scan1[string](t, p, `INSERT INTO iam_v2.pms_interface_revisions
				(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,config)
				VALUES ($1,$2,$3,99,'UTC','RESERVATION','{"heartbeat_timeout_ms":300000,"complete_sync_ms":86400000}') RETURNING id::text`,
				s.tenant, s.site, s.iface)
			setRuntime(t, p, s, `pinned_revision_id = '`+other+`'::uuid`)
		}, "PIN_REVISION_MISMATCH"},
	}
}

func freshnessBlock(t *testing.T, p *pgxpool.Pool, s rcScope) string {
	t.Helper()
	return scan1[string](t, p, `SELECT coalesce(iam_v2.p4_interface_freshness_block($1,$2,$3,$4,now()),'')`,
		s.tenant, s.site, s.iface, s.rev)
}

// ALL SIX STAGES, EVERY STATE.
func TestD48_AllSixRoomChargeStagesAgree(t *testing.T) {
	ctx := context.Background()
	for _, c := range freshnessCases() {
		c := c
		t.Run(c.name, func(t *testing.T) {
			admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
			fresh := c.block == ""

			// (0) the definition itself
			s0 := seedRoomCharge(t, admin)
			c.apply(t, admin, s0)
			if got := freshnessBlock(t, admin, s0); got != c.block {
				t.Fatalf("p4_interface_freshness_block = %q, want %q", got, c.block)
			}

			// (1) offer eligibility and (2) posting creation
			if got := scan1[bool](t, admin, `SELECT iam_v2.p4_room_charge_interface_fresh($1,$2,$3)`, s0.tenant, s0.site, s0.iface); got != fresh {
				t.Fatalf("offer: fresh=%v, want %v", got, fresh)
			}
			_, err := admin.Exec(ctx, `SELECT iam_v2.p4_create_room_charge_posting($1,$2,$3)`, s0.tenant, s0.site, s0.settle)
			if fresh && err != nil {
				t.Fatalf("posting creation refused on a fresh interface: %v", err)
			}
			if !fresh && (err == nil || !strings.Contains(err.Error(), "INTERFACE_NOT_FRESH")) {
				t.Fatalf("posting creation on a stale interface: %v", err)
			}

			// (3) worker claim and (4) abort-before-send: the posting was created while fresh; the state
			// changes before the worker runs.
			s1 := seedRoomCharge(t, admin)
			posting := createRoomCharge(t, admin, s1)
			c.apply(t, admin, s1)
			tr := &pmsdLike{db: admin, iface: s1.iface, answer: okAnswer}
			out, err := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr).RunOnce(ctx, s1.tenant, s1.site, s1.iface)
			if fresh {
				if err != nil || out.Result != "POSTED" {
					t.Fatalf("worker on a fresh interface: %+v %v", out, err)
				}
			} else {
				if err != nil || out.Result != "ABORTED" || len(tr.writtenBodies()) != 0 {
					t.Fatalf("worker on a stale interface must abort before sending: %+v %v", out, err)
				}
				if r := scan1[string](t, admin, `SELECT reason FROM iam_v2.posting_presend_aborts WHERE posting_id=$1`, posting); r != "DATA_STALE" {
					t.Fatalf("abort reason %q, want DATA_STALE", r)
				}
			}

			// (5) attempt creation: the attempt row itself is refused on a stale interface
			s2 := seedRoomCharge(t, admin)
			p2 := createRoomCharge(t, admin, s2)
			mustExec(t, admin, `UPDATE iam_v2.posting_outbox SET state='IN_FLIGHT' WHERE posting_id=$1`, p2)
			c.apply(t, admin, s2)
			_, err = admin.Exec(ctx, `INSERT INTO iam_v2.posting_attempts
				(tenant_id, site_id, internal_posting_id, pms_interface_id, attempt_no, p_number, rn, g_number, sent_at, ps_sha256)
				VALUES ($1,$2,$3,$4,1,'990001','1421',$5,now(),repeat('a',64))`, s2.tenant, s2.site, p2, s2.iface, s2.reservation)
			if fresh && err != nil {
				t.Fatalf("attempt creation refused on a fresh interface: %v", err)
			}
			if !fresh && (err == nil || !strings.Contains(err.Error(), "INTERFACE_NOT_FRESH")) {
				t.Fatalf("attempt creation on a stale interface: %v", err)
			}

			// (6) pmsd's pre-wire authorisation: the state changes AFTER the attempt, before the first byte
			s3 := seedRoomCharge(t, admin)
			createRoomCharge(t, admin, s3)
			tr3 := &pmsdLike{db: admin, iface: s3.iface, answer: okAnswer, before: func(int) { c.apply(t, admin, s3) }}
			out, _ = NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr3).RunOnce(ctx, s3.tenant, s3.site, s3.iface)
			if len(tr3.verdicts) != 1 {
				t.Fatalf("pmsd asked %d times", len(tr3.verdicts))
			}
			if fresh && (tr3.verdicts[0] != "AUTHORISED" || out.Result != "POSTED") {
				t.Fatalf("pmsd on a fresh interface: %v %+v", tr3.verdicts, out)
			}
			if !fresh && (tr3.verdicts[0] != "INTERFACE_NOT_FRESH" || len(tr3.writtenBodies()) != 0 || out.Result != "NOT_SENT") {
				t.Fatalf("pmsd on a stale interface writes nothing: %v %+v", tr3.verdicts, out)
			}
		})
	}
}

// pmsd's question: is the financial mirror due to be proven again (half the bound)?
func TestD48_FinancialResyncDueAtHalfTheBound(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	due := func() bool {
		return scan1[bool](t, admin, `SELECT iam_v2.p4_financial_resync_due($1,$2,$3)`, s.tenant, s.site, s.iface)
	}
	setRuntime(t, admin, s, `last_complete_sync_at = now() - interval '1 hour 50 minutes'`)
	if due() {
		t.Fatal("not due before half the 4-hour bound")
	}
	setRuntime(t, admin, s, `last_complete_sync_at = now() - interval '2 hours 1 minute'`)
	if !due() {
		t.Fatal("due once half the bound has passed")
	}
	setRuntime(t, admin, s, `sync_status = 'RESYNC_IN_PROGRESS', resync_requested_at = now() - interval '1 minute', resync_started_at = now()`)
	if due() {
		t.Fatal("never due while a resync is already in flight")
	}
	setRuntime(t, admin, s, `sync_status = 'IN_SYNC', resync_started_at = NULL, transport_status = 'DISCONNECTED'`)
	if due() {
		t.Fatal("never due on a disconnected link")
	}
}

// The setting: bounded, reasoned, attributed, logged.
func TestD48_MirrorAgeSettingIsValidatedAndAudited(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	if v := scan1[int](t, admin, `SELECT iam_v2.p4_financial_mirror_max_age_seconds($1,$2,$3)`, s.tenant, s.site, s.iface); v != 14400 {
		t.Fatalf("default %d, want 14400 (4 hours)", v)
	}
	for _, bad := range []struct {
		secs   int
		reason string
		want   string
	}{
		{1800, "too short", "MIRROR_AGE_OUT_OF_RANGE"},
		{90000, "too long", "MIRROR_AGE_OUT_OF_RANGE"},
		{7200, "no", "MIRROR_AGE_REASON"},
	} {
		_, err := admin.Exec(context.Background(), `SELECT iam_v2.p4_set_financial_mirror_max_age($1,$2,$3,$4,$5,$6)`,
			s.tenant, s.site, s.iface, bad.secs, bad.reason, s.op)
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Fatalf("%d/%q: %v, want %s", bad.secs, bad.reason, err, bad.want)
		}
	}
	mustExec(t, admin, `SELECT iam_v2.p4_set_financial_mirror_max_age($1,$2,$3,7200,'measured at this property',$4)`, s.tenant, s.site, s.iface, s.op)
	mustExec(t, admin, `SELECT iam_v2.p4_set_financial_mirror_max_age($1,$2,$3,10800,'raised after a quiet weekend',$4)`, s.tenant, s.site, s.iface, s.op)
	if n := scan1[int](t, admin, `SELECT count(*)::int FROM iam_v2.pms_interface_financial_setting_changes
		WHERE pms_interface_id=$1 AND changed_by=$2::uuid`, s.iface, s.op); n != 2 {
		t.Fatalf("every change is logged with its author: %d", n)
	}
	if _, err := admin.Exec(context.Background(), `DELETE FROM iam_v2.pms_interface_financial_setting_changes WHERE pms_interface_id=$1`, s.iface); err == nil {
		t.Fatal("the change log is append-only")
	}
}

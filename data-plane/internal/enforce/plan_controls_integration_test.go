//go:build integration

package enforce

// Plan controls, exercised through the same sweep acctd runs, the admission function scd uses and the ingestion
// operation the meter uses, so what passes here is what a guest gets.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// revisePlan publishes a new revision (revisions are immutable) and points the entitlement at it.
func revisePlan(t *testing.T, p *pgxpool.Pool, f fixture, ent string, set map[string]any) string {
	t.Helper()
	ctx := context.Background()
	cols := []string{"down_kbps", "up_kbps", "max_concurrent_devices", "device_limit_policy", "time_accounting_mode",
		"time_quota_seconds", "data_quota_bytes", "idle_timeout_seconds", "max_continuous_session_seconds"}
	sel := make([]string, 0, len(cols))
	args := []any{f.svcRev}
	for _, c := range cols {
		if v, ok := set[c]; ok {
			args = append(args, v)
			sel = append(sel, "$"+itoa(len(args)))
		} else {
			sel = append(sel, "r."+c)
		}
	}
	var rev string
	q := `INSERT INTO iam_v2.service_plan_revisions (id, tenant_id, site_id, service_plan_id, revision_no, ` +
		strings.Join(cols, ", ") + `)
		SELECT gen_random_uuid(), r.tenant_id, r.site_id, r.service_plan_id,
		       (SELECT max(x.revision_no) + 1 FROM iam_v2.service_plan_revisions x WHERE x.service_plan_id = r.service_plan_id), ` +
		strings.Join(sel, ", ") + `
		  FROM iam_v2.service_plan_revisions r WHERE r.id = $1 RETURNING id::text`
	if err := p.QueryRow(ctx, q, args...).Scan(&rev); err != nil {
		t.Fatalf("publish plan revision: %v", err)
	}
	if ent != "" {
		if _, err := p.Exec(ctx, `UPDATE iam_v2.entitlements SET service_plan_revision_id=$2 WHERE id=$1`, ent, rev); err != nil {
			t.Fatalf("point the entitlement at the revision: %v", err)
		}
	}
	return rev
}

func itoa(n int) string {
	const d = "0123456789"
	if n < 10 {
		return d[n : n+1]
	}
	return itoa(n/10) + d[n%10:n%10+1]
}

func rolePool(t *testing.T, role string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+role)
		return err
	}
	rp, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(rp.Close)
	return rp
}

func scalar[T any](t *testing.T, p *pgxpool.Pool, q string, args ...any) T {
	t.Helper()
	var v T
	if err := p.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// seedObserved records the session as already observed online for `observed`, as the accrual watermark would.
func seedObserved(t *testing.T, p *pgxpool.Pool, f fixture, sess string, observed time.Duration) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `INSERT INTO iam_v2.session_online_watermarks
		   (tenant_id, site_id, session_id, accounted_through, accounted_seconds)
		   VALUES ($1,$2,$3, now() - $4::interval, 0)`, f.tenant, f.site, sess, observed.String()); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
}

// A 60-second time allowance on a VALIDITY_WINDOW plan must end access long before the window (a day away).
func TestIntegration_PlanControls_TimeAllowanceEndsAValidityWindowEntitlement(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &window, time.Now().Add(-15*time.Minute))
	revisePlan(t, p, f, ent, map[string]any{"time_quota_seconds": int64(60)})
	seedObserved(t, p, f, sess, 10*time.Minute)

	due, err := New(p).WithAggregateOnlineTime(3600).EnforceExpiries(ctx, f.tenant, f.site)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var got *Expiry
	for i := range due {
		if due[i].EntitlementID == ent {
			got = &due[i]
		}
	}
	if got == nil {
		t.Fatal("the allowance ran out and the entitlement was not ended: the guest keeps browsing on a spent allowance")
	}
	if got.Reason != "TIME" {
		t.Fatalf("reason %q, want the contract's TIME", got.Reason)
	}
	// Only 60 of the ten observed minutes were allowed: it ended about nine minutes ago, not now.
	if age := time.Since(got.At); age < 8*time.Minute || age > 10*time.Minute {
		t.Fatalf("the ending is dated %s ago; it should be dated when the allowance ran out (~9 min)", age)
	}
	status, reason, consumed, mode := "", "", int64(0), ""
	if err := p.QueryRow(ctx, `SELECT status, COALESCE(terminal_reason,''), consumed_online_seconds, time_accounting_mode
		  FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&status, &reason, &consumed, &mode); err != nil {
		t.Fatal(err)
	}
	if status != "TERMINATED" || reason != "TIME" || consumed != 60 || mode != "VALIDITY_WINDOW" {
		t.Fatalf("durable state %s/%s consumed=%d mode=%s; want TERMINATED/TIME, 60 s, still VALIDITY_WINDOW",
			status, reason, consumed, mode)
	}
	cause := scalar[string](t, p, `SELECT cause_detail FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id=$1`, ent)
	if cause != "ONLINE_TIME_ALLOWANCE_EXHAUSTED" {
		t.Fatalf("evidence cause %q; an allowance exhaustion must be told apart from an elapsed window", cause)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.sessions WHERE id=$1 AND state IN ('active','PENDING_ENFORCEMENT')`, sess); n != 0 {
		t.Fatal("the session survived the allowance running out")
	}
}

func TestIntegration_PlanControls_TimeAllowanceIsSpentNotEnded(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &window, time.Now().Add(-15*time.Minute))
	revisePlan(t, p, f, ent, map[string]any{"time_quota_seconds": int64(7200)})
	seedObserved(t, p, f, sess, 10*time.Minute)

	if _, err := New(p).WithAggregateOnlineTime(3600).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if s := status(t, p, ent); s != "ACTIVE" {
		t.Fatalf("entitlement %s with most of its allowance left", s)
	}
	consumed := scalar[int64](t, p, `SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent)
	if consumed < 590 || consumed > 610 {
		t.Fatalf("consumed %d s; ten observed minutes should have been charged (~600)", consumed)
	}
}

func TestIntegration_PlanControls_NoAllowanceMeansNoAccrual(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &window, time.Now().Add(-15*time.Minute))
	if _, err := New(p).WithAggregateOnlineTime(3600).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.session_online_watermarks WHERE session_id=$1`, sess); n != 0 {
		t.Fatal("a plan with no time allowance started being accounted in online time")
	}
	if s := status(t, p, ent); s != "ACTIVE" {
		t.Fatalf("entitlement %s", s)
	}
}

func TestIntegration_PlanControls_TimeAllowanceRunsAsSvcAcctd(t *testing.T) {
	owner := pool(t)
	defer owner.Close()
	ctx := context.Background()
	f := seed(t, owner, 1000, 1000, 0)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, owner, f, &window, time.Now().Add(-40*time.Minute))
	revisePlan(t, owner, f, ent, map[string]any{"time_quota_seconds": int64(120)})
	seedObserved(t, owner, f, sess, 30*time.Minute)
	if _, err := New(rolePool(t, "svc_acctd")).WithAggregateOnlineTime(86400).EnforceExpiries(ctx, f.tenant, f.site); err != nil {
		t.Fatalf("the sweep could not run as svc_acctd: %v", err)
	}
	if s := status(t, owner, ent); s != "TERMINATED" {
		t.Fatalf("entitlement %s after svc_acctd's sweep", s)
	}
}

func sessionRow(t *testing.T, p *pgxpool.Pool, sess string) (state, reason string, ended *time.Time) {
	t.Helper()
	if err := p.QueryRow(context.Background(), `SELECT state, COALESCE(end_reason,''), ended FROM iam_v2.sessions WHERE id=$1`,
		sess).Scan(&state, &reason, &ended); err != nil {
		t.Fatal(err)
	}
	return
}

// A session ends N seconds after it started, at that instant, and nothing else moves: the device can sign back in.
func TestIntegration_PlanControls_MaxContinuousSessionEndsOnlyTheSession(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	started := time.Now().Add(-15 * time.Minute).Truncate(time.Microsecond)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &window, started)
	revisePlan(t, p, f, ent, map[string]any{"max_continuous_session_seconds": 600})

	ended, err := New(p).EnforceSessionLimits(ctx, f.tenant, f.site, time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ended) != 1 || ended[0].SessionID != sess || ended[0].Reason != EndReasonMaxSessionDuration {
		t.Fatalf("ended = %+v, want this session ended for MAX_SESSION_DURATION", ended)
	}
	state, reason, at := sessionRow(t, p, sess)
	if state != "ended" || reason != EndReasonMaxSessionDuration || at == nil ||
		at.Sub(started.Add(10*time.Minute)).Abs() > time.Millisecond {
		t.Fatalf("session %s/%s ended %v; want ended at started+600s (%s)", state, reason, at, started.Add(10*time.Minute))
	}
	if s := status(t, p, ent); s != "ACTIVE" {
		t.Fatalf("the ENTITLEMENT is %s; a session limit must not end it", s)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		 WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NULL`, ent, f.device); n != 1 {
		t.Fatal("the device lost its place on the entitlement; it could not sign back in")
	}
	// Signing back in works: admission is idempotent for a device that holds a place.
	if _, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, f.device); err != nil {
		t.Fatalf("the device could not be admitted again: %v", err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.sessions(tenant_id,site_id,entitlement_id,device_id,state,started,ip,mac)
		VALUES ($1,$2,$3,$4,'active',now(),'10.20.30.40'::inet,'02:00:00:00:30:01'::macaddr)`,
		f.tenant, f.site, ent, f.device); err != nil {
		t.Fatalf("a new session could not be opened after the limit: %v", err)
	}
	again, err := New(p).EnforceSessionLimits(ctx, f.tenant, f.site, time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second pass ended %+v", again)
	}
}

// Idle is measured from the last metered traffic, and only when the accounting pass is healthy.
func TestIntegration_PlanControls_IdleTimeoutEndsAtLastTrafficPlusTimeout(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	started := time.Now().Add(-20 * time.Minute)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &window, started)
	revisePlan(t, p, f, ent, map[string]any{"idle_timeout_seconds": 300})
	m := onBridge(t, p, sess, "br-g-idle")
	submit(t, p, f, m, 1, 0, 0, started.Add(time.Minute))
	lastTraffic := time.Now().Add(-10 * time.Minute).Truncate(time.Microsecond)
	submit(t, p, f, m, 1, 5_000, 50_000, lastTraffic)
	// A reading that has not moved is a replay, not traffic.
	submit(t, p, f, m, 1, 5_000, 50_000, time.Now().Add(-time.Minute))

	// A tick whose accounting pass did not complete judges nobody idle.
	if got, err := New(p).EnforceSessionLimits(ctx, f.tenant, f.site, time.Now(), false); err != nil || len(got) != 0 {
		t.Fatalf("an unhealthy accounting pass still judged idleness: %+v %v", got, err)
	}
	ended, err := New(p).EnforceSessionLimits(ctx, f.tenant, f.site, time.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ended) != 1 || ended[0].Reason != EndReasonIdleTimeout {
		t.Fatalf("ended = %+v, want one IDLE_TIMEOUT", ended)
	}
	if want := lastTraffic.Add(5 * time.Minute); ended[0].At.Sub(want).Abs() > time.Millisecond {
		t.Fatalf("ended at %s, want last traffic + 300 s = %s", ended[0].At, want)
	}
	if s := status(t, p, ent); s != "ACTIVE" {
		t.Fatalf("idle ended the ENTITLEMENT (%s)", s)
	}
}

func TestIntegration_PlanControls_IdleNeedsEvidence(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	started := time.Now().Add(-20 * time.Minute)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &window, started)
	revisePlan(t, p, f, ent, map[string]any{"idle_timeout_seconds": 300})

	// Never observed: no checkpoint, no evidence, not judged.
	if got, err := New(p).EnforceSessionLimits(ctx, f.tenant, f.site, time.Now(), true); err != nil || len(got) != 0 {
		t.Fatalf("an unobserved session was judged idle: %+v %v", got, err)
	}
	m := onBridge(t, p, sess, "br-g-busy")
	submit(t, p, f, m, 1, 0, 0, started.Add(time.Minute))
	submit(t, p, f, m, 1, 1_000, 9_000, time.Now().Add(-30*time.Second))
	if got, err := New(p).EnforceSessionLimits(ctx, f.tenant, f.site, time.Now(), true); err != nil || len(got) != 0 {
		t.Fatalf("a session with traffic 30 s ago was judged idle: %+v %v", got, err)
	}
	if st, _, _ := sessionRow(t, p, sess); st != "active" {
		t.Fatalf("session %s", st)
	}
}

func TestIntegration_PlanControls_SessionLimitsRunAsSvcAcctd(t *testing.T) {
	owner := pool(t)
	defer owner.Close()
	ctx := context.Background()
	f := seed(t, owner, 1000, 1000, 0)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, owner, f, &window, time.Now().Add(-2*time.Hour))
	revisePlan(t, owner, f, ent, map[string]any{"max_continuous_session_seconds": 3600})
	ended, err := New(rolePool(t, "svc_acctd")).EnforceSessionLimits(ctx, f.tenant, f.site, time.Now(), true)
	if err != nil {
		t.Fatalf("svc_acctd could not enforce the session limits: %v", err)
	}
	if len(ended) != 1 || ended[0].SessionID != sess {
		t.Fatalf("ended = %+v", ended)
	}
}

func newDevice(t *testing.T, p *pgxpool.Pool, f fixture, mac string) string {
	t.Helper()
	return scalar[string](t, p, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),($3::text)::macaddr) RETURNING id::text`, f.tenant, f.site, mac)
}

// On a one-device DISCONNECT_OLDEST plan a second device is admitted: the first's session ends, its place is released and the move recorded.
func TestIntegration_PlanControls_DisconnectOldestAdmitsTheNewDevice(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	window := time.Now().Add(24 * time.Hour)
	ent, oldSess := grant(t, p, f, &window, time.Now().Add(-time.Hour))
	revisePlan(t, p, f, ent, map[string]any{"max_concurrent_devices": 1, "device_limit_policy": "DISCONNECT_OLDEST"})
	newDev := newDevice(t, p, f, "02:00:00:00:51:01")

	if _, err := rolePool(t, "svc_scd").Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, newDev); err != nil {
		t.Fatalf("DISCONNECT_OLDEST refused the new device: %v", err)
	}
	state, reason, _ := sessionRow(t, p, oldSess)
	if state != "ended" || reason != "DEVICE_LIMIT_OLDEST_DISCONNECTED" {
		t.Fatalf("the oldest device's session is %s/%s", state, reason)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		 WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NULL`, ent, f.device); n != 0 {
		t.Fatal("the oldest device kept its place")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		 WHERE entitlement_id=$1 AND device_id=$2 AND deauthorized_at IS NULL`, ent, newDev); n != 1 {
		t.Fatal("the new device was not admitted")
	}
	why := scalar[string](t, p, `SELECT COALESCE(disconnected_reason,'') FROM iam_v2.entitlement_devices
		 WHERE entitlement_id=$1 AND device_id=$2`, ent, f.device)
	if why != "DISCONNECTED_FOR_NEWER_DEVICE" {
		t.Fatalf("disconnected_reason %q", why)
	}
	var gone, came string
	var limit, sessions int
	if err := p.QueryRow(ctx, `SELECT disconnected_device_id::text, admitted_device_id::text, device_limit, sessions_ended
		  FROM iam_v2.device_limit_disconnections WHERE entitlement_id=$1`, ent).Scan(&gone, &came, &limit, &sessions); err != nil {
		t.Fatalf("the disconnection was not recorded: %v", err)
	}
	if gone != f.device || came != newDev || limit != 1 || sessions != 1 {
		t.Fatalf("record = %s -> %s limit %d sessions %d", gone, came, limit, sessions)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.device_limit_disconnections SET sessions_ended=0 WHERE entitlement_id=$1`, ent); err == nil {
		t.Fatal("device_limit_disconnections accepted an UPDATE")
	}
	if s := status(t, p, ent); s != "ACTIVE" {
		t.Fatalf("entitlement %s", s)
	}
}

// REJECT refuses, and ADMIN_APPROVAL fails closed with a reason saying it is not available.
func TestIntegration_PlanControls_RejectAndAdminApprovalStillRefuse(t *testing.T) {
	for _, policy := range []string{"REJECT_NEW_DEVICE", "ADMIN_APPROVAL"} {
		t.Run(policy, func(t *testing.T) {
			p := pool(t)
			defer p.Close()
			ctx := context.Background()
			f := seed(t, p, 1000, 1000, 0)
			window := time.Now().Add(24 * time.Hour)
			ent, sess := grant(t, p, f, &window, time.Now().Add(-time.Hour))
			revisePlan(t, p, f, ent, map[string]any{"max_concurrent_devices": 1, "device_limit_policy": policy})
			dev := newDevice(t, p, f, "02:00:00:00:52:01")
			_, err := p.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, ent, dev)
			if err == nil || !strings.Contains(err.Error(), "MAX_DEVICES_REACHED") {
				t.Fatalf("err = %v, want MAX_DEVICES_REACHED", err)
			}
			if policy == "ADMIN_APPROVAL" && !strings.Contains(err.Error(), "not available") {
				t.Fatalf("ADMIN_APPROVAL refusal does not say it is unavailable: %v", err)
			}
			if st, _, _ := sessionRow(t, p, sess); st != "active" {
				t.Fatal("a refused admission disconnected the existing device")
			}
		})
	}
}

func crossing(t *testing.T, p *pgxpool.Pool, fn, ent string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := p.QueryRow(context.Background(), `SELECT iam_v2.`+fn+`($1)`, ent).Scan(&at); err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return at
}

func advance(t *testing.T, p *pgxpool.Pool, f fixture) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `SELECT iam_v2.p6_advance_data_crossing($1,$2)`, f.tenant, f.site); err != nil {
		t.Fatalf("advance: %v", err)
	}
}

// Through two devices, interleaved readings, a counter reset and a restart, the incremental answer equals the
// full derivation at every step.
func TestIntegration_DataCrossing_IncrementalEqualsTheFullDerivation(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := quotaSeed(t, p, int64(4_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	a := onBridge(t, p, sess, "br-g-eq")
	b := meteredSession(t, p, f, ent, "10.20.30.41", "02:00:00:00:53:02", "br-g-eq", at)
	submit(t, p, f, a, 1, 0, 0, at)
	submit(t, p, f, b, 1, 0, 0, at)

	ua, ub, epochB := int64(0), int64(0), int64(1)
	var wantCross *time.Time
	for i := 1; i <= 40; i++ {
		ts := at.Add(time.Duration(i) * 30 * time.Second)
		ua += 100_000
		submit(t, p, f, a, 1, ua/4, ua-ua/4, ts)
		if i%3 == 0 {
			ub += 150_000
			submit(t, p, f, b, epochB, ub/2, ub-ub/2, ts)
		}
		if i == 20 {
			// A trusted reset of device b's class: counters restart from a new baseline, nothing lost.
			ub, epochB = 0, 2
			submit(t, p, f, b, epochB, 0, 0, ts)
		}
		if i%5 == 0 {
			// A restart: a new Enforcer over the same durable checkpoint.
			advance(t, p, f)
		}
		got := crossing(t, p, "p6_data_crossing", ent)
		full := crossing(t, p, "p6_data_crossing_full", ent)
		if (got == nil) != (full == nil) || (got != nil && !got.Equal(*full)) {
			t.Fatalf("step %d: incremental %v, full derivation %v", i, got, full)
		}
		if full != nil && wantCross == nil {
			wantCross = full
		}
	}
	if wantCross == nil {
		t.Fatal("the fixture never crossed its quota; the case proves nothing")
	}
	due, err := New(p).EnforceExpiries(context.Background(), f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Reason != "DATA" || !due[0].At.Equal(*wantCross) {
		t.Fatalf("expiry %+v, want one DATA termination at %s", due, wantCross)
	}
}

// Once the checkpoint has passed a record it is not read again, proven by changing a counted record behind the
// checkpoint's back: a rescan would see the change.
func TestIntegration_DataCrossing_CountedRecordsAreNotReadAgain(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(100_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-nr")
	submit(t, p, f, m, 1, 0, 0, at)
	for i := 1; i <= 50; i++ {
		submit(t, p, f, m, 1, int64(i)*10_000, int64(i)*90_000, at.Add(time.Duration(i)*time.Second))
	}
	advance(t, p, f)
	examined := scalar[int64](t, p, `SELECT examined_seq FROM iam_v2.data_crossing_progress WHERE session_id=$1`, sess)
	if examined != 50 {
		t.Fatalf("checkpoint examined %d samples, want 50", examined)
	}

	// Rewrite the FIRST counted record to an enormous value. Disposable test database; superuser only.
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.accounting_records SET bytes_down = 999000000000
		 WHERE session_id=$1 AND sample_seq=1`, sess); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if full := crossing(t, p, "p6_data_crossing_full", ent); full == nil {
		t.Fatal("the full derivation does not see the rewritten record; the case proves nothing")
	}
	submit(t, p, f, m, 1, 510_000, 4_590_000, at.Add(51*time.Second))
	advance(t, p, f)
	if got := crossing(t, p, "p6_data_crossing", ent); got != nil {
		t.Fatalf("p6_data_crossing re-read history behind its checkpoint (it saw the rewritten record): %v", got)
	}
	if got := scalar[int64](t, p, `SELECT counted_bytes FROM iam_v2.data_crossing_progress WHERE session_id=$1`, sess); got != 5_100_000 {
		t.Fatalf("checkpoint counted %d bytes; want exactly the 51 genuine readings (5,100,000)", got)
	}
}

// An overstated checkpoint (an attribution interval shrank after it counted) is corrected, not obeyed: the exact
// derivation decides and the checkpoint is recounted so it does not trigger a full scan every tick.
func TestIntegration_DataCrossing_OverstatementIsRecounted(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := quotaSeed(t, p, int64(1_000_000), "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-time.Hour)
	ent, sess := grant(t, p, f, nil, at)
	m := onBridge(t, p, sess, "br-g-ov")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 100_000, 400_000, at.Add(time.Minute))     // 0.5 MB
	submit(t, p, f, m, 1, 200_000, 1_000_000, at.Add(2*time.Minute)) // +0.7 MB -> 1.2 MB, over
	// The interval shrinks so the second reading is no longer this entitlement's.
	overstate := func() {
		tx, err := p.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		// pretend the checkpoint had counted both readings under the old, wider interval
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.data_crossing_progress
			 (binding_id, tenant_id, site_id, entitlement_id, session_id, examined_seq, counted_bytes)
			 SELECT b.id, b.tenant_id, b.site_id, b.entitlement_id, b.session_id, 2, 1200000
			   FROM iam_v2.session_entitlement_bindings b WHERE b.session_id=$1`, sess); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.session_entitlement_bindings SET bound_until=$2 WHERE session_id=$1`,
			sess, at.Add(90*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	overstate()
	if full := crossing(t, p, "p6_data_crossing_full", ent); full != nil {
		t.Fatalf("fixture: the full derivation says crossed at %v", full)
	}
	advance(t, p, f)
	if got := crossing(t, p, "p6_data_crossing", ent); got != nil {
		t.Fatalf("an overstated checkpoint ended access: %v", got)
	}
	if got := scalar[int64](t, p, `SELECT counted_bytes FROM iam_v2.data_crossing_progress WHERE session_id=$1`, sess); got != 500_000 {
		t.Fatalf("checkpoint not recounted: %d, want 500000", got)
	}
}

// oldTerminatedUsage builds an entitlement metered 40 days ago that ended 39 days ago, plus a live one with a
// 40-day-old reading.
func oldTerminatedUsage(t *testing.T, p *pgxpool.Pool) (fixture, string, string) {
	t.Helper()
	f := quotaSeed(t, p, nil, "PER_DEVICE", "VALIDITY_WINDOW")
	at := time.Now().Add(-41 * 24 * time.Hour)
	window := time.Now().Add(-39 * 24 * time.Hour)
	ent, sess := grant(t, p, f, &window, at)
	m := onBridge(t, p, sess, "br-g-ret")
	submit(t, p, f, m, 1, 0, 0, at)
	submit(t, p, f, m, 1, 1_000, 9_000, at.Add(24*time.Hour))
	if _, err := New(p).EnforceExpiries(context.Background(), f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if s := status(t, p, ent); s != "TERMINATED" {
		t.Fatalf("fixture: %s", s)
	}
	live := secondEntitlement(t, p, f, at)
	ls := meteredSession(t, p, f, live, "10.20.30.60", "02:00:00:00:54:01", "br-g-ret", at)
	submit(t, p, f, ls, 1, 0, 0, at)
	submit(t, p, f, ls, 1, 2_000, 8_000, at.Add(24*time.Hour))
	return f, sess, ls.id
}

// A year is the default retention, so younger detail stays; an administrator's unlimited means nothing happens.
func TestIntegration_Retention_AYearByDefaultAndUnlimitedPurgesNothing(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f, sess, _ := oldTerminatedUsage(t, p)
	days := scalar[*int](t, p, `SELECT detail_retention_days FROM iam_v2.accounting_retention_get($1,$2)`, f.tenant, f.site)
	if days == nil || *days != 365 {
		t.Fatalf("default retention is %v; it must be 365 days", days)
	}
	acctd := New(rolePool(t, "svc_acctd"))
	purgeNothing := func(what string) {
		t.Helper()
		n, err := acctd.PurgeAccountingDetail(ctx, f.tenant, f.site, 1000)
		if err != nil || n != 0 {
			t.Fatalf("purge %s removed %d (%v)", what, n, err)
		}
		if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); c != 1 {
			t.Fatalf("records %d after a purge %s", c, what)
		}
		if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_purge_runs WHERE site_id=$1`, f.site); c != 0 {
			t.Fatalf("a purge %s recorded a run", what)
		}
	}
	purgeNothing("of 40-day-old detail under the 365-day default")

	if _, err := rolePool(t, "svc_edged").Exec(ctx,
		`SELECT iam_v2.accounting_retention_set($1,$2,NULL,'admin@test','keep everything')`, f.tenant, f.site); err != nil {
		t.Fatalf("the administrator could not choose unlimited: %v", err)
	}
	if days := scalar[*int](t, p, `SELECT detail_retention_days FROM iam_v2.accounting_retention_get($1,$2)`, f.tenant, f.site); days != nil {
		t.Fatalf("an administrator's unlimited reads %d days", *days)
	}
	purgeNothing("under the administrator's unlimited")
}

// The setting is bounded and audited, and nothing else can delete a record.
func TestIntegration_Retention_BoundsAndNoOtherDeletePath(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f, sess, _ := oldTerminatedUsage(t, p)
	edged := rolePool(t, "svc_edged")
	for _, bad := range []string{
		`SELECT iam_v2.accounting_retention_set($1,$2,7,'admin@test','too short')`,
		`SELECT iam_v2.accounting_retention_set($1,$2,90,'admin@test','')`,
		`SELECT iam_v2.accounting_retention_set($1,$2,90,'','no operator')`,
	} {
		if _, err := edged.Exec(ctx, bad, f.tenant, f.site); err == nil {
			t.Fatalf("accepted: %s", bad)
		}
	}
	// Back to unlimited is allowed, and logged.
	if _, err := edged.Exec(ctx, `SELECT iam_v2.accounting_retention_set($1,$2,NULL,'admin@test','keep everything')`,
		f.tenant, f.site); err != nil {
		t.Fatalf("unlimited refused: %v", err)
	}
	// No runtime role can delete detail directly, and the owner cannot either without the purge's declaration.
	for _, role := range []string{"svc_acctd", "svc_edged", "svc_scd"} {
		if _, err := rolePool(t, role).Exec(ctx, `DELETE FROM iam_v2.accounting_records WHERE session_id=$1`, sess); err == nil {
			t.Fatalf("%s deleted accounting detail directly", role)
		}
	}
	if err := ownerExec(t, p, `DELETE FROM iam_v2.accounting_records WHERE session_id=$1`, sess); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("owner DELETE without the purge declaration: %v", err)
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.accounting_records WHERE session_id=$1`, sess); c != 1 {
		t.Fatal("a record went")
	}
}

// Sub-second ticks are charged in full: flooring each tick's charge while moving the watermark discards the
// fraction. A hundred 0.9 s ticks are 90 s of online time and must charge 90 s (89 allows the last remainder).
func TestIntegration_PlanControls_SubSecondTicksChargeEverySecond(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)
	window := time.Now().Add(24 * time.Hour)
	ent, sess := grant(t, p, f, &window, time.Now().Add(-15*time.Minute))
	revisePlan(t, p, f, ent, map[string]any{"time_quota_seconds": int64(7200)})
	base := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Second)
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.session_online_watermarks
		   (tenant_id, site_id, session_id, accounted_through, accounted_seconds) VALUES ($1,$2,$3,$4,0)`,
		f.tenant, f.site, sess, base); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
	for k := 1; k <= 100; k++ {
		now := base.Add(time.Duration(k) * 900 * time.Millisecond)
		if _, err := p.Exec(ctx, `SELECT * FROM iam_v2.p6_tick_online_time($1,$2,$3,10)`, f.tenant, f.site, now); err != nil {
			t.Fatalf("tick %d: %v", k, err)
		}
	}
	consumed := scalar[int64](t, p, `SELECT consumed_online_seconds FROM iam_v2.entitlements WHERE id=$1`, ent)
	if consumed < 89 || consumed > 90 {
		t.Fatalf("a hundred 0.9 s ticks charged %d s; want 90 (the old floor-per-tick charged 0)", consumed)
	}
	watermark := scalar[time.Time](t, p, `SELECT accounted_through FROM iam_v2.session_online_watermarks WHERE session_id=$1`, sess)
	if d := base.Add(90 * time.Second).Sub(watermark.UTC()); d < 0 || d >= time.Second {
		t.Fatalf("watermark %s is not the charged whole seconds after the base (%s off)", watermark, d)
	}
}

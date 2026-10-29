package api

import (
	"context"
	"strings"
	"testing"
)

func explain(t *testing.T, f *fixture, sql string, args ...any) string {
	t.Helper()
	conn, err := f.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// With a few rows the planner prefers a sequential scan; forbidding it shows whether an index can serve.
	if _, err := conn.Exec(f.ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(f.ctx, `RESET enable_seqscan`)
	rows, err := conn.Query(f.ctx, `EXPLAIN `+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		b.WriteString(line + "\n")
	}
	return b.String()
}

// Perf audit D9-M1: the hourly fetch-log prune can use an index on `at` (migration 0058).
func TestFetchLogPruneUsesAnIndex(t *testing.T) {
	f := newFixture(t)
	plan := explain(t, f, `DELETE FROM appliance_assignment_fetch_log WHERE at < now() - make_interval(secs => 2592000)`)
	if !strings.Contains(plan, "appliance_assignment_fetch_log_at") {
		t.Fatalf("the prune must be able to use the at index:\n%s", plan)
	}
}

// Perf audit D9-M2: the fleet list's licence match uses the 0041 partial index, and still finds the licence.
func TestFleetLicenceMatchUsesTheIndex(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Fleet")
	app := decode(t, f.register(newBox("SC-TEST-D9M2", "hwf-d9m2")))["appliance_id"].(string)
	f.activate(app, cust, site, 7)
	rows, err := f.base.queryAppliances(f.ctx, "", app)
	if err != nil || len(rows) != 1 || rows[0].License.ID == nil || rows[0].License.MaxConcurrentOnlineGuests == nil ||
		*rows[0].License.MaxConcurrentOnlineGuests != 7 {
		t.Fatalf("the fleet row must carry its licence: %+v %v", rows, err)
	}
	plan := explain(t, f, `SELECT x.id FROM licenses x WHERE x.appliance_ids[1] = $1::uuid AND cardinality(x.appliance_ids) > 0
        AND x.status IN ('active','suspended','revoked') ORDER BY (x.status IN ('active','suspended')) DESC, x.issued_at DESC LIMIT 1`, app)
	if !strings.Contains(plan, "licenses_appliance_version_unique") && !strings.Contains(plan, "licenses_one_current_per_appliance") {
		t.Fatalf("the licence match must be able to use a 0041 index:\n%s", plan)
	}
}

// Perf audit D9-L1: an appliance's history comes from both conditions, newest first, each row once, and each
// branch can use its index (migration 0058).
func TestApplianceEventsUnion(t *testing.T) {
	f := newFixture(t)
	const app = "11111111-2222-3333-4444-555555555555"
	for _, q := range []string{
		`INSERT INTO audit_log (ts, actor_type, action, target_type, target_id, payload) VALUES (now() - interval '3 minutes', 'system', 'a.target', 'appliance', '` + app + `', '{}')`,
		`INSERT INTO audit_log (ts, actor_type, action, target_type, target_id, payload) VALUES (now() - interval '2 minutes', 'system', 'b.payload', 'licence', 'x', '{"appliance_id":"` + app + `"}')`,
		`INSERT INTO audit_log (ts, actor_type, action, target_type, target_id, payload) VALUES (now() - interval '1 minute', 'system', 'c.both', 'appliance', '` + app + `', '{"appliance_id":"` + app + `"}')`,
		`INSERT INTO audit_log (ts, actor_type, action, target_type, target_id, payload) VALUES (now(), 'system', 'd.other', 'appliance', 'someone-else', '{}')`,
	} {
		if _, err := f.pool.Exec(f.ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	ev, err := f.base.applianceEvents(f.ctx, app, Scope{All: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ev {
		got = append(got, e["action"].(string))
	}
	if strings.Join(got, ",") != "c.both,b.payload,a.target" {
		t.Fatalf("events = %v, want c.both,b.payload,a.target", got)
	}
	plan := explain(t, f, `SELECT ts FROM audit_log WHERE payload->>'appliance_id' = $1::text ORDER BY ts DESC LIMIT 50`, app)
	if !strings.Contains(plan, "audit_log_payload_appliance_ts") {
		t.Fatalf("the payload branch must be able to use its index:\n%s", plan)
	}
	plan = explain(t, f, `SELECT ts FROM audit_log WHERE target_type = 'appliance' AND target_id = $1::text ORDER BY ts DESC LIMIT 50`, app)
	if !strings.Contains(plan, "audit_log_target_ts") {
		t.Fatalf("the target branch must be able to use its index:\n%s", plan)
	}
}

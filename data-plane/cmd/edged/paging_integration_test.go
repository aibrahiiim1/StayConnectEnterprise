//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// THE OPERATOR LISTS PAGE OVER EVERY ROW, NOT OVER WHAT THE BROWSER LOADED.
//
// The activity trail stopped at 500 entries, the sessions list and PMS activity at 200, and each searched and
// counted only what it had loaded. These tests hold the server to the shared contract: ?page / ?page_size,
// meta.has_more, totals over every match, search in a header, and pages that neither repeat nor skip a row.

// getWithHeader sends one GET with an optional request header (the lists' search travels in a header).
func (f *apiFixture) getWithHeader(t *testing.T, path string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("GET", f.srv.URL+"/edge/v1"+path, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.sessTok})
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// pageThrough walks every page of path (which must already carry its query) and returns each row's key in
// order, failing on a repeated key or a page that disagrees with has_more.
func (f *apiFixture) pageThrough(t *testing.T, path string, size int, headers map[string]string, key func(map[string]any) string) []string {
	t.Helper()
	seen := map[string]bool{}
	keys := []string{}
	for page := 1; ; page++ {
		status, body := f.getWithHeader(t, fmt.Sprintf("%s&page=%d&page_size=%d", path, page, size), headers)
		if status != 200 {
			t.Fatalf("%s page %d = %d %v", path, page, status, body)
		}
		if body["page"] != float64(page) || body["page_size"] != float64(size) {
			t.Fatalf("%s page %d echoed page=%v page_size=%v", path, page, body["page"], body["page_size"])
		}
		data := body["data"].([]any)
		more := body["meta"].(map[string]any)["has_more"].(bool)
		if more && len(data) != size {
			t.Fatalf("%s page %d has_more with %d rows, want a full page of %d", path, page, len(data), size)
		}
		for _, r := range data {
			k := key(r.(map[string]any))
			if seen[k] {
				t.Fatalf("%s: row %s listed on two pages", path, k)
			}
			seen[k] = true
			keys = append(keys, k)
		}
		if !more {
			return keys
		}
		if page > 100 {
			t.Fatal("paging never ended")
		}
	}
}

// ---------------------------------------------------------------------------------------------- audit ---

// seedAudit writes n entries for the fixture's tenant. Entries come in pairs that share one timestamp, which
// is exactly the case an offset over "ORDER BY ts" alone gets wrong. Every fifth is a network change made by
// "op-net"; the rest are sign-ins.
func (f *apiFixture) seedAudit(t *testing.T, n int) time.Time {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO audit_log(ts, tenant_id, actor_type, actor_id, action, target_type, target_id, ip, payload)
		SELECT $2::timestamptz + make_interval(secs => (i / 2)), $1::uuid, 'operator',
		       CASE WHEN i % 5 = 0 THEN 'op-net' ELSE 'op-desk' END,
		       CASE WHEN i % 5 = 0 THEN 'network.apply' ELSE 'operator.login' END,
		       'thing', 'target-'||i, ('10.9.'||(i / 250)||'.'||(i % 250))::inet, '{}'::jsonb
		  FROM generate_series(1, $3::int) i`, f.tenant, base, n); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	return base
}

func auditKey(r map[string]any) string { return r["target_id"].(string) }

func TestIntegration_Audit_PagesEveryEntryWithoutRepeatsOrGaps(t *testing.T) {
	f := newAPI(t)
	f.seedAudit(t, 130)

	keys := f.pageThrough(t, "/audit?x=1", 25, nil, auditKey)
	if len(keys) != 130 {
		t.Fatalf("paged %d entries, want all 130", len(keys))
	}

	status, body := f.do(t, "GET", "/audit?page_size=25", nil)
	if status != 200 || body["total"] != float64(130) {
		t.Fatalf("total = %v (status %d), want 130", body["total"], status)
	}
	// The per-action counts cover the period, whichever filter is applied.
	counts := map[string]float64{}
	for _, a := range body["actions"].([]any) {
		m := a.(map[string]any)
		counts[m["action"].(string)] = m["count"].(float64)
	}
	if counts["network.apply"] != 26 || counts["operator.login"] != 104 {
		t.Fatalf("action counts = %v", counts)
	}

	// An older client's ?limit= still sizes the page, and has_more tells it there is more.
	status, body = f.do(t, "GET", "/audit?limit=100", nil)
	if status != 200 || len(body["data"].([]any)) != 100 || body["meta"].(map[string]any)["has_more"] != true {
		t.Fatalf("legacy limit: %d rows, has_more %v", len(body["data"].([]any)), body["meta"])
	}

	for _, bad := range []string{"/audit?page=0", "/audit?page_size=201", "/audit?page_size=0"} {
		if status, _ := f.do(t, "GET", bad, nil); status != 400 {
			t.Errorf("%s = %d, want 400", bad, status)
		}
	}
}

func TestIntegration_Audit_FiltersAndSearchNarrowTheWholeTrail(t *testing.T) {
	f := newAPI(t)
	f.seedAudit(t, 130)

	// An exact action list -- what the screen sends for a category -- and its total.
	status, body := f.do(t, "GET", "/audit?action=network.apply&page_size=10", nil)
	if status != 200 || body["total"] != float64(26) {
		t.Fatalf("action filter total = %v", body["total"])
	}
	// The per-action counts are NOT narrowed by the action filter.
	if len(body["actions"].([]any)) != 2 {
		t.Fatalf("actions under a filter = %v", body["actions"])
	}
	// Present but empty: a category with nothing in the period shows nothing, not everything.
	if _, body = f.do(t, "GET", "/audit?action=", nil); body["total"] != float64(0) {
		t.Fatalf("empty action list matched %v", body["total"])
	}
	if _, body = f.do(t, "GET", "/audit?action_prefix=network", nil); body["total"] != float64(26) {
		t.Fatalf("prefix total = %v", body["total"])
	}

	// The search travels in a header and reaches entries far past the first page.
	keys := f.pageThrough(t, "/audit?x=1", 5, map[string]string{auditSearchHeader: "target-12"}, auditKey)
	// target-12 and target-120..129.
	if len(keys) != 11 {
		t.Fatalf("search target-12 matched %v", keys)
	}
	// The address is searchable.
	if _, body = f.getWithHeader(t, "/audit?page_size=5", map[string]string{auditSearchHeader: "10.9.0.7"}); body["total"] == float64(0) {
		t.Fatal("an address search found nothing")
	}
	// Codes whose readable title matched (resolved by the screen) widen the search.
	_, body = f.getWithHeader(t, "/audit?page_size=5", map[string]string{
		auditSearchHeader: "applied network changes", auditSearchActionsHeader: "network.apply"})
	if body["total"] != float64(26) {
		t.Fatalf("title search total = %v, want 26", body["total"])
	}
	// The actor filter matches the actor.
	if _, body = f.do(t, "GET", "/audit?actor=op-net", nil); body["total"] != float64(26) {
		t.Fatalf("actor filter total = %v", body["total"])
	}
}

// ------------------------------------------------------------------------------------------- sessions ---

// seedSessions creates one account entitlement with one active session, then n ended sessions on the same
// device. Ended sessions need no live authorization binding, so the fixture can make as many as a test wants.
func (f *apiFixture) seedSessions(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	active := seedAllowanceSession(t, f, nil, nil, 0, "02:00:00:00:00:61")
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO iam_v2.sessions(tenant_id, site_id, entitlement_id, device_id, ip, mac, state, started, ended,
		                            end_reason, bytes_up, bytes_down)
		SELECT s.tenant_id, s.site_id, s.entitlement_id, s.device_id,
		       ('10.77.'||(i / 250)||'.'||(i % 250))::inet, s.mac, 'ended',
		       now() - make_interval(mins => i), now() - make_interval(mins => i) + interval '30 seconds',
		       'admin', 10, 20
		  FROM iam_v2.sessions s, generate_series(1, $2::int) i
		 WHERE s.id = $1`, active, n); err != nil {
		t.Fatalf("seed ended sessions: %v", err)
	}
}

func sessionKey(r map[string]any) string { return r["id"].(string) }

func TestIntegration_Sessions_PagedWithTotalsOverEverySession(t *testing.T) {
	f := newAPI(t)
	f.seedSessions(t, 120)

	keys := f.pageThrough(t, "/sessions?x=1", 50, nil, sessionKey)
	if len(keys) != 121 {
		t.Fatalf("paged %d sessions, want 121", len(keys))
	}

	status, body := f.do(t, "GET", "/sessions?page_size=10", nil)
	if status != 200 || body["total"] != float64(121) {
		t.Fatalf("total = %v", body["total"])
	}
	sum := body["summary"].(map[string]any)
	if sum["devices_online"] != float64(1) || sum["clients_online"] != float64(1) {
		t.Fatalf("summary = %v", sum)
	}
	if sum["kinds"].(map[string]any)["account"] != float64(121) {
		t.Fatalf("kind counts = %v", sum["kinds"])
	}
	if sum["bytes_total"] != float64(120*30) {
		t.Fatalf("bytes_total = %v", sum["bytes_total"])
	}

	// The state filter still narrows the list, and the legacy spelling still works.
	if _, body = f.do(t, "GET", "/sessions?state=active", nil); body["total"] != float64(1) {
		t.Fatalf("active total = %v", body["total"])
	}
	if _, body = f.do(t, "GET", "/sessions?state=closed", nil); body["total"] != float64(120) {
		t.Fatalf("closed total = %v", body["total"])
	}
	// The kind filter runs on the server.
	if _, body = f.do(t, "GET", "/sessions?kind=room", nil); body["total"] != float64(0) {
		t.Fatalf("room kind total = %v", body["total"])
	}
	if status, _ := f.do(t, "GET", "/sessions?kind=nobody", nil); status != 400 {
		t.Fatalf("an unknown kind = %d, want 400", status)
	}
	if status, _ := f.do(t, "GET", "/sessions?page_size=500", nil); status != 400 {
		t.Fatalf("an oversized page = %d, want 400", status)
	}
}

func TestIntegration_Sessions_SearchInAHeaderCoversEverySession(t *testing.T) {
	f := newAPI(t)
	f.seedSessions(t, 120)

	// 10.77.0.119 is the second-oldest session: far past the old 200-row window's equivalent first page.
	_, body := f.getWithHeader(t, "/sessions?page_size=5", map[string]string{sessionSearchHeader: "10.77.0.119"})
	if body["total"] != float64(1) {
		t.Fatalf("address search total = %v, want 1", body["total"])
	}
	// The account's username matches every session on it.
	_, body = f.getWithHeader(t, "/sessions?page_size=5", map[string]string{sessionSearchHeader: "guest-alw-61"})
	if body["total"] != float64(121) {
		t.Fatalf("username search total = %v, want 121", body["total"])
	}
	// The search narrows the total but not the tiles.
	if body["summary"].(map[string]any)["kinds"].(map[string]any)["account"] != float64(121) {
		t.Fatalf("summary narrowed by search: %v", body["summary"])
	}
	_, body = f.getWithHeader(t, "/sessions", map[string]string{sessionSearchHeader: "%"})
	if body["total"] != float64(0) {
		t.Fatalf("a literal %% matched %v", body["total"])
	}
}

// ----------------------------------------------------------------------------------------- PMS events ---

// seedStayEvents inserts n PENDING, unmatched messages (the only shape the append-only trigger accepts at
// insert) one second apart. Every tenth is a check-out.
func (f *apiFixture) seedStayEvents(t *testing.T, n int) string {
	t.Helper()
	ctx := context.Background()
	tag := fmt.Sprintf("EVP%d", time.Now().UnixNano())
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH
		  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
		         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id)
		INSERT INTO iam_v2.stay_events(id,tenant_id,site_id,pms_interface_id,external_event_identity,
		  event_type,processing_status,received_at)
		SELECT gen_random_uuid(),$1,$2,pi.id,$3||'-'||i,
		       CASE WHEN i % 10 = 0 THEN 'GUEST_OUT' ELSE 'GUEST_IN' END, 'PENDING',
		       now() - make_interval(secs => i)
		  FROM pi, generate_series(1,$4::int) i`, f.tenant, f.site, tag, n)
		return err
	}); err != nil {
		t.Fatalf("seed stay events: %v", err)
	}
	return tag
}

func TestIntegration_PMSEvents_PagedSearchedAndCounted(t *testing.T) {
	f := newAPI(t)
	tag := f.seedStayEvents(t, 230)

	keys := f.pageThrough(t, "/pms-events?x=1", 50, nil, func(r map[string]any) string { return r["id"].(string) })
	if len(keys) != 230 {
		t.Fatalf("paged %d messages, want all 230 -- the old list stopped at 200", len(keys))
	}

	status, body := f.do(t, "GET", "/pms-events?page_size=10", nil)
	if status != 200 || body["total"] != float64(230) {
		t.Fatalf("total = %v", body["total"])
	}
	sum := body["summary"].(map[string]any)
	if sum["pending"] != float64(230) || sum["unmatched"] != float64(230) || sum["applied"] != float64(0) {
		t.Fatalf("summary = %v", sum)
	}
	if sum["newest_received_at"] == nil {
		t.Fatal("no newest_received_at")
	}

	// The status filter narrows the list; the tiles still describe every message.
	if _, body = f.do(t, "GET", "/pms-events?processing_status=APPLIED", nil); body["total"] != float64(0) ||
		body["summary"].(map[string]any)["pending"] != float64(230) {
		t.Fatalf("status filter: total %v summary %v", body["total"], body["summary"])
	}

	// Search, in a header: the oldest message, past the first 200, by its PMS identifier...
	_, body = f.getWithHeader(t, "/pms-events?page_size=5", map[string]string{pmsEventSearchHeader: tag + "-230"})
	if body["total"] != float64(1) {
		t.Fatalf("identity search total = %v, want 1", body["total"])
	}
	// ...and by message type, across every page.
	keys = f.pageThrough(t, "/pms-events?x=1", 7, map[string]string{pmsEventSearchHeader: "guest_out"},
		func(r map[string]any) string { return r["id"].(string) })
	if len(keys) != 23 {
		t.Fatalf("type search paged %d, want 23", len(keys))
	}

	if status, _ := f.do(t, "GET", "/pms-events?page=abc", nil); status != 400 {
		t.Fatalf("a bad page = %d, want 400", status)
	}
}

// --------------------------------------------------------------------------------- PMS resolutions ---

func TestIntegration_PMSResolutions_PagedWithASummaryOfTheRecentWindow(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	gnA, _ := seedGuestNetwork(t, f.pool, f.tenant, f.site, "Annexe", "10.81.0.0/24")
	gnB, _ := seedGuestNetwork(t, f.pool, f.tenant, f.site, "Main", "10.82.0.0/24")
	if err := controlled(ctx, f.pool, []string{"auth_resolution"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.auth_resolutions(id,tenant_id,site_id,guest_network_id,
		    outcome_code,resolved_at)
		  SELECT gen_random_uuid(),$1,$2, CASE WHEN i % 2 = 0 THEN $3::uuid ELSE $4::uuid END,
		         'NO_MATCH', now() - make_interval(secs => i)
		    FROM generate_series(1,250) i`, f.tenant, f.site, gnA, gnB)
		return err
	}); err != nil {
		t.Fatalf("seed resolutions: %v", err)
	}

	keys := f.pageThrough(t, "/pms-resolutions?x=1", 100, nil, func(r map[string]any) string { return r["id"].(string) })
	if len(keys) != 250 {
		t.Fatalf("paged %d checks, want 250", len(keys))
	}
	_, body := f.do(t, "GET", "/pms-resolutions?page_size=10", nil)
	if body["total"] != float64(250) {
		t.Fatalf("total = %v", body["total"])
	}
	sum := body["summary"].(map[string]any)
	if sum["window"] != float64(200) || sum["total"] != float64(200) || sum["verified"] != float64(0) {
		t.Fatalf("summary = %v", sum)
	}
	if n := len(sum["networks"].([]any)); n != 2 {
		t.Fatalf("networks = %v", sum["networks"])
	}
	outs := sum["outcomes"].([]any)
	if len(outs) != 1 || outs[0].(map[string]any)["count"] != float64(200) {
		t.Fatalf("outcomes = %v", outs)
	}
}

// ------------------------------------------------------------------------------- sign-in attempts ---

func TestIntegration_SignInAttempts_PagedWithServerSearch(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id, site_id, occurred_at,
	      submitted_room, result, guest_network_name)
	    SELECT $1, $2, now() - make_interval(secs => i), (100 + i)::text,
	           CASE WHEN i % 4 = 0 THEN 'CREDENTIAL_MISMATCH' ELSE 'VERIFIED' END, 'Lobby'
	      FROM generate_series(1, 130) i`, f.tenant, f.site); err != nil {
		t.Skipf("sign_in_attempts not seedable directly on this schema: %v", err)
	}
	keys := f.pageThrough(t, "/guest-signin-attempts?x=1", 40, nil, func(r map[string]any) string { return r["id"].(string) })
	if len(keys) != 130 {
		t.Fatalf("paged %d attempts, want 130", len(keys))
	}
	_, body := f.do(t, "GET", "/guest-signin-attempts?page_size=10", nil)
	sum := body["summary"].(map[string]any)
	if body["total"] != float64(130) || sum["total"] != float64(130) || sum["mismatch"] != float64(32) {
		t.Fatalf("total %v summary %v", body["total"], sum)
	}
	// The room filter is exact; the search header matches part of a room or a reason's words.
	if _, body = f.do(t, "GET", "/guest-signin-attempts?room=230", nil); body["total"] != float64(1) {
		t.Fatalf("room filter total = %v", body["total"])
	}
	_, body = f.getWithHeader(t, "/guest-signin-attempts?page_size=5", map[string]string{signInSearchHeader: "22"})
	if body["total"] == float64(0) || body["summary"].(map[string]any)["total"] != float64(130) {
		t.Fatalf("search: total %v summary %v", body["total"], body["summary"])
	}
}

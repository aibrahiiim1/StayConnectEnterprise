package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// AD-4: outside platform scopes, Central staff appear as "OneGate support" with no reason, address or free text.
func TestCustomerUsersSeeOneGateSupportInsteadOfStaff(t *testing.T) {
	f := newFixture(t)
	cust, _ := f.customerSite("Hotel Four")
	var own, staff string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO operators (tenant_id, email, password_hash, status)
        VALUES ($1,'gm@hotelfour.test','x','active') RETURNING id::text`, cust).Scan(&own); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO operators (email, password_hash, status)
        VALUES ('engineer@central.test','x','active') RETURNING id::text`).Scan(&staff); err != nil {
		t.Fatal(err)
	}
	app := "11111111-1111-1111-1111-111111111111"
	for _, e := range []struct{ actor, reason, ip string }{
		{staff, "internal ticket 42: customer behind on payment", "198.51.100.77"},
		{own, "our own change", "203.0.113.5"},
	} {
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO audit_log (tenant_id, actor_type, actor_id, action, target_type, target_id, ip, payload)
            VALUES ($1,'operator',$2,'appliance.moved','appliance',$3,$4::inet,$5::jsonb)`,
			cust, e.actor, app, e.ip, fmt.Sprintf(`{"reason":%q,"appliance_id":%q,"note":%q}`, e.reason, app, "note for "+e.reason)); err != nil {
			t.Fatal(err)
		}
	}

	// The appliance's event list.
	ev, err := f.base.applianceEvents(f.ctx, app, Scope{CustomerID: cust})
	if err != nil || len(ev) != 2 {
		t.Fatalf("events: %v %v", ev, err)
	}
	raw, _ := json.Marshal(ev)
	for _, leak := range []string{"engineer@central.test", "internal ticket 42", staff} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("a customer-scoped reader saw %q: %s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), "OneGate support") || !strings.Contains(string(raw), "gm@hotelfour.test") ||
		!strings.Contains(string(raw), "our own change") {
		t.Fatalf("support label or the customer's own entry missing: %s", raw)
	}
	// A platform scope still sees everything.
	ev, _ = f.base.applianceEvents(f.ctx, app, Scope{All: true})
	raw, _ = json.Marshal(ev)
	if !strings.Contains(string(raw), "engineer@central.test") || !strings.Contains(string(raw), "internal ticket 42") {
		t.Fatalf("a platform reader lost the detail: %s", raw)
	}

	// The audit list.
	sess := &auth.Session{OperatorID: own, Email: "gm@hotelfour.test", Roles: []string{"tenant_owner"}, DefaultTenantID: cust, CreatedAt: time.Now()}
	req := httptest.NewRequest(http.MethodGet, "/cloud/v1/audit", nil)
	rec := httptest.NewRecorder()
	f.base.listAudit(rec, req.WithContext(auth.ContextWithSession(req.Context(), sess)))
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{staff, "internal ticket 42", "198.51.100.77", "note for internal ticket"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the audit list leaked %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "OneGate support") || !strings.Contains(body, "our own change") || !strings.Contains(body, "203.0.113.5") {
		t.Fatalf("audit list lost the label or the customer's own entry: %s", body)
	}
}

// AD-5: changing an administrator's password, email or two-step setting needs the owner role.
func TestResettingAnAdministratorNeedsTheOwnerRole(t *testing.T) {
	f := newFixture(t)
	mk := func(email, role string) string {
		var id string
		if err := f.pool.QueryRow(f.ctx, `INSERT INTO operators (email, password_hash, status) VALUES ($1,'x','active') RETURNING id::text`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,$2)`, id, role); err != nil {
			t.Fatal(err)
		}
		return id
	}
	peer := mk("peer@central.test", "platform_admin")
	support := mk("support@central.test", "platform_support")
	adminID := mk("admin@central.test", "platform_admin")
	admin := &auth.Session{OperatorID: adminID, Email: "admin@central.test", Roles: []string{"platform_admin"}, CreatedAt: time.Now()}

	call := func(h http.HandlerFunc, id string, body any) int {
		return f.operatorCallAs(admin, h, http.MethodPost, map[string]string{"id": id}, body).Code
	}
	pw := map[string]string{"password": "a-long-new-password"}
	if c := call(f.base.teamPassword, peer, pw); c != http.StatusForbidden {
		t.Fatalf("an admin reset a peer admin's password: %d", c)
	}
	if c := call(f.base.teamMFAReset, peer, map[string]string{"reason": "locked out"}); c != http.StatusForbidden {
		t.Fatalf("an admin reset a peer admin's two-step: %d", c)
	}
	if rec := f.operatorCallAs(admin, f.base.patchTeam, http.MethodPatch, map[string]string{"id": peer}, map[string]string{"email": "takeover@central.test"}); rec.Code != http.StatusForbidden {
		t.Fatalf("an admin changed a peer admin's email: %d", rec.Code)
	}
	// An admin may still reset support staff; an owner may reset an admin.
	if c := call(f.base.teamPassword, support, pw); c != http.StatusNoContent {
		t.Fatalf("an admin could not reset support staff: %d", c)
	}
	if c := f.operatorCallAs(f.session, f.base.teamPassword, http.MethodPost, map[string]string{"id": peer}, pw).Code; c != http.StatusNoContent {
		t.Fatalf("an owner could not reset an admin: %d", c)
	}
	if c := f.operatorCallAs(f.session, f.base.teamMFAReset, http.MethodPost, map[string]string{"id": peer}, map[string]string{"reason": "lost phone"}).Code; c != http.StatusOK {
		t.Fatalf("an owner could not reset an admin's two-step: %d", c)
	}
}

// AD-54: the replacement window and the retirement acknowledgement wait are settings.
func TestReplacementWindowAndRetirementAckAreSettings(t *testing.T) {
	f := newFixture(t)
	if h := f.base.replacementWindowHours(f.ctx, f.pool); h != 72 {
		t.Fatalf("default window = %d", h)
	}
	if d := f.base.retirementAckTimeout(f.ctx, f.pool); d != 10*time.Minute {
		t.Fatalf("default ack timeout = %v", d)
	}
	rec := f.operatorCallAs(f.session, f.base.patchSecuritySettings, http.MethodPatch, nil,
		map[string]any{"replacement_window_hours": 24, "retirement_ack_minutes": 30, "reason": "test"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	if h := f.base.replacementWindowHours(f.ctx, f.pool); h != 24 {
		t.Fatalf("window = %d", h)
	}
	if d := f.base.retirementAckTimeout(f.ctx, f.pool); d != 30*time.Minute {
		t.Fatalf("ack timeout = %v", d)
	}
	for _, bad := range []map[string]any{{"replacement_window_hours": 0}, {"replacement_window_hours": 721}, {"retirement_ack_minutes": 0}, {"retirement_ack_minutes": 121}} {
		bad["reason"] = "test"
		if c := f.operatorCallAs(f.session, f.base.patchSecuritySettings, http.MethodPatch, nil, bad).Code; c != http.StatusBadRequest {
			t.Fatalf("%v accepted: %d", bad, c)
		}
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='settings.security_updated' AND payload ? 'replacement_window_hours'`); n != 1 {
		t.Fatal("the change was not audited")
	}
}

// AD-54: not-modified fetches share one row an hour; served and denied ones are one row each.
func TestFetchLogCountsNotModifiedPerHour(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Fetch")
	_ = cust
	var app string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO appliances (serial, tenant_id, site_id, lifecycle_state) VALUES ('FETCH-1',$1,$2,'assigned') RETURNING id::text`, cust, site).Scan(&app); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		logFetch(f.ctx, f.pool, app, "", "not-modified:acked")
	}
	logFetch(f.ctx, f.pool, app, "fpr", "served")
	logFetch(f.ctx, f.pool, app, "fpr", "served")
	logFetch(f.ctx, f.pool, app, "", "denied:mtls")
	if n := f.count(`SELECT count(*) FROM appliance_assignment_fetch_log WHERE outcome='not-modified:acked'`); n != 1 {
		t.Fatalf("%d not-modified rows", n)
	}
	if n := f.count(`SELECT hits FROM appliance_assignment_fetch_log WHERE outcome='not-modified:acked'`); n != 5 {
		t.Fatalf("hits = %d", n)
	}
	if n := f.count(`SELECT count(*) FROM appliance_assignment_fetch_log WHERE outcome IN ('served','denied:mtls')`); n != 3 {
		t.Fatalf("%d served/denied rows", n)
	}
}

// AD-55: the fleet list pages in SQL with a total; unpaged callers still get everything.
func TestApplianceListPagesInSQL(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Paging")
	for i := 0; i < 7; i++ {
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliances (serial, tenant_id, site_id, lifecycle_state, hostname)
            VALUES ($1,$2,$3,'assigned',$4)`, fmt.Sprintf("PG-%02d", i), cust, site, map[bool]string{true: "lobby", false: "bar"}[i%2 == 0]); err != nil {
			t.Fatal(err)
		}
	}
	get := func(qs string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/cloud/v1/appliances?"+qs, nil)
		req = req.WithContext(auth.ContextWithSession(req.Context(), f.session))
		rec := httptest.NewRecorder()
		f.base.listAppliances(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", qs, rec.Code, rec.Body.String())
		}
		return decode(t, rec)
	}
	n := func(m map[string]any) int { return len(m["items"].([]any)) }
	if m := get(""); n(m) != 7 || m["total"] != nil {
		t.Fatalf("unpaged: %d items, total %v", n(m), m["total"])
	}
	m := get("limit=3&page=1")
	if n(m) != 3 || m["total"].(float64) != 7 || m["next_page"].(float64) != 2 {
		t.Fatalf("page 1: %v", m)
	}
	if m := get("limit=3&page=3"); n(m) != 1 || m["next_page"] != nil {
		t.Fatalf("page 3: %v", m)
	}
	if m := get("limit=3&page=9"); n(m) != 0 || m["total"].(float64) != 7 {
		t.Fatalf("page past the end: %v", m)
	}
	if m := get("limit=100&q=LOBBY"); n(m) != 4 || m["total"].(float64) != 4 {
		t.Fatalf("text filter: %v", m)
	}
	// A derived-state filter is counted before the page is cut.
	if m := get("limit=2&connection=online"); m["total"].(float64) != 0 {
		t.Fatalf("derived filter: %v", m)
	}
	if m := get("limit=2&site_id=" + site); m["total"].(float64) != 7 {
		t.Fatalf("site filter: %v", m)
	}
}

// AD-65: deleting an appliance keeps its lifecycle and certificate history in the archive.
func TestDeletedApplianceHistoryIsArchived(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Archive")
	var app string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO appliances (serial, tenant_id, site_id, lifecycle_state, deletion_started_at)
        VALUES ('ARCH-1',$1,$2,'decommissioned', now()) RETURNING id::text`, cust, site).Scan(&app); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliance_lifecycle_events (appliance_id, from_state, to_state, actor, reason)
        VALUES ($1,'assigned','decommissioned','ops@central.test','retired')`, app); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliance_assignments (appliance_id, tenant_id, site_id, reason) VALUES ($1,$2,$3,'first')`, app, cust, site); err != nil {
		t.Fatal(err)
	}
	// The same statement the delete handler runs, then the delete that cascades.
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	if _, err := tx.Exec(f.ctx, archiveApplianceHistorySQL, app, f.session.OperatorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `DELETE FROM appliances WHERE id=$1`, app); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM appliance_lifecycle_events WHERE appliance_id=$1`, app); n != 0 {
		t.Fatal("the cascade did not run")
	}
	if n := f.count(`SELECT count(*) FROM appliance_history_archive WHERE appliance_id=$1 AND serial='ARCH-1'
        AND jsonb_array_length(lifecycle_events)=1 AND jsonb_array_length(assignment_moves)=1`, app); n != 1 {
		t.Fatal("the history was not archived")
	}
}

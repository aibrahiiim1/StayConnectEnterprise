package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// A data export masks other people's emails, and a customer administrator's email correction rewrites only what is
// attributable to that user, not every matching string in the platform history.
func TestEmailCorrectionAndExportStayWithTheUser(t *testing.T) {
	f := newFixture(t)
	cust, _ := f.customerSite("Scope Hotel")
	insert := func(email, tenant string) string {
		t.Helper()
		var id string
		if err := f.pool.QueryRow(f.ctx, `INSERT INTO operators (tenant_id, email, password_hash, status)
			VALUES (NULLIF($2,'')::uuid, $1, 'x', 'active') RETURNING id::text`, email, tenant).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	admin := insert("hotel.admin@scope.test", cust)
	user := insert("front.desk@scope.test", cust)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_roles (operator_id, tenant_id, role) VALUES ($1,$3,'tenant_admin'), ($2,$3,'viewer')`,
		admin, user, cust); err != nil {
		t.Fatal(err)
	}
	// Includes an entry of the platform owner's that merely contains the user's address as a string.
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO audit_log (ts, actor_type, actor_id, action, target_type, target_id, payload) VALUES
		  (now(), 'operator', $1, 'user.created', 'operator', 'x-1', '{"email":"new.colleague@scope.test","by":"front.desk@scope.test"}'),
		  (now(), 'operator', $2, 'user.created', 'operator', $1, '{"email":"front.desk@scope.test","note":"asked by hotel.admin@scope.test"}'),
		  (now(), 'operator', $3, 'support.note', 'tenant', $4, '{"text":"front.desk@scope.test"}')`,
		user, admin, f.session.OperatorID, cust); err != nil {
		t.Fatal(err)
	}

	rec := f.operatorCall(f.base.userDataExport, http.MethodPost, user, map[string]string{"reason": "they asked in writing"})
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		Activity []struct{ Details map[string]any } `json:"activity"`
		About    []struct{ Details map[string]any } `json:"about_this_account"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc.Activity) != 1 || len(doc.About) != 1 {
		t.Fatalf("export shape: %s", rec.Body.String())
	}
	if doc.Activity[0].Details["email"] != "[other user]" || doc.Activity[0].Details["by"] != "front.desk@scope.test" {
		t.Fatalf("activity details = %v", doc.Activity[0].Details)
	}
	if doc.About[0].Details["email"] != "front.desk@scope.test" || doc.About[0].Details["note"] != "asked by [other user]" {
		t.Fatalf("about details = %v", doc.About[0].Details)
	}
	body := rec.Body.String()
	for _, other := range []string{"new.colleague@scope.test", "hotel.admin@scope.test", "owner@central.test"} {
		if strings.Contains(body, other) {
			t.Errorf("the export contains %s", other)
		}
	}

	tenantAdmin := &auth.Session{OperatorID: admin, Email: "hotel.admin@scope.test", DefaultTenantID: cust, Roles: []string{"tenant_admin"}}
	patch := func(email string) int {
		return f.operatorCallAs(tenantAdmin, f.base.patchCustomerUser, http.MethodPatch,
			map[string]string{"id": cust, "uid": user}, map[string]string{"email": email}).Code
	}
	for _, bad := range []string{"front desk@scope.test", "Front <fd@scope.test>", "fd@scope", "fd@@scope.test"} {
		if code := patch(bad); code != http.StatusBadRequest {
			t.Errorf("correcting to %q = %d, want 400", bad, code)
		}
	}
	if code := patch("Reception@Scope.test"); code != http.StatusOK {
		t.Fatalf("correction = %d", code)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE (actor_id=$1 OR target_id=$1) AND payload::text LIKE '%front.desk@scope.test%'`, user); n != 0 {
		t.Errorf("%d of the user's own entries still carry the old address", n)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE (actor_id=$1 OR target_id=$1) AND payload::text LIKE '%reception@scope.test%'`, user); n != 2 {
		t.Errorf("%d of the user's entries carry the corrected address, want 2", n)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='support.note' AND payload->>'text' = 'front.desk@scope.test'`); n != 1 {
		t.Error("the correction rewrote an entry that is not the user's")
	}
}

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestACentralUsersDataIsExportedCorrectedAndForgotten(t *testing.T) {
	f := newFixture(t)
	var colleague string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO operators (email, display_name, password_hash, status)
		VALUES ('grace@central.test','Grace','x','active') RETURNING id::text`).Scan(&colleague); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'platform_support')`, colleague); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO audit_log (ts, actor_type, actor_id, action, target_type, target_id, payload) VALUES
		  (now(), 'operator', $1, 'appliance.activated', 'appliance', 'a-1', '{}'::jsonb),
		  (now(), 'operator', 'someone', 'user.created', 'operator', $1, '{"email":"grace@central.test","role":"platform_support"}'::jsonb)`,
		colleague); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE central_security_settings SET updated_by = 'grace@central.test'`); err != nil {
		t.Fatal(err)
	}

	if rec := f.operatorCall(f.base.userDataExport, http.MethodPost, colleague, map[string]string{"reason": ""}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an export without a reason: %d", rec.Code)
	}
	if rec := f.operatorCall(f.base.userDataExport, http.MethodPost, "00000000-0000-4000-8000-000000000000", map[string]string{"reason": "asked"}); rec.Code != http.StatusNotFound {
		t.Fatalf("an export of nobody: %d", rec.Code)
	}
	rec := f.operatorCall(f.base.userDataExport, http.MethodPost, colleague, map[string]string{"reason": "she asked in writing"})
	var doc struct {
		Account  map[string]any   `json:"account"`
		Roles    []map[string]any `json:"roles"`
		Activity []map[string]any `json:"activity"`
		About    []map[string]any `json:"about_this_account"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &doc) != nil {
		t.Fatalf("export = %d %s", rec.Code, rec.Body.String())
	}
	if doc.Account["email"] != "grace@central.test" || len(doc.Roles) != 1 || len(doc.Activity) != 1 || len(doc.About) != 1 {
		t.Fatalf("export is incomplete: %s", rec.Body.String())
	}
	if f.count(`SELECT count(*) FROM audit_log WHERE action='user.data_exported' AND target_id=$1`, colleague) != 1 {
		t.Fatal("the export was not audited")
	}

	if rec := f.operatorCall(f.base.patchTeam, http.MethodPatch, colleague, map[string]string{"email": "Grace.Hopper@central.test"}); rec.Code != http.StatusOK {
		t.Fatalf("correct email = %d %s", rec.Code, rec.Body.String())
	}
	if f.count(`SELECT count(*) FROM operators WHERE id=$1 AND email='grace.hopper@central.test'`, colleague) != 1 ||
		f.count(`SELECT count(*) FROM audit_log WHERE payload::text LIKE '%grace@central.test%'`) != 0 ||
		f.count(`SELECT count(*) FROM central_security_settings WHERE updated_by='grace.hopper@central.test'`) != 1 {
		t.Fatal("the correction did not reach the history")
	}
	if rec := f.operatorCall(f.base.patchTeam, http.MethodPatch, colleague, map[string]string{"email": "owner@central.test"}); rec.Code != http.StatusConflict {
		t.Fatalf("taking another user's email = %d", rec.Code)
	}

	if rec := f.operatorCall(f.base.deleteTeam, http.MethodDelete, colleague, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	label := "removed-user-" + colleague[:8]
	for _, q := range []string{
		`SELECT count(*) FROM audit_log WHERE payload::text ILIKE '%grace%'`,
		`SELECT count(*) FROM central_security_settings WHERE updated_by ILIKE '%grace%'`,
	} {
		if n := f.count(q); n != 0 {
			t.Errorf("%s = %d after removal", q, n)
		}
	}
	if f.count(`SELECT count(*) FROM central_security_settings WHERE updated_by=$1`, label) != 1 {
		t.Error("the settings do not name a removed user")
	}
	var payload string
	_ = f.pool.QueryRow(f.ctx, `SELECT payload::text FROM audit_log WHERE action='user.removed' AND target_id=$1`, colleague).Scan(&payload)
	if strings.Contains(payload, "@") {
		t.Errorf("the removal entry names the user: %s", payload)
	}
}

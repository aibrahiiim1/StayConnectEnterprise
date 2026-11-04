//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// as is the fixture signed in as another staff account, with the roles a sign-in would load.
func (f *apiFixture) as(t *testing.T, operatorID string) *apiFixture {
	t.Helper()
	roles, err := f.app.loadRoles(context.Background(), operatorID)
	if err != nil {
		t.Fatalf("load roles: %v", err)
	}
	g := *f
	g.operator = operatorID
	g.sessTok = f.app.sessions.create(&session{OperatorID: operatorID, Email: "staff@test.local", Roles: roles})
	return &g
}

func TestIntegration_CustomRoles_DefineAssignAndEnforce(t *testing.T) {
	f := newAPI(t, "site_admin")
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	name := "Night desk " + sfx[len(sfx)-6:]
	role := func(n string, perms map[string]string, pw string) (int, map[string]any) {
		return f.do(t, "POST", "/roles", map[string]any{"name": n, "description": "Covers the night", "permissions": perms, "password": pw})
	}

	// What a custom role may not be.
	for _, c := range []struct {
		why   string
		name  string
		perms map[string]string
		pw    string
		want  int
	}{
		{"without the password", name, map[string]string{"sessions": "write"}, "", 401},
		{"managing staff accounts", name, map[string]string{"operators": "write"}, f.password, 400},
		{"switching modules", name, map[string]string{"modules": "write"}, f.password, 400},
		{"managing PMS routing", name, map[string]string{"pms-routing": "write"}, f.password, 400},
		{"an unknown feature", name, map[string]string{"roles": "write"}, f.password, 400},
		{"a built-in role's name", "Site viewer", map[string]string{"sessions": "read"}, f.password, 400},
		{"no feature at all", name, map[string]string{}, f.password, 400},
	} {
		if st, body := role(c.name, c.perms, c.pw); st != c.want {
			t.Errorf("a role %s = %d %v, want %d", c.why, st, body, c.want)
		}
	}

	st, body := role(name, map[string]string{"sessions": "write", "usage": "read"}, f.password)
	id, _ := body["id"].(string)
	if st != 201 || id == "" {
		t.Fatalf("create role = %d %v", st, body)
	}
	if st, _ := role(" night DESK "+sfx[len(sfx)-6:], map[string]string{"usage": "read"}, f.password); st != 409 {
		t.Errorf("a second role with the same name = %d, want 409", st)
	}

	// Assigned like a built-in role, by staff management.
	st, body = f.do(t, "POST", "/operators", map[string]any{"email": "night-" + sfx + "@test.local", "password": "Night-Desk-Pa55word!",
		"role": "custom:" + id, "your_password": f.password})
	op, _ := body["id"].(string)
	if st != 201 || op == "" {
		t.Fatalf("create operator with the custom role = %d %v", st, body)
	}
	if names, _ := body["role_names"].(map[string]any); names["custom:"+id] != name {
		t.Errorf("the staff account does not name its custom role: %v", body)
	}

	n := f.as(t, op)
	if st, _ := n.do(t, "GET", "/sessions", nil); st != 200 {
		t.Errorf("View/Manage sessions: GET = %d, want 200", st)
	}
	if st, _ := n.do(t, "POST", "/sessions/00000000-0000-4000-8000-000000000000/disconnect", map[string]any{}); st == 403 {
		t.Errorf("Manage sessions: a write was refused by role")
	}
	for _, path := range []string{"/guest-accounts", "/roles", "/audit"} {
		if st, _ := n.do(t, "GET", path, nil); st != 403 {
			t.Errorf("GET %s with no access = %d, want 403", path, st)
		}
	}
	// No escalation: the holder cannot make staff accounts or roles.
	if st, _ := n.do(t, "POST", "/operators", map[string]any{"email": "x-" + sfx + "@test.local", "password": "Another-Pa55word!", "role": "site_admin", "your_password": "Night-Desk-Pa55word!"}); st != 403 {
		t.Errorf("a custom-role holder created a staff account: %d", st)
	}
	if st, _ := n.do(t, "POST", "/roles", map[string]any{"name": "Mine", "permissions": map[string]string{"usage": "read"}, "password": "Night-Desk-Pa55word!"}); st != 403 {
		t.Errorf("a custom-role holder created a role: %d", st)
	}
	_, me := n.do(t, "GET", "/auth/whoami", nil)
	if cr, _ := me["custom_roles"].(map[string]any); cr["custom:"+id] == nil {
		t.Errorf("whoami does not describe the custom role: %v", me)
	}

	// An edit applies to the holder's next request, without signing in again.
	if st, body := f.do(t, "PATCH", "/roles/"+id, map[string]any{"permissions": map[string]string{"usage": "read"}, "password": f.password}); st != 200 {
		t.Fatalf("edit role = %d %v", st, body)
	}
	if st, _ := n.do(t, "GET", "/sessions", nil); st != 403 {
		t.Errorf("sessions after the role lost it = %d, want 403", st)
	}

	// A role in use is not deleted; removed from the staff account first, it is.
	if st, body := f.do(t, "DELETE", "/roles/"+id, map[string]any{"password": f.password}); st != 409 || body["error"] != "role_in_use" {
		t.Errorf("delete a role in use = %d %v, want 409 role_in_use", st, body)
	}
	if st, body := f.do(t, "DELETE", "/operators/"+op+"/roles/custom:"+id, map[string]any{"your_password": f.password}); st != 204 {
		t.Fatalf("remove the role from the staff account = %d %v", st, body)
	}
	if st, body := f.do(t, "DELETE", "/roles/"+id, map[string]any{"password": f.password}); st != 204 {
		t.Fatalf("delete the unused role = %d %v", st, body)
	}

	var audited, leaked int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE target_id=$1 AND action IN ('role.created','role.updated','role.deleted')`, id).Scan(&audited)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE payload::text LIKE '%'||$1||'%'`, f.password).Scan(&leaked)
	if audited != 3 || leaked != 0 {
		t.Errorf("audit: %d role entries (want 3), %d carrying the password (want 0)", audited, leaked)
	}

	// The built-in roles are reported exactly as compiled.
	_, list := f.do(t, "GET", "/roles", nil)
	built, _ := list["built_in"].([]any)
	if len(built) != 7 {
		t.Fatalf("built-in roles = %d, want 7", len(built))
	}
	for _, b := range built {
		m := b.(map[string]any)
		key := m["key"].(string)
		if key == "site_admin" {
			continue
		}
		got := m["permissions"].(map[string]any)
		if len(got) != len(rolePerms[key]) {
			t.Errorf("%s reported with %d permissions, compiled with %d", key, len(got), len(rolePerms[key]))
		}
		for res, p := range rolePerms[key] {
			if got[res] != permName(p) {
				t.Errorf("%s %s reported %v, compiled %s", key, res, got[res], permName(p))
			}
		}
	}
}

// The Admin Console's database role holds what custom roles need, as svc_edged.
func TestIntegration_CustomRoles_RunAsTheAdminConsoleRole(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set; skipping the svc_edged privilege proof")
	}
	ctx := context.Background()
	ep, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	for _, q := range []string{
		`SELECT count(*) FROM public.custom_roles`,
		`SELECT count(*) FROM public.operator_custom_roles`,
	} {
		if _, err := ep.Exec(ctx, q); err != nil {
			t.Errorf("svc_edged cannot run %q: %v", q, err)
		}
	}
	for _, tbl := range []string{"public.custom_roles", "public.operator_custom_roles"} {
		for _, priv := range []string{"INSERT", "UPDATE", "DELETE"} {
			var ok bool
			if err := ep.QueryRow(ctx, `SELECT has_table_privilege(current_user, $1, $2)`, tbl, priv).Scan(&ok); err != nil || !ok {
				t.Errorf("svc_edged lacks %s on %s (%v)", priv, tbl, err)
			}
		}
	}
}

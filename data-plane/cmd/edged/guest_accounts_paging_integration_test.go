//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"
)

// CLIENT ACCOUNTS: the generated-password format setting (migration 0104) and the paged, newest-first list.

func (f *apiFixture) getAccounts(t *testing.T, query, search string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("GET", f.srv.URL+"/edge/v1/guest-accounts/"+query, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if search != "" {
		req.Header.Set(accountSearchHeader, search)
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

func TestIntegration_AccountPasswordFormat_DefaultsBoundsAndRefusals(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()

	st, body := f.do(t, "GET", "/account-password-settings/", nil)
	if st != 200 || body["password_style"] != "mixed" || body["password_length"] != float64(14) ||
		body["config_version"] != float64(0) {
		t.Fatalf("defaults = %d %v", st, body)
	}
	limits := body["limits"].(map[string]any)
	if limits["max_length"] != float64(32) || limits["entropy_floor_bits"] != float64(40) {
		t.Fatalf("limits = %v", limits)
	}
	mins := map[string]float64{}
	for _, s := range limits["styles"].([]any) {
		m := s.(map[string]any)
		mins[m["key"].(string)] = m["min_length"].(float64)
	}
	if mins["mixed"] != 7 || mins["upper_digits"] != 9 || mins["lower_digits"] != 8 || mins["digits"] != 14 {
		t.Fatalf("style minimums = %v", mins)
	}

	// Below the floor, unknown style, missing reason: all refused, nothing stored.
	for _, bad := range []map[string]any{
		{"password_style": "digits", "password_length": 13, "reason": "keypad"},
		{"password_style": "mixed", "password_length": 33, "reason": "long"},
		{"password_style": "numbers", "password_length": 14, "reason": "nope"},
		{"password_style": "digits", "password_length": 16},
		{"password_style": "digits", "password_length": 16, "reason": "no"},
	} {
		if st, b := f.do(t, "PUT", "/account-password-settings/", bad); st != 400 {
			t.Fatalf("%v accepted: %d %v", bad, st, b)
		}
	}

	// The setter enforces the floor itself, and so does the table, for a caller that skipped edged.
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.account_password_settings_set($1,$2,'upper_digits',8,'x','floor test')`,
		f.tenant, f.site); err == nil {
		t.Fatal("the SQL setter stored upper_digits/8, below the 40-bit floor")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.site_account_password_settings
		(tenant_id, site_id, password_style, password_length) VALUES ($1,$2,'lower_digits',7)`,
		f.tenant, f.site); err == nil {
		t.Fatal("the table CHECK accepted lower_digits/7")
	}

	st, body = f.do(t, "PUT", "/account-password-settings/",
		map[string]any{"password_style": "digits", "password_length": 16, "reason": "keypad kiosk"})
	if st != 200 || body["config_version"] != float64(1) {
		t.Fatalf("save = %d %v", st, body)
	}
	st, body = f.do(t, "GET", "/account-password-settings/", nil)
	if st != 200 || body["password_style"] != "digits" || body["password_length"] != float64(16) ||
		body["updated_by"] != "op@test.local" {
		t.Fatalf("after save = %v", body)
	}

	st, body = f.do(t, "GET", "/account-password-settings/changes", nil)
	ch := body["changes"].([]any)
	if st != 200 || len(ch) != 1 {
		t.Fatalf("changes = %d %v", st, body)
	}
	c := ch[0].(map[string]any)
	if c["changed_by"] != "op@test.local" || c["change_reason"] != "keypad kiosk" ||
		c["new_password_style"] != "digits" || c["old_password_style"] != nil {
		t.Fatalf("change row = %v", c)
	}
	var audited int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM public.audit_log
		WHERE action='guest_account.password_format_changed' AND payload->>'password_style'='digits'
		  AND actor_id=$1`, f.operator).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit rows = %d (%v)", audited, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.account_password_settings_changes SET change_reason='x'
		WHERE tenant_id=$1`, f.tenant); err == nil {
		t.Fatal("the change history is not append-only")
	}
}

func TestIntegration_AccountPasswordFormat_ReadOnlyRoleCannotChangeIt(t *testing.T) {
	f := newAPI(t, "front_office_operator")
	if st, b := f.do(t, "GET", "/account-password-settings/", nil); st != 200 {
		t.Fatalf("read = %d %v", st, b)
	}
	if st, b := f.do(t, "PUT", "/account-password-settings/",
		map[string]any{"password_style": "digits", "password_length": 16, "reason": "desk tries"}); st != 403 {
		t.Fatalf("the desk changed the format: %d %v", st, b)
	}
}

func TestIntegration_GeneratedPasswordFollowsTheFormat(t *testing.T) {
	f := newAPI(t)

	// Default: mixed, 14 -- what the generator produced before 0104.
	st, body := f.do(t, "POST", "/guest-accounts/", map[string]any{"username": "gen-default", "generate": true})
	if st != 201 {
		t.Fatalf("create = %d %v", st, body)
	}
	if pw, _ := body["generated_password"].(string); !regexp.MustCompile(`^[A-HJ-NP-Za-km-z2-9]{14}$`).MatchString(pw) {
		t.Fatalf("default generated %q", pw)
	}

	if st, b := f.do(t, "PUT", "/account-password-settings/",
		map[string]any{"password_style": "digits", "password_length": 16, "reason": "keypad kiosk"}); st != 200 {
		t.Fatalf("save = %d %v", st, b)
	}
	st, body = f.do(t, "POST", "/guest-accounts/", map[string]any{"username": "gen-digits", "generate": true})
	if st != 201 {
		t.Fatalf("create = %d %v", st, body)
	}
	if pw, _ := body["generated_password"].(string); !regexp.MustCompile(`^[2-9]{16}$`).MatchString(pw) {
		t.Fatalf("digits/16 generated %q", pw)
	}
	id := body["account"].(map[string]any)["id"].(string)

	if st, b := f.do(t, "PUT", "/account-password-settings/",
		map[string]any{"password_style": "upper_digits", "password_length": 10, "reason": "printed slips"}); st != 200 {
		t.Fatalf("save = %d %v", st, b)
	}
	st, body = f.do(t, "POST", "/guest-accounts/"+id+"/set-password", map[string]any{"generate": true})
	if st != 200 {
		t.Fatalf("set-password = %d %v", st, body)
	}
	if pw, _ := body["generated_password"].(string); !regexp.MustCompile(`^[A-HJKMNP-Z2-9]{10}$`).MatchString(pw) {
		t.Fatalf("upper_digits/10 generated %q", pw)
	}

	// An operator-typed password keeps its own rules: one character is still allowed.
	if st, b := f.do(t, "POST", "/guest-accounts/", map[string]any{"username": "typed", "password": "x"}); st != 201 {
		t.Fatalf("typed one-character password refused: %d %v", st, b)
	}
}

func TestIntegration_GuestAccounts_PagedNewestFirstWithTotalsOverEveryMatch(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	// 115 accounts with creation times (acct-001 oldest), 10 from before 0104 with none, three disabled. Two carry
	// a locked_until value, which the summary must NOT count: nothing in the product sets that column and an
	// always-zero "locked out" counter was a measurement the domain cannot make (guest_account_disconnect_integration_test.go).
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts
		  (tenant_id, site_id, username, password_hash, display_name, enabled, created_at, locked_until)
		SELECT $1, $2, 'acct-' || lpad(i::text, 3, '0'), 'x', 'Guest ' || i, i % 40 <> 0,
		       now() - (200 - i) * interval '1 minute',
		       CASE WHEN i IN (7, 8) THEN now() + interval '1 hour' END
		  FROM generate_series(1, 115) i`, f.tenant, f.site); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts
		  (tenant_id, site_id, username, password_hash, created_at)
		SELECT $1, $2, 'legacy-' || chr(96 + i), 'x', NULL FROM generate_series(1, 10) i`,
		f.tenant, f.site); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	var order []string
	for page := 1; ; page++ {
		st, body := f.getAccounts(t, fmt.Sprintf("?page=%d&page_size=50", page), "")
		if st != 200 {
			t.Fatalf("page %d = %d %v", page, st, body)
		}
		sum := body["summary"].(map[string]any)
		if sum["total"] != float64(125) || sum["disabled"] != float64(2) || sum["enabled"] != float64(123) ||
			sum["devices_online"] != float64(0) {
			t.Fatalf("summary = %v", sum)
		}
		if _, present := sum["locked"]; present {
			t.Fatalf("the summary reports an account lockout count: %v", sum)
		}
		if body["authority"] != "iam_v2" || body["page"] != float64(page) || body["page_size"] != float64(50) {
			t.Fatalf("envelope = %v", body)
		}
		for _, r := range body["data"].([]any) {
			order = append(order, r.(map[string]any)["username"].(string))
		}
		if !body["meta"].(map[string]any)["has_more"].(bool) {
			break
		}
		if page > 5 {
			t.Fatal("paging never ended")
		}
	}
	if len(order) != 125 {
		t.Fatalf("paged %d accounts, want 125", len(order))
	}
	if order[0] != "acct-115" || order[114] != "acct-001" {
		t.Fatalf("newest first: got %s ... %s", order[0], order[114])
	}
	// Undated accounts sort last, by username.
	if order[115] != "legacy-a" || order[124] != "legacy-j" {
		t.Fatalf("undated tail = %v", order[115:])
	}

	// Search is server-side, over every account, by username or name; % is literal.
	st, body := f.getAccounts(t, "?page_size=10", "acct-00")
	if st != 200 || body["summary"].(map[string]any)["total"] != float64(9) {
		t.Fatalf("search = %d %v", st, body["summary"])
	}
	if _, body = f.getAccounts(t, "", "Guest 101"); body["summary"].(map[string]any)["total"] != float64(1) {
		t.Fatalf("name search = %v", body["summary"])
	}
	if _, body = f.getAccounts(t, "", "%"); body["summary"].(map[string]any)["total"] != float64(0) {
		t.Fatalf("a literal %% matched %v", body["summary"])
	}

	if st, body = f.getAccounts(t, "?status=disabled", ""); st != 200 || len(body["data"].([]any)) != 2 {
		t.Fatalf("disabled filter = %d %v", st, body)
	}
	if st, body = f.getAccounts(t, "?status=locked", ""); st != 400 {
		t.Fatalf("locked filter = %d %v (it can only ever match nothing)", st, body)
	}
	if st, _ = f.getAccounts(t, "?status=bogus", ""); st != 400 {
		t.Fatalf("unknown status = %d", st)
	}
	if st, _ = f.getAccounts(t, "?page_size=500", ""); st != 400 {
		t.Fatalf("oversized page = %d", st)
	}

	// A newly created account goes to the top, with its creation time.
	if st, b := f.do(t, "POST", "/guest-accounts/", map[string]any{"username": "zz-newest", "password": "pw"}); st != 201 {
		t.Fatalf("create = %d %v", st, b)
	}
	_, body = f.getAccounts(t, "?page_size=1", "")
	top := body["data"].([]any)[0].(map[string]any)
	if top["username"] != "zz-newest" || top["created_at"] == nil {
		t.Fatalf("top of the list = %v", top)
	}
}

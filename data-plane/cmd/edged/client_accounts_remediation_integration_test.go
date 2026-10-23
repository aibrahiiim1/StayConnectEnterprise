//go:build integration

package main

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func entHead(t *testing.T, f *apiFixture, ent string) (status, reason, window string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(), `
	    SELECT e.status, COALESCE((SELECT t.reason FROM iam_v2.entitlement_state_transitions t
	                                WHERE t.entitlement_id = e.id AND t.superseded_by IS NULL
	                                ORDER BY t.seq DESC LIMIT 1), ''), e.window_ends_at::text
	      FROM iam_v2.entitlements e WHERE e.id = $1`, ent).Scan(&status, &reason, &window); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	return
}

func TestIntegration_ClientAccountDisable_EndsSessionsAndSuspendsThePackage_EnableResumesIt(t *testing.T) {
	f := newAPI(t)
	fs, client := newFakeSCD(t, f)
	f.app.scd = client
	acct, ent, sessions := seedAccountWithSessions(t, f, "da-online", 1000, "10.91.0.11", "10.91.0.12")
	_, otherEnt, otherSessions := seedAccountWithSessions(t, f, "da-other", 5, "10.91.0.21")
	_, _, windowBefore := entHead(t, f, ent)

	st, body := f.do(t, "PATCH", "/guest-accounts/"+acct, map[string]any{"enabled": false})
	if st != 200 || body["enabled"] != false || body["packages_suspended"] != float64(1) ||
		body["disconnected_sessions"] != float64(2) {
		t.Fatalf("disable = %d %v", st, body)
	}
	if _, bad := body["disconnect_failures"]; bad {
		t.Fatalf("a clean disable reported failures: %v", body)
	}
	if got := fs.seen(); len(got) != 2 {
		t.Fatalf("scd was asked to revoke %v, want the account's two addresses", got)
	}
	for ip, s := range sessionStates(t, f, sessions) {
		if s[0] != "ended" {
			t.Fatalf("session %s is %q after Disable, want ended", ip, s[0])
		}
	}
	status, reason, window := entHead(t, f, ent)
	if status != "SUSPENDED" || reason != "ACCOUNT_DISABLED" {
		t.Fatalf("after Disable the package is %s/%s, want SUSPENDED/ACCOUNT_DISABLED", status, reason)
	}
	if window != windowBefore {
		t.Fatalf("Disable moved the validity window %s -> %s; the clock must keep running", windowBefore, window)
	}
	if s, _, _ := entHead(t, f, otherEnt); s != "ACTIVE" {
		t.Fatalf("another account's package became %s", s)
	}
	for ip, s := range sessionStates(t, f, otherSessions) {
		if s[0] != "active" {
			t.Fatalf("another account's session %s became %q", ip, s[0])
		}
	}
	if auditCount(t, f, "guest_account.disabled", acct) != 1 {
		t.Fatal("Disable was not audited as guest_account.disabled")
	}

	st, body = f.do(t, "PATCH", "/guest-accounts/"+acct, map[string]any{"enabled": true})
	if st != 200 || body["enabled"] != true || body["packages_resumed"] != float64(1) {
		t.Fatalf("enable = %d %v", st, body)
	}
	if status, reason, _ := entHead(t, f, ent); status != "ACTIVE" || reason != "ACCOUNT_ENABLED" {
		t.Fatalf("after Enable the package is %s/%s, want ACTIVE/ACCOUNT_ENABLED", status, reason)
	}
	if auditCount(t, f, "guest_account.enabled", acct) != 1 {
		t.Fatal("Enable was not audited as guest_account.enabled")
	}
	st, body = f.do(t, "PATCH", "/guest-accounts/"+acct, map[string]any{"notes": "front desk note"})
	if st != 200 || body["packages_suspended"] != nil || body["packages_resumed"] != nil {
		t.Fatalf("a notes edit = %d %v", st, body)
	}
}

func TestIntegration_ClientAccountEnable_DoesNotLiftAnotherSuspensionOrAnEndedWindow(t *testing.T) {
	f := newAPI(t)
	_, client := newFakeSCD(t, f)
	f.app.scd = client
	ctx := context.Background()

	// Re-enabling must not lift a suspension for being over the online-time budget.
	acct, ent, _ := seedAccountWithSessions(t, f, "da-budget", 0)
	if _, err := f.pool.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'SUSPENDED',now(),'AGGREGATE_OVER_BUDGET')`, ent); err != nil {
		t.Fatal(err)
	}
	if st, b := f.do(t, "PATCH", "/guest-accounts/"+acct, map[string]any{"enabled": false}); st != 200 ||
		b["packages_suspended"] != float64(0) {
		t.Fatalf("disable = %d %v", st, b)
	}
	if st, b := f.do(t, "PATCH", "/guest-accounts/"+acct, map[string]any{"enabled": true}); st != 200 ||
		b["packages_resumed"] != float64(0) {
		t.Fatalf("enable = %d %v", st, b)
	}
	if s, r, _ := entHead(t, f, ent); s != "SUSPENDED" || r != "AGGREGATE_OVER_BUDGET" {
		t.Fatalf("Enable lifted a suspension it did not make: %s/%s", s, r)
	}

	// A window that ended while disabled stays suspended for the expiry sweep to end.
	acct2, ent2, _ := seedAccountWithSessions(t, f, "da-window", 0)
	if st, _ := f.do(t, "PATCH", "/guest-accounts/"+acct2, map[string]any{"enabled": false}); st != 200 {
		t.Fatal("disable failed")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE iam_v2.entitlements DISABLE TRIGGER USER`); err != nil {
		t.Skipf("cannot age the window in this database: %v", err)
	}
	_, err := f.pool.Exec(ctx, `UPDATE iam_v2.entitlements SET window_ends_at = now() - interval '1 minute' WHERE id=$1`, ent2)
	_, _ = f.pool.Exec(ctx, `ALTER TABLE iam_v2.entitlements ENABLE TRIGGER USER`)
	if err != nil {
		t.Fatal(err)
	}
	if st, b := f.do(t, "PATCH", "/guest-accounts/"+acct2, map[string]any{"enabled": true}); st != 200 ||
		b["packages_resumed"] != float64(0) {
		t.Fatalf("enable = %d %v", st, b)
	}
	if s, _, _ := entHead(t, f, ent2); s != "SUSPENDED" {
		t.Fatalf("a package past its window was resumed: %s", s)
	}
}

func TestIntegration_ClientAccountDisable_RefusesFirstWhenDevicesCannotBeTakenOff(t *testing.T) {
	f := newAPI(t)
	f.app.scd = nil // no enforcement channel
	acct, ent, sessions := seedAccountWithSessions(t, f, "da-noscd", 0, "10.91.1.11")
	st, body := f.do(t, "PATCH", "/guest-accounts/"+acct, map[string]any{"enabled": false})
	if st != http.StatusServiceUnavailable || body["error"] != "enforcement_unavailable" {
		t.Fatalf("disable without scd = %d %v", st, body)
	}
	var enabled bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT enabled FROM iam_v2.guest_access_accounts WHERE id=$1`, acct).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("a refused Disable changed the account (enabled=%v err=%v)", enabled, err)
	}
	if s, _, _ := entHead(t, f, ent); s != "ACTIVE" {
		t.Fatalf("a refused Disable suspended the package: %s", s)
	}
	for ip, s := range sessionStates(t, f, sessions) {
		if s[0] != "active" {
			t.Fatalf("session %s became %q on a refused Disable", ip, s[0])
		}
	}
}

func TestIntegration_RemovedClientAccount_CannotBeEnabledRenamedOrEdited(t *testing.T) {
	f := newAPI(t)
	_, client := newFakeSCD(t, f)
	f.app.scd = client
	acct, _, _ := seedAccountWithSessions(t, f, "da-removed", 0)
	if st, b := f.do(t, "DELETE", "/guest-accounts/"+acct, map[string]any{"password": f.password}); st != 200 ||
		b["status"] != "removed" {
		t.Fatalf("remove = %d %v", st, b)
	}
	for _, patch := range []map[string]any{
		{"enabled": true}, {"username": "da-removed"}, {"notes": "bring it back"}, {"valid_until": nil},
	} {
		if st, b := f.do(t, "PATCH", "/guest-accounts/"+acct, patch); st != http.StatusConflict || b["error"] != "removed" {
			t.Fatalf("PATCH %v on a removed account = %d %v, want 409 removed", patch, st, b)
		}
	}
	if st, b := f.do(t, "POST", "/guest-accounts/"+acct+"/set-password", map[string]any{"password": "x1"}); st != http.StatusConflict {
		t.Fatalf("set-password on a removed account = %d %v", st, b)
	}
	// The database refuses it for any writer, not only the API.
	_, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.guest_access_accounts SET enabled = true WHERE id=$1`, acct)
	if err == nil {
		t.Fatal("the database let a removed account be enabled again")
	}
	_, err = f.pool.Exec(context.Background(), `UPDATE iam_v2.guest_access_accounts SET username = 'da-removed' WHERE id=$1`, acct)
	if err == nil {
		t.Fatal("the database let a removed account be renamed back")
	}
	// The guard is narrow: only enabled and username are final, so scd's sweeps of other columns still work.
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.guest_access_accounts SET grace_until = NULL WHERE id=$1`, acct); err != nil {
		t.Fatalf("the guard refused a column it does not govern: %v", err)
	}
}

func TestIntegration_ClientAccountEdit_ExplicitNullClearsAndTheWindowMustBeOrdered(t *testing.T) {
	f := newAPI(t)
	from := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	until := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	st, b := f.do(t, "POST", "/guest-accounts", map[string]any{
		"username": "da-clear", "password": "pw-1", "display_name": "Desk name", "notes": "a note",
		"valid_from": from, "valid_until": until,
	})
	if st != 201 && st != 200 {
		t.Fatalf("create = %d %v", st, b)
	}
	id, _ := b["account"].(map[string]any)["id"].(string)

	st, b = f.do(t, "PATCH", "/guest-accounts/"+id, map[string]any{"valid_until": nil, "display_name": nil})
	if st != 200 {
		t.Fatalf("clear = %d %v", st, b)
	}
	if _, has := b["valid_until"]; has {
		t.Fatalf("valid_until was not cleared: %v", b)
	}
	if _, has := b["display_name"]; has {
		t.Fatalf("display_name was not cleared: %v", b)
	}
	if b["notes"] != "a note" || b["valid_from"] == nil {
		t.Fatalf("an absent field was changed: %v", b)
	}
	if st, b := f.do(t, "PATCH", "/guest-accounts/"+id, map[string]any{"valid_until": from,
		"valid_from": until}); st != 400 {
		t.Fatalf("an inverted window = %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/guest-accounts", map[string]any{"username": "da-inverted", "password": "p",
		"valid_from": until, "valid_until": from}); st != 400 {
		t.Fatalf("create with an inverted window = %d %v", st, b)
	}
	if st, _ := f.do(t, "PATCH", "/guest-accounts/"+id, map[string]any{"enabled": nil}); st != 400 {
		t.Fatal("enabled: null was accepted")
	}
}

func TestIntegration_ClientAccountPortalSwitch_IsASignInMethodDecision(t *testing.T) {
	desk := newAPI(t, "front_office_operator")
	if st, b := desk.do(t, "POST", "/guest-accounts/portal", map[string]any{"enabled": true}); st != http.StatusForbidden {
		t.Fatalf("the desk flipped a sign-in method: %d %v", st, b)
	}
	kiosk := newAPI(t, "voucher_operator")
	if st, _ := kiosk.do(t, "POST", "/guest-accounts/portal", map[string]any{"enabled": true}); st != http.StatusForbidden {
		t.Fatal("the voucher station flipped a sign-in method")
	}
	admin := newAPI(t)
	if st, b := admin.do(t, "POST", "/guest-accounts/portal", map[string]any{"enabled": true}); st != 200 || b["enabled"] != true {
		t.Fatalf("site_admin portal switch = %d %v", st, b)
	}
	if auditCount(t, admin, "auth_methods.updated", admin.tenant) != 1 {
		t.Fatal("the portal switch did not write the auth_methods.updated audit")
	}
	if st, b := admin.do(t, "GET", "/guest-accounts/portal", nil); st != 200 || b["enabled"] != true || b["can_change"] != true {
		t.Fatalf("portal read = %d %v", st, b)
	}
}

func TestIntegration_MarkVerified_NeedsTheVouchingPowerThePasswordAndAReason(t *testing.T) {
	ctx := context.Background()
	seedSelfService := func(f *apiFixture, name string) string {
		var id string
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts
			(tenant_id, site_id, username, password_hash, self_registered, verification_deadline)
			VALUES ($1,$2,$3,'$argon2id$fixture$x',true, now() + interval '1 day') RETURNING id::text`,
			f.tenant, f.site, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	kiosk := newAPI(t, "voucher_operator")
	id := seedSelfService(kiosk, "kiosk@partner.example")
	if st, b := kiosk.do(t, "POST", "/guest-accounts/"+id+"/verify-email", verifyBody(kiosk)); st != http.StatusForbidden {
		t.Fatalf("the voucher station vouched for an address: %d %v", st, b)
	}
	desk := newAPI(t, "front_office_operator")
	id = seedSelfService(desk, "desk@partner.example")
	if st, _ := desk.do(t, "POST", "/guest-accounts/"+id+"/verify-email",
		map[string]any{"password": desk.password, "reason": "ok"}); st != http.StatusBadRequest {
		t.Fatal("a three-character reason was accepted")
	}
	if st, _ := desk.do(t, "POST", "/guest-accounts/"+id+"/verify-email",
		map[string]any{"password": "wrong", "reason": "shown at the desk"}); st != http.StatusUnauthorized {
		t.Fatal("a wrong password was accepted")
	}
	if st, b := desk.do(t, "POST", "/guest-accounts/"+id+"/verify-email", verifyBody(desk)); st != 200 || b["status"] != "verified" {
		t.Fatalf("mark verified = %d %v", st, b)
	}
}

func TestIntegration_RemoveAccount_NeedsThePassword(t *testing.T) {
	f := newAPI(t)
	st, b := f.do(t, "POST", "/guest-accounts", map[string]any{"username": "da-remove-pw", "password": "pw"})
	if st != 201 && st != 200 {
		t.Fatalf("create = %d %v", st, b)
	}
	id, _ := b["account"].(map[string]any)["id"].(string)
	if st, _ := f.do(t, "DELETE", "/guest-accounts/"+id, nil); st != http.StatusUnauthorized {
		t.Fatalf("remove without the password = %d", st)
	}
	if st, _ := f.do(t, "DELETE", "/guest-accounts/"+id, map[string]any{"password": "wrong"}); st != http.StatusUnauthorized {
		t.Fatal("remove with a wrong password was accepted")
	}
	if st, b := f.do(t, "DELETE", "/guest-accounts/"+id, map[string]any{"password": f.password}); st != 200 || b["status"] != "deleted" {
		t.Fatalf("remove = %d %v", st, b)
	}
}

func TestIntegration_ClientAccountsFollowAccess_IsGrantedToEdgedAndNothingWider(t *testing.T) {
	f := newAPI(t)
	var edged, public, transition bool
	err := f.pool.QueryRow(context.Background(), `SELECT
	    has_function_privilege('svc_edged','iam_v2.account_entitlements_follow_access(uuid,uuid,uuid)','EXECUTE'),
	    has_function_privilege('public','iam_v2.account_entitlements_follow_access(uuid,uuid,uuid)','EXECUTE'),
	    has_function_privilege('svc_edged','iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)','EXECUTE')`).
		Scan(&edged, &public, &transition)
	if err != nil {
		t.Skipf("Gate-P roles are not provisioned in this database: %v", err)
	}
	if !edged || public || transition {
		t.Fatalf("edged=%v public=%v edged-on-transition-writer=%v; want true/false/false", edged, public, transition)
	}
}

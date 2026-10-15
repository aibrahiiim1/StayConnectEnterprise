//go:build integration

package main

import (
	"context"
	"testing"
	"time"
)

// MANAGING A CLIENT ACCOUNT (guest_accounts_manage.go) against a real database: an unused account is deleted;
// one with history is removed -- disconnected, disabled, its address free for a new registration -- while its
// entitlement and sessions stay; and the verification adjustments touch only verification state.

func idsOf(body map[string]any) map[string]bool {
	out := map[string]bool{}
	data, _ := body["data"].([]any)
	for _, x := range data {
		out[x.(map[string]any)["id"].(string)] = true
	}
	return out
}

func TestIntegration_AccountManage_DeleteUnusedRemoveUsedAndReregister(t *testing.T) {
	f := newAPI(t)
	_, client := newFakeSCD(t, f)
	f.app.scd = client
	ctx := context.Background()

	// 1. Never used: the row goes, with its queued verification email.
	var unused string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts
		(tenant_id, site_id, username, password_hash, self_registered, verification_deadline)
		VALUES ($1,$2,'unused@example.com','$argon2id$fixture$x', true, now() + interval '1 day') RETURNING id::text`,
		f.tenant, f.site).Scan(&unused); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.account_email_tokens (tenant_id, site_id, account_id, purpose, expires_at)
		VALUES ($1,$2,$3,'VERIFY', now() + interval '1 day')`, f.tenant, f.site, unused); err != nil {
		t.Fatal(err)
	}
	if st, b := f.do(t, "GET", "/guest-accounts/"+unused+"/manage", nil); st != 200 || b["removal"] != "delete" {
		t.Fatalf("manage (unused) = %d %v", st, b)
	}
	if st, b := f.do(t, "DELETE", "/guest-accounts/"+unused, nil); st != 200 || b["status"] != "deleted" {
		t.Fatalf("delete unused = %d %v", st, b)
	}
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_access_accounts WHERE id=$1`, unused).Scan(&n)
	if n != 0 {
		t.Fatal("an unused account is deleted")
	}

	// 2. With history: removed, not deleted.
	acc, ent, sessions := seedAccountWithSessions(t, f, "used@example.com", 5000, "10.91.0.11")
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.guest_access_accounts SET self_registered = true,
		verification_deadline = now() + interval '1 day', login_count = 2 WHERE id=$1`, acc); err != nil {
		t.Fatal(err)
	}
	st, b := f.do(t, "GET", "/guest-accounts/"+acc+"/manage", nil)
	ents, _ := b["entitlements"].([]any)
	if st != 200 || b["removal"] != "remove" || len(ents) != 1 {
		t.Fatalf("manage (used) = %d %v", st, b)
	}
	devs, _ := ents[0].(map[string]any)["devices"].([]any)
	if len(devs) != 1 || devs[0].(map[string]any)["online"] != true {
		t.Fatalf("the package's device = %v", ents[0])
	}
	before := readEntState(t, f, ent)
	st, b = f.do(t, "DELETE", "/guest-accounts/"+acc, nil)
	if st != 200 || b["status"] != "removed" || b["history_kept"] != true || b["disconnected_sessions"] != float64(1) {
		t.Fatalf("remove used = %d %v", st, b)
	}
	var username string
	var enabled bool
	_ = f.pool.QueryRow(ctx, `SELECT username, enabled FROM iam_v2.guest_access_accounts WHERE id=$1`, acc).Scan(&username, &enabled)
	if enabled || username != removedUsername("used@example.com", acc) || !isRemovedUsername(username) {
		t.Fatalf("removed account = %q enabled=%v", username, enabled)
	}
	after := readEntState(t, f, ent)
	if after.status != before.status || after.dataUsed != before.dataUsed || after.window != before.window {
		t.Fatalf("removal changed the package: %+v -> %+v", before, after)
	}
	if st := sessionStates(t, f, sessions)["10.91.0.11"]; st[0] != "ended" {
		t.Fatalf("the device must be disconnected: %v", st)
	}
	if auditCount(t, f, "guest_account.removed", acc) != 1 {
		t.Fatal("the removal is audited")
	}
	// Removing again is a no-op.
	if st, b := f.do(t, "DELETE", "/guest-accounts/"+acc, nil); st != 200 || b["status"] != "removed" {
		t.Fatalf("remove again = %d %v", st, b)
	}
	// Listed only under its own filter.
	if _, all := f.getAccounts(t, "", ""); idsOf(all)[acc] {
		t.Fatal("a removed account is not in the default list")
	}
	if _, dis := f.getAccounts(t, "?status=disabled", ""); idsOf(dis)[acc] {
		t.Fatal("a removed account is not counted as disabled")
	}
	if st, rem := f.getAccounts(t, "?status=removed", ""); st != 200 || !idsOf(rem)[acc] {
		t.Fatalf("status=removed lists it: %d %v", st, rem)
	}
	// The address is free again.
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts
		(tenant_id, site_id, username, password_hash, self_registered) VALUES ($1,$2,'used@example.com','$argon2id$fixture$x', true)`,
		f.tenant, f.site); err != nil {
		t.Fatalf("the address must be registrable again: %v", err)
	}
	// Staff cannot create a username that looks removed.
	if st, _ := f.do(t, "POST", "/guest-accounts", map[string]any{"username": "x.removed-1.invalid", "password": "abcdefgh"}); st != 400 {
		t.Fatalf("a removal-marker username was accepted: %d", st)
	}
}

func TestIntegration_AccountManage_VerificationAdjustments(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	var late, staff string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts
		(tenant_id, site_id, username, password_hash, self_registered, verification_deadline, grace_extensions_used,
		 grace_until)
		VALUES ($1,$2,'late2@example.com','$argon2id$fixture$x', true, now() - interval '2 hours', 1,
		        now() - interval '1 hour') RETURNING id::text`, f.tenant, f.site).Scan(&late); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash)
		VALUES ($1,$2,'desk-1','$argon2id$fixture$x') RETURNING id::text`, f.tenant, f.site).Scan(&staff); err != nil {
		t.Fatal(err)
	}

	if st, b := f.do(t, "POST", "/guest-accounts/"+late+"/restart-verification", nil); st != 200 || b["status"] != "restarted" {
		t.Fatalf("restart = %d %v", st, b)
	}
	var deadline time.Time
	var used int
	var grace *time.Time
	_ = f.pool.QueryRow(ctx, `SELECT verification_deadline, grace_extensions_used, grace_until
		FROM iam_v2.guest_access_accounts WHERE id=$1`, late).Scan(&deadline, &used, &grace)
	if time.Until(deadline) < 23*time.Hour || used != 0 || grace != nil {
		t.Fatalf("a new period: deadline in %v, grace used %d, grace %v", time.Until(deadline), used, grace)
	}
	if st, b := f.do(t, "GET", "/guest-accounts/"+late, nil); st != 200 || b["suspended"] == true {
		t.Fatalf("no longer suspended: %d %v", st, b)
	}

	if st, b := f.do(t, "POST", "/guest-accounts/"+late+"/verify-email", nil); st != 200 || b["status"] != "verified" {
		t.Fatalf("mark verified = %d %v", st, b)
	}
	var verified bool
	_ = f.pool.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM iam_v2.guest_access_accounts WHERE id=$1`, late).Scan(&verified)
	if !verified || auditCount(t, f, "guest_account.email_marked_verified", late) != 1 {
		t.Fatal("marked verified and audited")
	}
	if st, b := f.do(t, "POST", "/guest-accounts/"+late+"/verify-email", nil); st != 200 || b["status"] != "already_verified" {
		t.Fatalf("again = %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/guest-accounts/"+late+"/restart-verification", nil); st != 409 || b["error"] != "already_verified" {
		t.Fatalf("restart a verified account = %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/guest-accounts/"+staff+"/verify-email", nil); st != 409 || b["error"] != "not_self_service" {
		t.Fatalf("staff account = %d %v", st, b)
	}
}

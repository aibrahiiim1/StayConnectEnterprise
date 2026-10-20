//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Migration 0147: an anonymised record keeps nothing that names the person, opens a door for them, or points a join
// away from them -- and the same is true when retention anonymises it, and for what staff typed or are called.

const removedNote = "Removed after the staff-notes period"

func TestIntegration_Depth_AnAnonymisedStayAndItsPostStayAndStaffNotesKeepNothingIdentifying(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	res := "R-DEEP-" + sfx
	stay := seedGuestDataStay(t, f, res, "CHECKED_OUT")
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET room_type='DELUXE', arrival='2026-09-12', departure='2026-09-17',
			effective_checkout_at='2026-09-17 10:42:31+00' WHERE id=$1::uuid`, stay)
		return err
	})
	var iface string
	if err := f.pool.QueryRow(ctx, `SELECT pms_interface_id::text FROM iam_v2.stays WHERE id=$1`, stay).Scan(&iface); err != nil {
		t.Fatal(err)
	}
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_posting_blocks(tenant_id,site_id,pms_interface_id,stay_id,reason,source,note,created_by,cleared_at,cleared_by_source,cleared_by,cleared_reason)
			VALUES ($1,$2,$3::uuid,$4::uuid,'ADMIN_BLOCK','OPERATOR','Ada disputes the charge, called 03/10', gen_random_uuid(), now(), 'OPERATOR', gen_random_uuid(), 'Ada agreed')`, f.tenant, f.site, iface, stay); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,pin_generation,pin_set_at,pin_revealed_at,
			valid_until,status,revoked_at,revoke_reason,issued_via,issued_by_operator,issued_at)
			SELECT $1,$2,s.id,s.lifecycle_version,'$argon2id$v=19$m=65536,t=1,p=4$c29tZXNhbHQ$c29tZWhhc2hzb21laGFzaHNvbWVoYXNoc29tZWhhc2g',1,now(),now(),
			       now() + interval '30 days','REVOKED',now(),'Ada lost her phone','OPERATOR_RESET',gen_random_uuid(),now()
			  FROM iam_v2.stays s WHERE s.id=$3::uuid`, f.tenant, f.site, stay)
		return err
	})
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	for name, q := range map[string]string{
		"room type":              `SELECT (room_type IS NULL)::text FROM iam_v2.stays WHERE id=$1`,
		"arrival to the month":   `SELECT (extract(day FROM arrival) = 1)::text FROM iam_v2.stays WHERE id=$1`,
		"departure to the month": `SELECT (extract(day FROM departure) = 1)::text FROM iam_v2.stays WHERE id=$1`,
		"checkout to the day":    `SELECT (effective_checkout_at = date_trunc('day', effective_checkout_at))::text FROM iam_v2.stays WHERE id=$1`,
		"block note":             `SELECT (note = '` + removedNote + `' AND cleared_reason = '` + removedNote + `')::text FROM iam_v2.stay_posting_blocks WHERE stay_id=$1`,
		"post-stay reason":       `SELECT (revoke_reason = '` + removedNote + `')::text FROM iam_v2.post_stay_profiles WHERE origin_stay_id=$1`,
		"post-stay secret":       `SELECT (pin_hash LIKE '%$AAAAAAAAAAAAAAAAAAAAAA$%')::text FROM iam_v2.post_stay_profiles WHERE origin_stay_id=$1`,
	} {
		if got := gdStr(t, f, q, stay); got != "true" {
			t.Errorf("after the erasure, the stay's %s still identifies", name)
		}
	}
}

func TestIntegration_Depth_AnAnonymisedAccountKeepsNoReservationNoSecretNoHistoryAndNoNotes(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-ACC-"+sfx, "CHECKED_OUT")
	mac := "02:aa:00:98:00:01"
	acct := selfAccount(t, f, "depth-"+sfx+"@example.com", true, stay, mac)
	ent := newAccess(t, f, "depth"+sfx, "guest_account_id", acct, false)
	dev := newDevice(t, f, mac)
	onDevice(t, f, ent, dev, "10.98.0.1", "ACCOUNT", false)
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		for _, q := range []string{
			`UPDATE iam_v2.guest_access_accounts SET last_login_at=now() - interval '40 days', login_count=9, email_verified_at=now() - interval '300 days',
			     valid_from=now() - interval '300 days', valid_until=now() - interval '200 days' WHERE id='` + acct + `'::uuid`,
			`INSERT INTO iam_v2.entitlement_adjustments(tenant_id,site_id,entitlement_id,field,reason) VALUES ('` + f.tenant + `','` + f.site + `','` + ent + `'::uuid,'window','goodwill extension for Ms Lovelace')`,
			`INSERT INTO iam_v2.device_slot_actions(tenant_id,site_id,entitlement_id,device_id,actor,operator_label,action,outcome,reason,detail)
			     VALUES ('` + f.tenant + `','` + f.site + `','` + ent + `'::uuid,'` + dev + `'::uuid,'STAFF','desk@hotel.test','RELEASE','OK','Ada asked at the desk','removed her old iPhone')`,
			`INSERT INTO iam_v2.guest_device_actions(tenant_id,site_id,entitlement_id,device_id,action,outcome,detail) VALUES ('` + f.tenant + `','` + f.site + `','` + ent + `'::uuid,'` + dev + `'::uuid,'RELEASE','OK','removed my iPhone')`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	for name, q := range map[string]string{
		"reservation link":     `SELECT (stay_id IS NULL)::text FROM iam_v2.guest_access_accounts WHERE id=$1`,
		"password hash":        `SELECT (password_hash LIKE '%$AAAAAAAAAAAAAAAAAAAAAA$%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`,
		"sign-in history":      `SELECT (last_login_at IS NULL AND login_count = 0 AND email_verified_at IS NULL)::text FROM iam_v2.guest_access_accounts WHERE id=$1`,
		"validity window":      `SELECT (valid_from IS NULL AND valid_until IS NULL)::text FROM iam_v2.guest_access_accounts WHERE id=$1`,
		"creation date":        `SELECT (extract(day FROM created_at) = 1)::text FROM iam_v2.guest_access_accounts WHERE id=$1`,
		"adjustment note":      `SELECT (bool_and(reason = '` + removedNote + `'))::text FROM iam_v2.entitlement_adjustments WHERE entitlement_id='` + ent + `'::uuid AND $1::uuid IS NOT NULL`,
		"slot action text":     `SELECT (bool_and(reason = '` + removedNote + `' AND detail = '` + removedNote + `'))::text FROM iam_v2.device_slot_actions WHERE entitlement_id='` + ent + `'::uuid AND $1::uuid IS NOT NULL`,
		"guest action text":    `SELECT (bool_and(detail = '` + removedNote + `'))::text FROM iam_v2.guest_device_actions WHERE entitlement_id='` + ent + `'::uuid AND $1::uuid IS NOT NULL`,
		"device first seen":    `SELECT (first_seen = date_trunc('day', first_seen) AND last_seen = date_trunc('day', last_seen))::text FROM iam_v2.devices WHERE id='` + dev + `'::uuid AND $1::uuid IS NOT NULL`,
		"account's device MAC": `SELECT (created_by_device_mac IS NULL)::text FROM iam_v2.guest_access_accounts WHERE id=$1`,
	} {
		if got := gdStr(t, f, q, acct); got != "true" {
			t.Errorf("after the erasure, the account's %s still identifies (%s)", name, got)
		}
	}
	// What the staff member did is kept: who and when. The staff member's own name is another matter (below).
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.device_slot_actions WHERE entitlement_id=$1 AND action='RELEASE'`, ent); got != "1" {
		t.Errorf("the record that a release happened must stay: %s", got)
	}
}

func TestIntegration_Depth_AnIdentityRememberedDeviceSecretIsReplacedNotJustRevoked(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	dev := newDevice(t, f, "02:aa:00:98:00:02")
	var principal string
	if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Dora') RETURNING id)
		INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
		SELECT $1, gp.id, 'EMAIL', '', $2, now() - interval '400 days', '{}'::jsonb FROM gp RETURNING guest_principal_id::text`,
		f.tenant, "dora-"+sfx+"@example.com").Scan(&principal); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.principal_device_credentials(tenant_id,site_id,guest_principal_id,device_id,method,secret_hmac,key_id,expires_at,last_used_at)
		VALUES ($1,$2,$3::uuid,$4::uuid,'OTP',decode('00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff','hex'),'k1', now() + interval '30 days', now() - interval '3 days')`,
		f.tenant, f.site, principal, dev); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "principal", principal, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT (secret_hmac <> decode('00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff','hex') AND revoked_at IS NOT NULL AND last_used_at IS NULL)::text
		FROM iam_v2.principal_device_credentials WHERE guest_principal_id=$1`, principal); got != "true" {
		t.Error("a remembered-device secret was revoked but kept")
	}
	if got := gdStr(t, f, `SELECT (extract(day FROM verified_at) = 1)::text FROM iam_v2.guest_principal_identities WHERE guest_principal_id=$1`, principal); got != "true" {
		t.Error("the identity's verification time was kept to the second")
	}
}

// Retention is the same erasure by age: an entry about a guest follows the guest's period, and a departed staff
// member's name leaves the records they acted in.
func TestIntegration_Depth_AuditEntriesAboutAGuestFollowTheGuestsPeriodNotTheAuditLogs(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	for i, d := range []int{500, 100} {
		for _, tt := range []string{"guest_account", "operator", "guest_data"} {
			if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
				VALUES (now() - make_interval(days => $2::int),$1,'operator','op-1','x.'||$3::text,$3,$4,jsonb_build_object('username','guest-'||$5::text||'@example.com'))`,
				f.tenant, d, tt, fmt.Sprintf("tgt-%d-%s", i, tt), fmt.Sprint(i)); err != nil {
				t.Fatalf("seed audit: %v", err)
			}
		}
	}
	if _, err := f.pool.Exec(ctx, `SELECT count(*) FROM public.run_data_retention_public($1::uuid,$2::uuid,1000)`, f.tenant, f.site); err != nil {
		t.Fatalf("retention: %v", err)
	}
	cases := map[string]string{
		"x.guest_account|500": "true", "x.guest_data|500": "true", // older than the guest periods (365): anonymised
		"x.guest_account|100": "false", "x.guest_data|100": "false", // within them: kept
		"x.operator|500": "false", // not about a guest: the audit log's own 730 days apply
	}
	for k, want := range cases {
		parts := strings.Split(k, "|")
		var days = parts[1]
		got := gdStr(t, f, `SELECT (payload = '{"anonymised": true}'::jsonb)::text FROM public.audit_log
			WHERE tenant_id=$1 AND action=$2 AND ts < now() - make_interval(days => $3::int - 2) AND ts > now() - make_interval(days => $3::int + 2)`, f.tenant, parts[0], days)
		if got != want {
			t.Errorf("%s: anonymised=%s, want %s", k, got, want)
		}
	}
}

func TestIntegration_Depth_ADepartedStaffMembersEmailLeavesTheRecordsTheyActedIn(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "leaver-" + sfx + "@hotel.test"
	var op string
	if err := f.pool.QueryRow(ctx, `INSERT INTO public.operators(tenant_id,email,display_name,password_hash,status) VALUES ($1,$2,'Leaver','$argon2id$fixture','active') RETURNING id::text`, f.tenant, email).Scan(&op); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_data_erasures(erasure_ref,tenant_id,site_id,subject_kind,erased_by,erase_reason,counts)
		VALUES ('ER-'||upper(substr(md5($3),1,12)),$1,$2,'account',$4,'guest asked','{}'::jsonb)`, f.tenant, f.site, sfx, email); err != nil {
		t.Fatalf("seed erasure: %v", err)
	}
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.device_slot_actions(tenant_id,site_id,actor,operator_label,action,outcome,reason) VALUES ($1,$2,'STAFF',$3,'RELEASE','OK','asked')`, f.tenant, f.site, email)
		return err
	})
	if _, err := f.pool.Exec(ctx, `UPDATE public.operators SET status='disabled' WHERE id=$1::uuid`, op); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE public.operators SET disabled_at = now() - interval '800 days' WHERE id=$1::uuid`, op); err != nil {
		t.Fatal(err)
	}
	var out map[string]int
	if err := f.pool.QueryRow(ctx, `SELECT public.run_staff_retention($1::uuid,$2::uuid)`, f.tenant, f.site).Scan(&out); err != nil {
		t.Fatal(err)
	}
	if out["staff_labels_scrubbed"] < 2 {
		t.Errorf("the staff retention run scrubbed %d records", out["staff_labels_scrubbed"])
	}
	for name, q := range map[string]string{
		"erasure register": `SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE erased_by ILIKE '%' || $1 || '%'`,
		"slot action":      `SELECT count(*)::text FROM iam_v2.device_slot_actions WHERE operator_label ILIKE '%' || $1 || '%'`,
	} {
		if got := gdStr(t, f, q, "leaver-"+sfx); got != "0" {
			t.Errorf("the departed member's email is still in the %s (%s rows)", name, got)
		}
	}
	// And the tables are as append-only as before for everyone else.
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.guest_data_erasures SET erased_by='someone' WHERE tenant_id=$1`, f.tenant); err == nil {
		t.Error("the register can be edited outside the scrub")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM iam_v2.device_slot_actions WHERE tenant_id=$1`, f.tenant); err == nil {
		t.Error("slot actions can be deleted")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.device_slot_actions SET action='REPLACE' WHERE tenant_id=$1`, f.tenant); err == nil {
		t.Error("a slot action's content can be edited")
	}
}

// The staff-notes period reaches every registered column, and only older ones.
func TestIntegration_Depth_TheStaffNotesPeriodReachesEveryRegisteredColumn(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	ent := newAccess(t, f, "notes"+fmt.Sprint(time.Now().UnixNano()), "stay_id", seedGuestDataStay(t, f, "R-NOTE-"+fmt.Sprint(time.Now().UnixNano()), "CHECKED_OUT"), false)
	var oldID, newID string
	for _, c := range []struct {
		dst *string
		ago string
	}{{&oldID, "800 days"}, {&newID, "10 days"}} {
		asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlement_adjustments(tenant_id,site_id,entitlement_id,field,reason,created_at)
				VALUES ($1,$2,$3::uuid,'window','goodwill extension for Ms Lovelace', now() - $4::interval) RETURNING id::text`, f.tenant, f.site, ent, c.ago).Scan(c.dst)
		})
	}
	if _, err := f.pool.Exec(ctx, `SELECT count(*) FROM iam_v2.run_staff_note_retention($1::uuid,$2::uuid,1000)`, f.tenant, f.site); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := gdStr(t, f, `SELECT reason FROM iam_v2.entitlement_adjustments WHERE id=$1`, oldID); got != removedNote {
		t.Errorf("an old note was kept: %q", got)
	}
	if got := gdStr(t, f, `SELECT reason FROM iam_v2.entitlement_adjustments WHERE id=$1`, newID); got == removedNote {
		t.Errorf("a recent note was removed")
	}
}

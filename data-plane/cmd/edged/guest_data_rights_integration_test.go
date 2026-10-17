//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// GDPR plan C3 and C4 (migration 0128): staff can find one guest, hand over everything held about them, and
// erase them -- only the roles D-C4 named, only with the password and a reason, never while the guest is live,
// and without touching anyone else.

func TestIntegration_GuestDataRights_FindExportErase(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "ada-" + sfx + "@example.com"

	acct, device, session := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.88.0.10")
	// The same address as a portal identity: a separate record the guest is also known by.
	var principal string
	if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Ada') RETURNING id)
		INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
		SELECT $1, gp.id, 'EMAIL', '', $2, now() - interval '400 days', '{}'::jsonb FROM gp RETURNING guest_principal_id::text`,
		f.tenant, email).Scan(&principal); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	var otp, attempt string
	if err := f.pool.QueryRow(ctx, `INSERT INTO public.auth_otps(tenant_id,channel,destination,code_hash,issued_at,expires_at,ip)
		VALUES ($1,'email',$2,'h',now() - interval '400 days',now() - interval '400 days' + interval '10 minutes','10.88.0.5')
		RETURNING id::text`, f.tenant, email).Scan(&otp); err != nil {
		t.Fatalf("seed otp: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id,site_id,occurred_at,result,verifier_kind,submitted_room,device_mac,device_ip)
		VALUES ($1,$2,now() - interval '400 days','CREDENTIAL_MISMATCH','UNKNOWN','101','02:aa:bb:cc:dd:01','10.88.0.10') RETURNING id::text`,
		f.tenant, f.site).Scan(&attempt); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
		VALUES (now() - interval '1 day',$1,'operator','op-1','guest_account.password_set','guest_account',$2,jsonb_build_object('username',$3::text)),
		       (now() - interval '1 day',$1,'operator','op-1','guest_account.created','guest_account','other-'||$4,jsonb_build_object('username','someone-else@example.com'))`,
		f.tenant, acct, email, sfx); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	// --- Who may: site_admin and the IT manager (D-C4); not the desk.
	q := "/guest-data-rights/search?q=" + "ada-" + sfx
	st, body := f.do(t, "GET", q, nil)
	matches, _ := body["matches"].([]any)
	if st != 200 || len(matches) != 2 {
		t.Fatalf("search by address = %d %v, want the account and the identity", st, body)
	}
	if st, body := f.do(t, "GET", "/guest-data-rights/search?q=02:AA:BB:CC:DD:01", nil); st != 200 || len(body["matches"].([]any)) != 1 {
		t.Fatalf("search by MAC = %d %v, want the account that used the device", st, body)
	}
	if st, _ := newAPIIn(t, f.tenant, "hotel_it_manager").do(t, "GET", q, nil); st != 200 {
		t.Fatalf("the IT manager cannot search: %d", st)
	}
	for _, role := range []string{"front_office_operator", "guest_relations_operator", "site_viewer"} {
		if st, _ := newAPIIn(t, f.tenant, role).do(t, "GET", q, nil); st != 403 {
			t.Fatalf("%s reached guest data rights: %d", role, st)
		}
	}

	// --- Export: the password, a reason, a real record.
	req := func(kind, id, reason, pw string) map[string]any {
		return map[string]any{"kind": kind, "id": id, "reason": reason, "password": pw}
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/export", req("account", acct, "guest asked", "wrong")); st != 401 {
		t.Fatalf("export with a wrong password = %d", st)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/export", req("account", acct, "", f.password)); st != 400 {
		t.Fatalf("export without a reason = %d", st)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/export", req("operator", acct, "guest asked", f.password)); st != 400 {
		t.Fatalf("export of an unknown kind = %d", st)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/export", req("account", "00000000-0000-4000-8000-000000000000", "guest asked", f.password)); st != 404 {
		t.Fatalf("export of a record that does not exist = %d", st)
	}
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", req("account", acct, "guest asked in writing", f.password))
	if st != 200 {
		t.Fatalf("export = %d %s", st, raw)
	}
	var doc struct {
		Format   string `json:"format"`
		Accounts []struct {
			Username string `json:"username"`
			Notes    string `json:"notes"`
		} `json:"client_accounts"`
		Devices  []map[string]any `json:"devices"`
		Sessions []map[string]any `json:"sessions"`
		Access   []map[string]any `json:"internet_access"`
		Codes    []map[string]any `json:"verification_codes"`
		Activity []map[string]any `json:"activity_log"`
		Attempts []map[string]any `json:"sign_in_attempts"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("export is not JSON: %v", err)
	}
	if doc.Format != "stayconnect.guest-data-export/1" || len(doc.Accounts) != 1 || doc.Accounts[0].Username != email ||
		doc.Accounts[0].Notes != "room 101 guest" || len(doc.Devices) != 1 || len(doc.Sessions) != 1 || len(doc.Access) != 1 ||
		len(doc.Codes) != 1 || len(doc.Activity) != 1 || len(doc.Attempts) != 1 {
		t.Fatalf("export is incomplete: %s", raw)
	}
	if strings.Contains(raw, "someone-else@example.com") {
		t.Fatal("the export carries another guest's data")
	}
	audited := func(action, target string) (n int) {
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM public.audit_log WHERE action=$1 AND target_id=$2`, action, target).Scan(&n)
		return n
	}
	if audited("guest_data.exported", acct) != 1 {
		t.Fatal("the export was not audited by record id")
	}

	// --- Erasure is refused while the guest is live: access still running, a device online.
	liveEmail := "live-" + sfx + "@example.com"
	liveAcct, _, _ := seedAccountWithSessions(t, f, liveEmail, 0, "10.88.0.11")
	st, body = f.do(t, "POST", "/guest-data-rights/erase", req("account", liveAcct, "guest asked", f.password))
	if st != 409 || body["error"] != "guest_data_live" {
		t.Fatalf("erasing a live guest = %d %v, want 409 guest_data_live", st, body)
	}
	var liveName string
	_ = f.pool.QueryRow(ctx, `SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, liveAcct).Scan(&liveName)
	if liveName != liveEmail {
		t.Fatalf("a refused erasure changed the account: %q", liveName)
	}

	// --- ...and while the PMS still has the stay open.
	inHouse := seedGuestDataStay(t, f, "R-IN-"+sfx, "IN_HOUSE")
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", req("stay", inHouse, "guest asked", f.password)); st != 409 {
		t.Fatalf("erasing an in-house stay = %d %v", st, body)
	}

	// --- Erase the ended account.
	if st, _ := f.do(t, "POST", "/guest-data-rights/erase", req("account", acct, "guest asked", "wrong")); st != 401 {
		t.Fatalf("erase with a wrong password = %d", st)
	}
	st, body = f.do(t, "POST", "/guest-data-rights/erase", req("account", acct, "written request 2026-10-17", f.password))
	ref, _ := body["erasure_ref"].(string)
	if st != 200 || !strings.HasPrefix(ref, "ER-") || len(ref) != 15 {
		t.Fatalf("erase = %d %v", st, body)
	}
	str := func(q string, args ...any) string {
		t.Helper()
		var s *string
		if err := f.pool.QueryRow(ctx, q, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if s == nil {
			return "<null>"
		}
		return *s
	}
	expect := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %q, want %q", what, got, want)
		}
	}
	expect("account anonymised", str(`SELECT (username LIKE 'anon-%@anonymised.removed-%.invalid' AND NOT enabled AND display_name IS NULL AND notes IS NULL)::text FROM iam_v2.guest_access_accounts WHERE id=$1`, acct), "true")
	expect("device anonymised", str(`SELECT (mac::text LIKE '03:%' AND last_ip IS NULL)::text FROM iam_v2.devices WHERE id=$1`, device), "true")
	expect("session address", str(`SELECT concat_ws(',',host(ip),mac::text) FROM iam_v2.sessions WHERE id=$1`, session), "")
	expect("session totals kept", str(`SELECT bytes_down::text FROM iam_v2.sessions WHERE id=$1`, session), "4096")
	expect("sign-in attempt", str(`SELECT concat_ws(',',submitted_room,device_mac::text,host(device_ip)) || result FROM iam_v2.sign_in_attempts WHERE id=$1`, attempt), "CREDENTIAL_MISMATCH")
	expect("code destination", str(`SELECT destination || coalesce(host(ip),'') FROM public.auth_otps WHERE id=$1`, otp), "anonymised")
	expect("audit entry about the guest", str(`SELECT payload::text || coalesce(target_id,'-') FROM public.audit_log WHERE action='guest_account.password_set' AND tenant_id=$1`, f.tenant), `{"anonymised": true}-`)
	expect("audit entry about someone else", str(`SELECT payload->>'username' FROM public.audit_log WHERE target_id=$1`, "other-"+sfx), "someone-else@example.com")
	expect("export audit no longer points at the record", fmt.Sprint(audited("guest_data.exported", acct)), "0")
	// The identity with the same address is a different record the operator did not choose: untouched.
	expect("unchosen identity", str(`SELECT factor_value_norm FROM iam_v2.guest_principal_identities WHERE guest_principal_id=$1`, principal), email)

	// The erasure is recorded by reference only, and audited the same way.
	if audited("guest_data.erased", ref) != 1 {
		t.Fatal("the erasure was not audited by its reference")
	}
	expect("erasure audit names nobody", str(`SELECT (payload::text NOT LIKE '%'||$2||'%')::text FROM public.audit_log WHERE action='guest_data.erased' AND target_id=$1`, ref, email), "true")
	st, body = f.do(t, "GET", "/guest-data-rights/erasures", nil)
	list, _ := body["erasures"].([]any)
	if st != 200 || len(list) != 1 || list[0].(map[string]any)["erasure_ref"] != ref {
		t.Fatalf("erasures = %d %v", st, body)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM iam_v2.guest_data_erasures WHERE erasure_ref=$1`, ref); err == nil {
		t.Fatal("an erasure record was deleted")
	}

	// The search no longer finds the account; erasing the identity too leaves nothing.
	st, body = f.do(t, "GET", q, nil)
	matches, _ = body["matches"].([]any)
	if st != 200 || len(matches) != 1 || matches[0].(map[string]any)["kind"] != "principal" {
		t.Fatalf("search after erasure = %v", body)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", req("principal", principal, "same request", f.password)); st != 200 {
		t.Fatalf("erase identity = %d %v", st, body)
	}
	if st, body := f.do(t, "GET", q, nil); st != 200 || len(body["matches"].([]any)) != 0 {
		t.Fatalf("search after both erasures = %v", body)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/erase", req("account", acct, "again", f.password)); st != 200 {
		t.Fatalf("erasing an erased account again = %d, want an idempotent 200", st)
	}
}

// A checked-out stay is erased whole: reservation, room, every guest's names, the raw PMS messages.
func TestIntegration_GuestDataRights_ErasesAStay(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-OUT-"+sfx, "CHECKED_OUT")
	st, body := f.do(t, "GET", "/guest-data-rights/search?q=R-OUT-"+sfx, nil)
	if m, _ := body["matches"].([]any); st != 200 || len(m) != 1 {
		t.Fatalf("search by reservation = %d %v", st, body)
	}
	if st, body := f.do(t, "GET", "/guest-data-rights/search?q=lovelace", nil); st != 200 || len(body["matches"].([]any)) != 1 {
		t.Fatalf("search by guest name = %d %v", st, body)
	}
	st, body = f.do(t, "POST", "/guest-data-rights/erase", map[string]any{"kind": "stay", "id": stay, "reason": "guest asked", "password": f.password})
	if st != 200 {
		t.Fatalf("erase stay = %d %v", st, body)
	}
	var reservation, names, payload, status string
	if err := f.pool.QueryRow(context.Background(), `SELECT s.external_reservation_id, concat_ws(',',g.first_name_norm,g.last_name_norm,g.display_name,g.date_of_birth::text),
		e.payload::text, s.status FROM iam_v2.stays s JOIN iam_v2.stay_guests g ON g.stay_id=s.id JOIN iam_v2.stay_events e ON e.stay_id=s.id WHERE s.id=$1`,
		stay).Scan(&reservation, &names, &payload, &status); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reservation, "anon-") || names != "" || payload != `{"anonymised": true}` || status != "CHECKED_OUT" {
		t.Fatalf("stay after erasure: %s | %s | %s | %s", reservation, names, payload, status)
	}
}

// seedGuestDataStay is one stay in the given state with one named guest and one applied PMS message.
func seedGuestDataStay(t *testing.T, f *apiFixture, res, status string) string {
	t.Helper()
	ctx := context.Background()
	var iface, stay string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
		VALUES (gen_random_uuid(),$1,$2,'protel-fias',$3,'ACTIVE') RETURNING id::text`, f.tenant, f.site, "PMS "+res).Scan(&iface); err != nil {
		t.Fatalf("seed interface: %v", err)
	}
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(tenant_id,site_id,pms_interface_id,external_reservation_id,
			  external_stay_identity,normalized_room_number,status,arrival,departure,effective_checkout_at)
			VALUES ($1,$2,$3::uuid,$4,$4,'101',$5,current_date - 3,current_date + 1,
			        CASE WHEN $5 = 'CHECKED_OUT' THEN now() - interval '1 day' END) RETURNING id::text`,
			f.tenant, f.site, iface, res, status).Scan(&stay); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,
			  external_guest_id,first_name_norm,last_name_norm,display_name,is_primary,date_of_birth)
			VALUES ($1,$2,$3::uuid,$4::uuid,'G-'||$5,'ada','lovelace','Ada Lovelace',true,'1815-12-10')`,
			f.tenant, f.site, iface, stay, res); err != nil {
			return err
		}
		var ev string
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events(tenant_id,site_id,pms_interface_id,external_event_identity,
			  event_type,processing_status,payload)
			VALUES ($1,$2,$3::uuid,'EV-'||$4,'GUEST_IN','PENDING',jsonb_build_object('reservation',$4::text,'last_name','Lovelace'))
			RETURNING id::text`, f.tenant, f.site, iface, res).Scan(&ev); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status='APPLIED', stay_id=$2::uuid, processed_at=now() WHERE id=$1::uuid`, ev, stay)
		return err
	}); err != nil {
		t.Fatalf("seed stay %s: %v", res, err)
	}
	return stay
}

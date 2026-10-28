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

	// Who may: site_admin and the IT manager; not the desk.
	q := "ada-" + sfx
	st, body := f.searchGuestData(t, q)
	matches, _ := body["matches"].([]any)
	if st != 200 || len(matches) != 2 {
		t.Fatalf("search by address = %d %v, want the account and the identity", st, body)
	}
	// P9 L11: search text in the URL is no longer read, so it cannot end up in proxy logs.
	if st, _ := f.do(t, "GET", "/guest-data-rights/search?q="+q, nil); st != 400 {
		t.Fatalf("a ?q= search must be refused, got %d", st)
	}
	if st, body := f.searchGuestData(t, "02:AA:BB:CC:DD:01"); st != 200 || len(body["matches"].([]any)) != 1 {
		t.Fatalf("search by MAC = %d %v, want the account that used the device", st, body)
	}
	if st, _ := newAPIIn(t, f.tenant, "hotel_it_manager").searchGuestData(t, q); st != 200 {
		t.Fatalf("the IT manager cannot search: %d", st)
	}
	for _, role := range []string{"front_office_operator", "guest_relations_operator", "site_viewer"} {
		if st, _ := newAPIIn(t, f.tenant, role).searchGuestData(t, q); st != 403 {
			t.Fatalf("%s reached guest data rights: %d", role, st)
		}
	}

	// Export: the password, a reason, a real record.
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
	if doc.Format != "stayconnect.guest-data-export/2" || len(doc.Accounts) != 1 || doc.Accounts[0].Username != email ||
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

	// Erasure is refused while the guest is live: access still running, a device online.
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

	// ...and while the PMS still has the stay open.
	inHouse := seedGuestDataStay(t, f, "R-IN-"+sfx, "IN_HOUSE")
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", req("stay", inHouse, "guest asked", f.password)); st != 409 {
		t.Fatalf("erasing an in-house stay = %d %v", st, body)
	}

	// Erase the ended account.
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
	// The entry keeps who and when; what named the guest, and the record id, give way to the reference.
	expect("audit entry about the guest", str(`SELECT concat_ws('|', actor_id, payload->>'username', payload->>'erasure_ref', coalesce(target_id,'-'))
		FROM public.audit_log WHERE action='guest_account.password_set' AND tenant_id=$1`, f.tenant), "op-1|[erased]|"+ref+"|-")
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
	st, body = f.searchGuestData(t, q)
	matches, _ = body["matches"].([]any)
	if st != 200 || len(matches) != 1 || matches[0].(map[string]any)["kind"] != "principal" {
		t.Fatalf("search after erasure = %v", body)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", req("principal", principal, "same request", f.password)); st != 200 {
		t.Fatalf("erase identity = %d %v", st, body)
	}
	if st, body := f.searchGuestData(t, q); st != 200 || len(body["matches"].([]any)) != 0 {
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
	st, body := f.searchGuestData(t, "R-OUT-"+sfx)
	if m, _ := body["matches"].([]any); st != 200 || len(m) != 1 {
		t.Fatalf("search by reservation = %d %v", st, body)
	}
	if st, body := f.searchGuestData(t, "lovelace"); st != 200 || len(body["matches"].([]any)) != 1 {
		t.Fatalf("search by guest name = %d %v", st, body)
	}
	st, body = f.do(t, "POST", "/guest-data-rights/erase", map[string]any{"kind": "stay", "id": stay, "reason": "guest asked", "password": f.password})
	if st != 200 {
		t.Fatalf("erase stay = %d %v", st, body)
	}
	var reservation, names, payload, status string
	if err := f.pool.QueryRow(context.Background(), `SELECT s.external_reservation_id, concat_ws(',',g.first_name_norm,g.last_name_norm,g.display_name),
		e.payload::text, s.status FROM iam_v2.stays s JOIN iam_v2.stay_guests g ON g.stay_id=s.id JOIN iam_v2.stay_events e ON e.stay_id=s.id WHERE s.id=$1`,
		stay).Scan(&reservation, &names, &payload, &status); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reservation, "anon-") || names != "" || payload != `{"anonymised": true}` || status != "CHECKED_OUT" {
		t.Fatalf("stay after erasure: %s | %s | %s | %s", reservation, names, payload, status)
	}
}

// The export carries what the stay's PMS records and the guest's devices hold; erasure follows a room move,
// covers an unresolved PMS event, refuses while an event is waiting or a device is restricted, and erases a
// stay the PMS forgot to close.
func TestIntegration_GuestDataRights_ExportAndErasureCoverEverything(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	req := func(kind, id string) map[string]any {
		return map[string]any{"kind": kind, "id": id, "reason": "guest asked in writing", "password": f.password}
	}
	inStay := func(sql string, args ...any) {
		t.Helper()
		if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// An event the PMS sent that never resolved to a stay, naming the reservation.
	orphan := func(res, status string) string {
		t.Helper()
		var id string
		if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events(tenant_id,site_id,pms_interface_id,external_event_identity,
				  event_type,processing_status,payload)
				SELECT $1,$2,s.pms_interface_id,'EVO-'||$3,'GUEST_CHANGE','PENDING',jsonb_build_object('reservation',$3::text,'last_name','Lovelace')
				  FROM iam_v2.stays s WHERE s.external_reservation_id=$3 RETURNING id::text`, f.tenant, f.site, res).Scan(&id); err != nil {
				return err
			}
			if status == "PENDING" {
				return nil
			}
			_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status=$2, processed_at=now(),
				review_code=CASE WHEN $2='MANUAL_REVIEW' THEN 'NO_STAY_FOR_RESERVATION' END WHERE id=$1::uuid`, id, status)
			return err
		}); err != nil {
			t.Fatalf("seed orphan event: %v", err)
		}
		return id
	}

	// A checked-out stay, continued at another PMS (a transfer), with an event the PMS sent that never resolved.
	first, second := "R-MOVE-A-"+sfx, "R-MOVE-B-"+sfx
	a := seedGuestDataStay(t, f, first, "CHECKED_OUT")
	b := seedGuestDataStay(t, f, second, "CHECKED_OUT")
	inStay(`UPDATE iam_v2.stays SET vip=true, travel_agent='Grand Tours', rate_plan='BAR' WHERE id=$1::uuid`, a)
	if err := controlled(ctx, f.pool, nil, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.p5_begin_controlled_operation('entitlement_transfer')`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_links(tenant_id,site_id,from_stay,to_stay,reason)
			VALUES ($1,$2,$3::uuid,$4::uuid,'CROSS_PMS_TRANSFER')`, f.tenant, f.site, a, b)
		return err
	}); err != nil {
		t.Fatalf("seed stay link: %v", err)
	}
	ev := orphan(first, "MANUAL_REVIEW")

	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", req("stay", a))
	if st != 200 {
		t.Fatalf("export = %d %s", st, raw)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"remembered_devices", "pms_events", "post_stay_access", "payments", "refunds_that_ended_access",
		"room_charges", "sign_in_restrictions", "verification_access"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("the export has no %q", key)
		}
	}
	var stays []map[string]any
	var events []map[string]any
	_ = json.Unmarshal(doc["hotel_stays"], &stays)
	_ = json.Unmarshal(doc["pms_events"], &events)
	if len(stays) != 2 {
		t.Fatalf("the linked stay is not exported: %s", doc["hotel_stays"])
	}
	if !strings.Contains(string(doc["hotel_stays"]), `"travel_agent": "Grand Tours"`) || !strings.Contains(string(doc["hotel_stays"]), `"pms_guest_id"`) {
		t.Fatalf("the stay's PMS details are missing: %s", doc["hotel_stays"])
	}
	if len(events) != 3 || !strings.Contains(string(doc["pms_events"]), "Lovelace") {
		t.Fatalf("pms events = %s, want both stays' events and the unresolved one", doc["pms_events"])
	}

	// A waiting event refuses the erasure, and says why.
	c := seedGuestDataStay(t, f, "R-WAIT-"+sfx, "CHECKED_OUT")
	orphan("R-WAIT-"+sfx, "PENDING")
	st, body := f.do(t, "POST", "/guest-data-rights/erase", req("stay", c))
	if live, _ := body["live"].([]any); st != 409 || fmt.Sprint(live) != "[pms_pending]" {
		t.Fatalf("erasing with a waiting PMS event = %d %v", st, body)
	}

	// An event still waiting after 24 hours will not be processed. It no longer blocks the erasure:
	// it is given up, FAILED with RETENTION_GIVEN_UP, and its contents blanked.
	old := seedGuestDataStay(t, f, "R-OLDWAIT-"+sfx, "CHECKED_OUT")
	var oldEv string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events(tenant_id,site_id,pms_interface_id,external_event_identity,
			  event_type,processing_status,received_at,payload)
			SELECT $1,$2,s.pms_interface_id,'EVO-'||$3,'GUEST_CHANGE','PENDING',now() - interval '2 days',
			       jsonb_build_object('reservation',$3::text,'last_name','Lovelace')
			  FROM iam_v2.stays s WHERE s.external_reservation_id=$3 RETURNING id::text`, f.tenant, f.site, "R-OLDWAIT-"+sfx).Scan(&oldEv)
	}); err != nil {
		t.Fatalf("seed old waiting event: %v", err)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", req("stay", old)); st != 200 {
		t.Fatalf("erasing with a two-day-old waiting event = %d %v", st, body)
	}
	var evStatus, evCode, evPayload string
	_ = f.pool.QueryRow(ctx, `SELECT processing_status, coalesce(review_code,''), payload::text FROM iam_v2.stay_events WHERE id=$1`, oldEv).
		Scan(&evStatus, &evCode, &evPayload)
	if evStatus != "FAILED" || evCode != "RETENTION_GIVEN_UP" || evPayload != `{"anonymised": true}` {
		t.Fatalf("the old waiting event after erasure: %s %s %s", evStatus, evCode, evPayload)
	}

	// A stay the PMS left in-house after its departure is erased; one departing tomorrow is not.
	stale := seedGuestDataStay(t, f, "R-STALE-"+sfx, "IN_HOUSE")
	inStay(`UPDATE iam_v2.stays SET arrival=current_date - 9, departure=current_date - 5 WHERE id=$1::uuid`, stale)
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", req("stay", stale)); st != 200 {
		t.Fatalf("erasing a stay the PMS never closed = %d %v", st, body)
	}

	// Erasing the first stay erases the stay it continued as, and the unresolved event.
	st, body = f.do(t, "POST", "/guest-data-rights/erase", req("stay", a))
	if st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if counts, _ := body["counts"].(map[string]any); counts["hotel_stays"] != float64(2) || counts["pms_events"] != float64(3) {
		t.Fatalf("erase counts = %v", body)
	}
	var resB, payload string
	_ = f.pool.QueryRow(ctx, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, b).Scan(&resB)
	_ = f.pool.QueryRow(ctx, `SELECT payload::text FROM iam_v2.stay_events WHERE id=$1`, ev).Scan(&payload)
	if !strings.HasPrefix(resB, "anon-") || payload != `{"anonymised": true}` {
		t.Fatalf("after erasure: linked stay %q, unresolved event %s", resB, payload)
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
			  external_guest_id,first_name_norm,last_name_norm,display_name,is_primary)
			VALUES ($1,$2,$3::uuid,$4::uuid,'G-'||$5,'ada','lovelace','Ada Lovelace',true)`,
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

// A stay shared by two guests is exported and erased one guest at a time; the last guest erased is the whole stay.
func TestIntegration_GuestDataRights_OneGuestOfASharedStay(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	res := "R-SHARED-" + fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, res, "CHECKED_OUT")
	var ada, bob string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.stay_guests WHERE stay_id=$1`, stay).Scan(&ada); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,
			  external_guest_id,first_name_norm,last_name_norm,display_name,is_primary)
			SELECT tenant_id,site_id,pms_interface_id,id,'G-BOB','BOB','BAKER','Bob Baker',false FROM iam_v2.stays WHERE id=$1
			RETURNING id::text`, stay).Scan(&bob); err != nil {
			return err
		}
		var ev string
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events(tenant_id,site_id,pms_interface_id,external_event_identity,
			  event_type,processing_status,payload)
			SELECT tenant_id,site_id,pms_interface_id,'EV-SH-'||$2,'GUEST_CHANGE','PENDING',
			       jsonb_build_object('reservation',$2::text,'room','101','last_name','Lovelace','first_name','Ada',
			         'sharers',jsonb_build_array(
			           jsonb_build_object('external_guest_id','G-'||$2,'first_name','Ada','last_name','Lovelace','is_primary',true),
			           jsonb_build_object('external_guest_id','G-BOB','first_name','Bob','last_name','Baker','is_primary',false)))
			  FROM iam_v2.stays WHERE id=$1 RETURNING id::text`, stay, res).Scan(&ev); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status='APPLIED', stay_id=$2::uuid, processed_at=now() WHERE id=$1::uuid`, ev, stay)
		return err
	}); err != nil {
		t.Fatalf("seed second guest: %v", err)
	}
	req := func(kind, id string) map[string]any {
		return map[string]any{"kind": kind, "id": id, "reason": "guest asked", "password": f.password}
	}

	// The search offers each guest, not the stay.
	st, body := f.searchGuestData(t, res)
	kinds := map[string]int{}
	for _, m := range body["matches"].([]any) {
		kinds[m.(map[string]any)["kind"].(string)]++
	}
	if st != 200 || kinds["stay_guest"] != 2 || kinds["stay"] != 0 {
		t.Fatalf("search = %d %v", st, body)
	}
	// The stay whole can be neither exported nor erased.
	if st, _ := f.do(t, "POST", "/guest-data-rights/export", req("stay", stay)); st != 400 {
		t.Fatalf("a shared stay was exported whole: %d", st)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", req("stay", stay)); st != 409 || fmt.Sprint(body["live"]) != "[co_guests]" {
		t.Fatalf("a shared stay was erased whole: %d %v", st, body)
	}

	// Ada's export holds Ada, not Bob.
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", req("stay_guest", ada))
	if st != 200 || !strings.Contains(string(raw), "Lovelace") || strings.Contains(strings.ToLower(string(raw)), "baker") ||
		strings.Contains(string(raw), "G-BOB") || !strings.Contains(string(raw), "shared_stays_note") || !strings.Contains(string(raw), res) {
		t.Fatalf("export of one guest = %d %s", st, raw)
	}

	// Erasing Ada leaves Bob and the stay.
	st, body = f.do(t, "POST", "/guest-data-rights/erase", req("stay_guest", ada))
	counts, _ := body["counts"].(map[string]any)
	if st != 200 || counts["stay_guests"] != float64(1) || counts["hotel_stays"] != float64(0) {
		t.Fatalf("erase one guest = %d %v", st, body)
	}
	var reservation, adaNames, bobNames, payloads string
	if err := f.pool.QueryRow(ctx, `SELECT s.external_reservation_id,
		  (SELECT concat_ws(',',first_name_norm,last_name_norm,display_name,external_guest_id) FROM iam_v2.stay_guests WHERE id=$2),
		  (SELECT concat_ws(',',first_name_norm,last_name_norm,display_name,external_guest_id) FROM iam_v2.stay_guests WHERE id=$3),
		  (SELECT string_agg(payload::text, ' ') FROM iam_v2.stay_events WHERE stay_id=s.id)
		FROM iam_v2.stays s WHERE s.id=$1`, stay, ada, bob).Scan(&reservation, &adaNames, &bobNames, &payloads); err != nil {
		t.Fatal(err)
	}
	if reservation != res || adaNames != "" || bobNames != "BOB,BAKER,Bob Baker,G-BOB" ||
		strings.Contains(strings.ToLower(payloads), "lovelace") || !strings.Contains(payloads, "Baker") || !strings.Contains(payloads, res) {
		t.Fatalf("after erasing one guest: %s | %q | %q | %s", reservation, adaNames, bobNames, payloads)
	}

	// Bob is now the only guest: the search offers the stay, and erasing him is the whole stay.
	st, body = f.searchGuestData(t, res)
	if m, _ := body["matches"].([]any); st != 200 || len(m) != 1 || m[0].(map[string]any)["kind"] != "stay" {
		t.Fatalf("search after = %d %v", st, body)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", req("stay_guest", bob)); st != 200 {
		t.Fatalf("erase the last guest = %d %v", st, body)
	}
	if err := f.pool.QueryRow(ctx, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, stay).Scan(&reservation); err != nil || !strings.HasPrefix(reservation, "anon-") {
		t.Fatalf("the last guest's erasure must anonymise the stay: %q %v", reservation, err)
	}
}

// A one-guest stay that continued into a shared room. Exporting or erasing
// it reaches the guest's own row on the shared stay, never the other guest, and never the shared stay whole.
func TestIntegration_GuestDataRights_AStayLinkDoesNotLeadIntoASharedStay(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	first := seedGuestDataStay(t, f, "R-MOVE-A-"+sfx, "CHECKED_OUT")
	shared := seedGuestDataStay(t, f, "R-MOVE-B-"+sfx, "CHECKED_OUT")
	var adaFirst, adaShared, bob string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		// The same guest on both stays, by PMS guest number; Bob shares the second room.
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.stay_guests SET external_guest_id='G-ADA-'||$2 WHERE stay_id = ANY($1::uuid[])`,
			[]string{first, shared}, sfx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.stay_guests WHERE stay_id=$1`, first).Scan(&adaFirst); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.stay_guests WHERE stay_id=$1`, shared).Scan(&adaShared); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,
			  external_guest_id,first_name_norm,last_name_norm,display_name,is_primary)
			SELECT tenant_id,site_id,pms_interface_id,id,'G-BOB-'||$2,'BOB','BAKER','Bob Baker',false FROM iam_v2.stays WHERE id=$1
			RETURNING id::text`, shared, sfx).Scan(&bob); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT iam_v2.p5_begin_controlled_operation('entitlement_transfer')`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_links(tenant_id,site_id,from_stay,to_stay,reason)
			VALUES ($1,$2,$3::uuid,$4::uuid,'CROSS_PMS_TRANSFER')`, f.tenant, f.site, first, shared)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req := func(kind, id string) map[string]any {
		return map[string]any{"kind": kind, "id": id, "reason": "guest asked", "password": f.password}
	}

	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", req("stay", first))
	if st != 200 || strings.Contains(strings.ToLower(raw), "baker") || strings.Contains(raw, "G-BOB-") ||
		!strings.Contains(raw, "R-MOVE-B-"+sfx) || !strings.Contains(raw, "shared_stays_note") {
		t.Fatalf("export of the first stay = %d %s", st, raw)
	}

	st, body := f.do(t, "POST", "/guest-data-rights/erase", req("stay", first))
	counts, _ := body["counts"].(map[string]any)
	if st != 200 || counts["hotel_stays"] != float64(1) || counts["stay_guests"] != float64(1) || counts["hotel_stays_kept_shared"] != float64(1) {
		t.Fatalf("erase the first stay = %d %v", st, body)
	}
	str := func(q string, args ...any) string {
		var v string
		if err := f.pool.QueryRow(ctx, q, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	for name, got := range map[string][2]string{
		"first stay anonymised": {str(`SELECT (external_reservation_id LIKE 'anon-%')::text FROM iam_v2.stays WHERE id=$1`, first), "true"},
		"shared stay kept":      {str(`SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, shared), "R-MOVE-B-" + sfx},
		"Ada gone from shared":  {str(`SELECT concat_ws(',',first_name_norm,last_name_norm,display_name,external_guest_id) FROM iam_v2.stay_guests WHERE id=$1`, adaShared), ""},
		"Bob kept":              {str(`SELECT concat_ws(',',first_name_norm,last_name_norm,display_name) FROM iam_v2.stay_guests WHERE id=$1`, bob), "BOB,BAKER,Bob Baker"},
		"Ada gone from first":   {str(`SELECT concat_ws(',',first_name_norm,last_name_norm,display_name) FROM iam_v2.stay_guests WHERE id=$1`, adaFirst), ""},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
}

// A stay the PMS left open with no departure date is erasable once
// the PMS's latest full room list no longer has it, and refused while no such list says so.
func TestIntegration_GuestDataRights_AnOpenStayWithNoDepartureLeftOffTheRoomList(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	stay := seedGuestDataStay(t, f, "R-OPEN-"+fmt.Sprint(time.Now().UnixNano()), "IN_HOUSE")
	var iface string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `UPDATE iam_v2.stays SET departure = NULL, arrival = current_date - 90 WHERE id=$1::uuid
			RETURNING pms_interface_id::text`, stay).Scan(&iface); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	erase := func() (int, map[string]any) {
		return f.do(t, "POST", "/guest-data-rights/erase", map[string]any{"kind": "stay", "id": stay, "reason": "guest asked", "password": f.password})
	}
	if st, body := erase(); st != 409 || fmt.Sprint(body["live"]) != "[pms_open]" {
		t.Fatalf("with no room list the open stay must be refused: %d %v", st, body)
	}
	// The PMS publishes a full room list (generation 1) without this stay.
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		  (tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode, resync_generation_seq, published_resync_generation,
		   last_complete_sync_at)
		VALUES ($1,$2,$3::uuid,1,'NONE',1,1, now())`, f.tenant, f.site, iface); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if st, body := erase(); st != 200 {
		t.Fatalf("a stay left off the room list must be erasable: %d %v", st, body)
	}
}

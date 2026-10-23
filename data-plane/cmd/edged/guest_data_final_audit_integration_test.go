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

// audit_log is a hypertable: a row's ctid is unique only inside its chunk. The erasure must rewrite the rows it
// means and no others, whatever chunk they are in.
func TestIntegration_FinalAudit_ErasureRewritesOnlyTheRowsItMeans(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "chunks-" + sfx + "@example.com"
	acct, _, _ := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.97.0.10")
	for i := 0; i < 50; i++ {
		if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
			VALUES (now() - make_interval(days => $2::int),$1,'operator','op-1','other.thing','vlan',$3,jsonb_build_object('memo','keep me '||$4::text))`,
			f.tenant, i*2, fmt.Sprintf("vlan-%d", i), fmt.Sprint(i)); err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}
	for _, d := range []int{3, 40} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
			VALUES (now() - make_interval(days => $2::int),$1,'operator','op-1','guest_account.updated','guest_account',$3,jsonb_build_object('username',$4::text))`,
			f.tenant, d, acct, email); err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND action='other.thing' AND payload->>'memo' LIKE 'keep me %' AND target_id LIKE 'vlan-%'`, f.tenant); got != "50" {
		t.Errorf("the erasure rewrote unrelated audit entries: %s of 50 intact", got)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND action='guest_account.updated' AND payload::text NOT LIKE '%'||$2||'%' AND target_id IS NULL`, f.tenant, email); got != "2" {
		t.Errorf("the entries about the account were not cleaned: %s of 2", got)
	}
}

// A stay linked to another person's stay (a transfer, a post-stay chain) is followed only to the same guest.
func TestIntegration_FinalAudit_AStayLinkNeverLeadsToAnotherPerson(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	ada := seedGuestDataStay(t, f, "R-LA-"+sfx, "CHECKED_OUT")
	bob := seedGuestDataStay(t, f, "R-LB-"+sfx, "CHECKED_OUT")
	adaAgain := seedGuestDataStay(t, f, "R-LC-"+sfx, "CHECKED_OUT")
	guestRow(t, f, ada, "G-ADA-"+sfx, "ada", "lovelace")
	guestRow(t, f, adaAgain, "G-ADA-"+sfx, "ada", "lovelace")
	guestRow(t, f, bob, "G-BOB-"+sfx, "bob", "baker")
	linkStays(t, f, ada, bob)
	linkStays(t, f, bob, adaAgain)

	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", ada, "guest asked"))
	if st != 200 || strings.Contains(raw, "Baker") || strings.Contains(raw, "G-BOB-"+sfx) || strings.Contains(raw, "R-LB-"+sfx) {
		t.Fatalf("an export of Ada's stay carries Bob's reservation: %d %s", st, raw)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", ada, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, bob); got != "R-LB-"+sfx {
		t.Errorf("erasing Ada's stay anonymised Bob's reservation: %s", got)
	}
	if got := gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm) FROM iam_v2.stay_guests WHERE stay_id=$1`, bob); got != "bob,baker" {
		t.Errorf("Bob's guest row changed: %s", got)
	}
	if got := gdStr(t, f, `SELECT (external_reservation_id LIKE 'anon-%')::text FROM iam_v2.stays WHERE id=$1`, ada); got != "true" {
		t.Error("Ada's own stay was not anonymised")
	}
}

// One person, one phone, an account and a portal identity on the same address: erasing both frees the phone.
func TestIntegration_FinalAudit_APersonsPhoneIsFreedOnceTheirOtherRecordIsErased(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "cleo-" + sfx + "@example.com"
	mac := "02:aa:00:97:00:01"
	acct := selfAccount(t, f, email, true, "", mac)
	ent := newAccess(t, f, "cleo"+sfx, "guest_account_id", acct, false)
	dev := newDevice(t, f, mac)
	sess := onDevice(t, f, ent, dev, "10.97.1.1", "ACCOUNT", false)
	var principal string
	if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Cleo') RETURNING id)
		INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
		SELECT $1, gp.id, 'EMAIL', '', $2, now() - interval '9 days', '{}'::jsonb FROM gp RETURNING guest_principal_id::text`, f.tenant, email).Scan(&principal); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.principal_device_credentials(tenant_id,site_id,guest_principal_id,device_id,method,secret_hmac,key_id,expires_at)
		VALUES ($1,$2,$3::uuid,$4::uuid,'OTP',public.gen_random_bytes(32),'k1', now() + interval '30 days')`, f.tenant, f.site, principal, dev); err != nil {
		t.Fatalf("seed remembered device: %v", err)
	}
	// The account first: the phone is also the identity's, so it is left (shared), as before.
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("erase the account = %d %v", st, body)
	}
	if got := devMAC(t, f, dev); got != mac {
		t.Fatalf("the phone was anonymised while an identity of a person still used it: %s", got)
	}
	// Then the identity: nothing identifiable is left that used the phone, so it goes with it.
	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "principal", principal, "guest asked"))
	if c := countsOf(body); st != 200 || c["devices"] != float64(1) || c["devices_kept_shared"] != float64(0) {
		t.Fatalf("erase the identity = %d %v", st, body)
	}
	if got := devMAC(t, f, dev); !strings.HasPrefix(got, "03:") {
		t.Errorf("the person's phone was kept: %s", got)
	}
	if got := sessAddr(t, f, sess); got != "" {
		t.Errorf("the person's session address was kept: %s", got)
	}
}

// Another living person still using the phone keeps it shared, even when the first record was erased.
func TestIntegration_FinalAudit_AnotherLivingPersonStillKeepsAPhoneShared(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	mac := "02:aa:00:97:00:02"
	acct1 := selfAccount(t, f, "one-"+sfx+"@example.com", true, "", mac)
	acct2 := selfAccount(t, f, "two-"+sfx+"@example.com", true, "", "")
	e1 := newAccess(t, f, "one"+sfx, "guest_account_id", acct1, false)
	e2 := newAccess(t, f, "two"+sfx, "guest_account_id", acct2, false)
	dev := newDevice(t, f, mac)
	onDevice(t, f, e1, dev, "10.97.2.1", "ACCOUNT", false)
	onDevice(t, f, e2, dev, "10.97.2.2", "ACCOUNT", false)
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct1, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := devMAC(t, f, dev); got != mac {
		t.Errorf("a phone a second living account used was anonymised: %s", got)
	}
}

// What was said about the guest loses every way of naming them, and so does an entry they were the actor of.
func TestIntegration_FinalAudit_EntriesAboutTheGuestLoseTheirSurnameRoomAndActorId(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	res := "R-AUD-" + sfx
	stay := seedGuestDataStay(t, f, res, "CHECKED_OUT")
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
		VALUES (now() - interval '1 hour',$1,'operator','op-1','stay.note','stay',$2,jsonb_build_object('room','101','guest','Lovelace','note','called about wifi')),
		       (now() - interval '1 hour',$1,'guest',$3,'portal.something','session',NULL,jsonb_build_object('x','y'))`,
		f.tenant, stay, res); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND action='stay.note' AND (payload::text ILIKE '%Lovelace%' OR payload::text LIKE '%101%')`, f.tenant); got != "0" {
		t.Errorf("an entry about the stay still names the guest or the room (%s)", got)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND actor_id=$2`, f.tenant, res); got != "0" {
		t.Errorf("an entry the guest was the actor of kept their reservation number (%s)", got)
	}
}

// The export gives the person what is attributable to them: what staff wrote about their access, what was done with
// their devices, the log entries the erasure would rewrite, and what is kept by law.
func TestIntegration_FinalAudit_TheExportCoversWhatTheErasureRewritesAndWhatStaffWrote(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	mac := "02:aa:00:99:00:01"
	email := "export-" + sfx + "@example.com"
	acct := selfAccount(t, f, email, true, "", mac)
	ent := newAccess(t, f, "exp"+sfx, "guest_account_id", acct, false)
	dev := newDevice(t, f, mac)
	onDevice(t, f, ent, dev, "10.99.0.1", "ACCOUNT", false)
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		for _, q := range []string{
			`INSERT INTO iam_v2.entitlement_adjustments(tenant_id,site_id,entitlement_id,field,reason) VALUES ('` + f.tenant + `','` + f.site + `','` + ent + `'::uuid,'window','goodwill extension for the guest')`,
			`INSERT INTO iam_v2.device_slot_actions(tenant_id,site_id,entitlement_id,device_id,actor,operator_label,action,outcome,reason) VALUES ('` + f.tenant + `','` + f.site + `','` + ent + `'::uuid,'` + dev + `'::uuid,'STAFF','frontdesk@hotel.test','RELEASE','OK','guest asked at the desk')`,
			`INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload) VALUES (now() - interval '1 hour','` + f.tenant + `','operator','op-1','device.seen','device','` + mac + `',jsonb_build_object('ip','10.99.0.1'))`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "account", acct, "guest asked"))
	if st != 200 {
		t.Fatalf("export = %d %s", st, raw)
	}
	for _, want := range []string{"goodwill extension for the guest", "guest asked at the desk", "access_adjustments", "device_actions", "device.seen", "kept_by_law", "account_security"} {
		if !strings.Contains(raw, want) {
			t.Errorf("the export lacks %q", want)
		}
	}
	if strings.Contains(raw, "frontdesk@hotel.test") {
		t.Error("the export hands the guest a staff member's email")
	}
}

// A reservation export lists the person's other reservations by their PMS guest number, and the staff notes about it.
func TestIntegration_FinalAudit_AReservationExportNamesTheOtherReservationsOfTheSamePmsGuest(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	first := seedGuestDataStay(t, f, "R-X1-"+sfx, "CHECKED_OUT")
	second := secondStaySameInterface(t, f, first, "R-X2-"+sfx)
	guestRow(t, f, first, "G-SAME-"+sfx, "ada", "lovelace")
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE iam_v2.stay_guests SET external_guest_id=$2 WHERE stay_id=$1::uuid`, second, "G-SAME-"+sfx)
		return err
	})
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", first, "guest asked"))
	if st != 200 || !strings.Contains(raw, "R-X2-"+sfx) || !strings.Contains(raw, "other_reservations_with_your_pms_guest_number") {
		t.Fatalf("the export does not name the guest's other reservation: %d %s", st, raw)
	}
	if strings.Contains(raw, "Bianchi") {
		t.Error("the other reservation's guest name is in the export")
	}
}

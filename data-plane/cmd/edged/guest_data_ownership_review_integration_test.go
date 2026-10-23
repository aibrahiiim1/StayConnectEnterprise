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

// pmsEvent adds a PMS message on an interface. stay != "" makes it an applied message of that stay; otherwise it
// never resolved to a stay (waiting when pending, else sent to manual review).
func pmsEvent(t *testing.T, f *apiFixture, iface, stay, payload string, pending bool, receivedAgo time.Duration) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events(tenant_id,site_id,pms_interface_id,external_event_identity,
			  event_type,processing_status,payload,received_at)
			VALUES ($1,$2,$3::uuid,'EV-'||gen_random_uuid(),'GUEST_CHANGE','PENDING',$4::jsonb, now() - $5::interval)
			RETURNING id::text`, f.tenant, f.site, iface, payload, fmt.Sprintf("%d seconds", int(receivedAgo.Seconds()))).Scan(&id); err != nil {
			return err
		}
		switch {
		case stay != "":
			_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status='APPLIED', stay_id=$2::uuid, processed_at=now() WHERE id=$1::uuid`, id, stay)
			return err
		case !pending:
			_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status='MANUAL_REVIEW', review_code='NO_STAY_FOR_RESERVATION',
				processed_at=now() WHERE id=$1::uuid`, id)
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed PMS event: %v", err)
	}
	return id
}

// seedStayOn is one checked-out stay with one named guest on an existing PMS interface.
func seedStayOn(t *testing.T, f *apiFixture, iface, res, status string) string {
	t.Helper()
	ctx := context.Background()
	var stay string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(tenant_id,site_id,pms_interface_id,external_reservation_id,
			  external_stay_identity,normalized_room_number,status,arrival,departure,effective_checkout_at)
			VALUES ($1,$2,$3::uuid,$4,$4,'102',$5,current_date - 3,current_date + 1,
			        CASE WHEN $5 = 'CHECKED_OUT' THEN now() - interval '1 day' END) RETURNING id::text`,
			f.tenant, f.site, iface, res, status).Scan(&stay); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,
			  external_guest_id,first_name_norm,last_name_norm,display_name,is_primary)
			VALUES ($1,$2,$3::uuid,$4::uuid,'G-'||$5,'x','y','X Y',true)`, f.tenant, f.site, iface, stay, res)
		return err
	}); err != nil {
		t.Fatalf("seed stay %s: %v", res, err)
	}
	return stay
}

func ifaceOf(t *testing.T, f *apiFixture, stay string) string {
	return gdStr(t, f, `SELECT pms_interface_id::text FROM iam_v2.stays WHERE id=$1`, stay)
}

// Erasing a guest rewrites the activity-log entries about them, never an entry that merely contains a room
// number or a surname.
func TestIntegration_OwnershipReview_TheActivityLogIsNotRewrittenForARoomOrASurname(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	res := "R-AUD-" + sfx
	stay := seedGuestDataStay(t, f, res, "CHECKED_OUT")
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload) VALUES
		(now() - interval '1 day',$1,'operator','op-1','vlan.created','vlan','vlan-'||$2,jsonb_build_object('note','VLAN 101 created for pool','room','101','ip','10.0.101.5')),
		(now() - interval '1 day',$1,'operator','op-1','request.logged','request','req-'||$2,jsonb_build_object('note','a Lovelace family request, not hers','by','jane.lovelace@y.test')),
		(now() - interval '1 day',$1,'operator','op-1','stay.note_added','stay',$3,jsonb_build_object('reservation',$4::text,'about','Ada Lovelace in room 101')),
		(now() - interval '1 day',$1,'operator','op-2','guest_data.searched','guest_data',NULL,jsonb_build_object('search','lovel')),
		(now() - interval '1 day',$1,'operator','op-2','guest_data.searched','guest_data',NULL,jsonb_build_object('search','zebra'))`,
		f.tenant, sfx, stay, res); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	note := func(action string) string {
		return gdStr(t, f, `SELECT payload::text || '|' || coalesce(target_id,'-') FROM public.audit_log WHERE action=$1 AND tenant_id=$2 AND actor_id IN ('op-1','op-2') ORDER BY ts DESC, payload::text LIMIT 1`, action, f.tenant)
	}
	if got := note("vlan.created"); !strings.Contains(got, "VLAN 101 created for pool") || !strings.Contains(got, `"room": "101"`) || !strings.Contains(got, "10.0.101.5") || strings.Contains(got, "erased") {
		t.Errorf("an entry about a VLAN was rewritten for sharing the room number: %s", got)
	}
	if got := note("request.logged"); !strings.Contains(got, "a Lovelace family request") || strings.Contains(got, "erased") {
		t.Errorf("an entry about someone else was rewritten for sharing a surname: %s", got)
	}
	if got := note("stay.note_added"); strings.Contains(got, res) || strings.Contains(got, "Lovelace") || !strings.HasSuffix(got, "|-") {
		t.Errorf("an entry about the reservation kept the guest's identifiers: %s", got)
	}
	if got := gdStr(t, f, `SELECT payload->>'search' FROM public.audit_log WHERE action='guest_data.searched' AND payload->>'search' IN ('lovel','[erased]') AND tenant_id=$1`, f.tenant); got != "[erased]" {
		t.Errorf("a staff search for part of the guest's name was kept: %q", got)
	}
	if got := gdStr(t, f, `SELECT payload->>'search' FROM public.audit_log WHERE action='guest_data.searched' AND payload->>'search' = 'zebra' AND tenant_id=$1`, f.tenant); got != "zebra" {
		t.Errorf("an unrelated staff search was rewritten: %q", got)
	}
}

// Erasing one guest of a shared stay changes only that guest's name in the stay's PMS messages. A message
// still waiting is never given up, and a co-guest's data in an applied message stays.
func TestIntegration_OwnershipReview_AGuestOfASharedStayCannotDestroyAnotherGuestsMessage(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	res := "R-H2-" + sfx
	stay := seedGuestDataStay(t, f, res, "CHECKED_OUT")
	john := guestRow(t, f, stay, "G-77-"+sfx, "JOHN", "SMITH")
	mary := addGuest(t, f, stay, "G-88-"+sfx, "MARY", "JONES")
	iface := ifaceOf(t, f, stay)
	both := fmt.Sprintf(`{"reservation":%q,"sharers":[{"external_guest_id":"G-77-%s","first_name":"John","last_name":"Smith"},{"external_guest_id":"G-88-%s","first_name":"Mary","last_name":"Jones"}]}`, res, sfx, sfx)
	applied := pmsEvent(t, f, iface, stay, both, false, 48*time.Hour)
	waiting := pmsEvent(t, f, iface, "", both, true, 72*time.Hour) // never resolved, still waiting after three days

	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay_guest", john, "guest asked")); st != 200 {
		t.Fatalf("erase John = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT processing_status || '|' || coalesce(review_code,'-') || '|' || (payload->'sharers')::text FROM iam_v2.stay_events WHERE id=$1`, waiting); !strings.HasPrefix(got, "PENDING|-|") || !strings.Contains(got, "Mary") {
		t.Errorf("a message still waiting was given up, or lost the co-guest: %s", got)
	}
	got := gdStr(t, f, `SELECT (payload->'sharers')::text FROM iam_v2.stay_events WHERE id=$1`, applied)
	if strings.Contains(got, "Smith") || !strings.Contains(got, "Mary") || !strings.Contains(got, "Jones") {
		t.Errorf("an applied message after erasing John: %s (John gone, Mary kept)", got)
	}
	if row := gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm) FROM iam_v2.stay_guests WHERE id=$1`, mary); row != "MARY,JONES" {
		t.Errorf("the co-guest's row was changed: %q", row)
	}
	_ = ctx
}

// A reservation number means something only inside one PMS interface; a message that merely repeats it on
// another interface is another reservation's.
func TestIntegration_OwnershipReview_AReservationNumberOnAnotherInterfaceIsAnotherReservation(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	res := "R-H3-" + sfx
	stay := seedGuestDataStay(t, f, res, "CHECKED_OUT")
	other := ifaceOf(t, f, seedGuestDataStay(t, f, "R-H3-OTHER-"+sfx, "CHECKED_OUT"))
	mine := pmsEvent(t, f, ifaceOf(t, f, stay), "", fmt.Sprintf(`{"reservation":%q,"last_name":"Lovelace"}`, res), false, 48*time.Hour)
	theirs := pmsEvent(t, f, other, "", fmt.Sprintf(`{"reservation":%q,"last_name":"Jones"}`, res), false, 48*time.Hour)

	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", stay, "guest asked"))
	if st != 200 || strings.Contains(raw, "Jones") {
		t.Fatalf("the export carries another interface's message: %d %s", st, raw)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT payload::text FROM iam_v2.stay_events WHERE id=$1`, mine); got != `{"anonymised": true}` {
		t.Errorf("the reservation's own unresolved message was kept: %s", got)
	}
	if got := gdStr(t, f, `SELECT payload->>'last_name' FROM iam_v2.stay_events WHERE id=$1`, theirs); got != "Jones" {
		t.Errorf("another interface's message was anonymised: %q", got)
	}
}

// A staff-issued account may be given to a whole party, so what other people typed or paid through it is not
// attributed to the account holder; a self-registered account's own purchases are.
func TestIntegration_OwnershipReview_AStaffIssuedCredentialsAttemptsAndPaymentsAreNotTheHolders(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	staff := selfAccount(t, f, "frontdesk-"+sfx, false, "", "")
	staffEnt := newAccess(t, f, "fd"+sfx, "guest_account_id", staff, false)
	own := selfAccount(t, f, "solo-"+sfx+"@example.com", true, "", "")
	ownEnt := newAccess(t, f, "solo"+sfx, "guest_account_id", own, false)
	for _, ent := range []string{staffEnt, ownEnt} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.sign_in_attempts
			  (tenant_id, site_id, occurred_at, result, verifier_kind, entitlement_id, device_mac, device_ip)
			VALUES ($1,$2,now() - interval '1 day','VERIFIED','UNKNOWN',$3::uuid,
			        CASE WHEN $3::uuid = $4::uuid THEN '02:dd:00:00:00:02'::macaddr ELSE '02:dd:00:00:00:03'::macaddr END,
			        CASE WHEN $3::uuid = $4::uuid THEN '10.5.5.2'::inet ELSE '10.5.5.3'::inet END)`, f.tenant, f.site, ent, staffEnt); err != nil {
			t.Fatalf("seed attempt: %v", err)
		}
	}
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "account", staff, "guest asked"))
	if st != 200 || strings.Contains(raw, "10.5.5.2") || strings.Contains(raw, "02:dd:00:00:00:02") || !strings.Contains(raw, `"purchases": []`) {
		t.Fatalf("a staff-issued account's export carries attempts or purchases made through a shared credential: %d %s", st, raw)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", staff, "guest asked")); st != 200 {
		t.Fatalf("erase the staff-issued account = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT host(device_ip) || '|' || device_mac::text FROM iam_v2.sign_in_attempts WHERE entitlement_id=$1`, staffEnt); got != "10.5.5.2|02:dd:00:00:00:02" {
		t.Errorf("an attempt made through the shared credential was changed: %s", got)
	}
	st, raw = f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "account", own, "guest asked"))
	if st != 200 || !strings.Contains(raw, "10.5.5.3") || !strings.Contains(raw, "ADMIN_GRANT") {
		t.Errorf("a self-registered account's own attempt and purchase are missing: %d %s", st, raw)
	}
}

// A namesake without a PMS number cannot be told from the guest who has one; refused, never guessed.
func TestIntegration_OwnershipReview_ANamesakeWithoutANumberMakesTheGuestAmbiguous(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	a := seedGuestDataStay(t, f, "R-M2A-"+sfx, "CHECKED_OUT")
	iface := ifaceOf(t, f, a)
	b := seedStayOn(t, f, iface, "R-M2B-"+sfx, "CHECKED_OUT") // the same PMS interface, so a guest number can decide
	c := seedStayOn(t, f, iface, "R-M2C-"+sfx, "CHECKED_OUT")
	a1 := guestRow(t, f, a, "G-77-"+sfx, "JOHN", "SMITH")
	b1 := guestRow(t, f, b, "G-77-"+sfx, "JOHN", "SMITH")
	addGuest(t, f, b, "", "JOHN", "SMITH") // an id-less namesake on the same stay
	c1 := guestRow(t, f, c, "G-77-"+sfx, "JOHN", "SMITH")
	rows := func(anchor, stay string) (string, error) {
		var out string
		err := f.pool.QueryRow(ctx, `SELECT array_to_string(iam_v2.same_guest_rows(ARRAY[$1::uuid], ARRAY[$2::uuid]), ',')`, anchor, stay).Scan(&out)
		return out, err
	}
	if _, err := rows(a1, b); err == nil || !strings.Contains(err.Error(), "ambiguous guest") {
		t.Errorf("a number plus an id-less namesake on one stay must be refused as ambiguous, got %v", err)
	}
	if got, err := rows(a1, c); err != nil || got != c1 {
		t.Errorf("one clear match on a stay = %q %v, want %s", got, err, c1)
	}
	_ = b1
}

// A device where someone else proved an address, or used another person's access, is not this person's;
// their own one-time codes tie a verification and a code binding to them whatever device it happened on.
func TestIntegration_OwnershipReview_ADeviceSomeoneElseVerifiedOnIsNotThePersons(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "tab-" + sfx + "@example.com"
	mac := "02:aa:00:09:00:01"
	acct := selfAccount(t, f, email, true, "", mac)
	ent := newAccess(t, f, "tab"+sfx, "guest_account_id", acct, false)
	dev := newDevice(t, f, mac)
	onDevice(t, f, ent, dev, "10.77.9.1", "ACCOUNT", false)
	var mine, theirs string
	for _, c := range []struct {
		dst  *string
		dest string
	}{{&mine, email}, {&theirs, "someone-else-" + sfx + "@example.com"}} {
		if err := f.pool.QueryRow(ctx, `INSERT INTO public.auth_otps(tenant_id,channel,destination,code_hash,issued_at,expires_at,ip)
			VALUES ($1,'email',$2,'h',now() - interval '2 days',now() - interval '2 days' + interval '10 minutes','10.88.0.5') RETURNING id::text`,
			f.tenant, c.dest).Scan(c.dst); err != nil {
			t.Fatalf("seed code: %v", err)
		}
	}
	grant := func(challenge, ip string) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.verification_access_grants
			  (tenant_id, site_id, device_mac, ip, bridge, challenge_id, destination_digest, started_at, expires_at, data_cap_bytes, down_kbps, up_kbps, ended_at, end_reason)
			VALUES ($1,$2,$3::macaddr,$5::inet,'br0',$4::uuid,encode(public.gen_random_bytes(32), 'hex'),now() - interval '2 days',now() - interval '2 days' + interval '1 hour',1000000,1000,1000, now() - interval '47 hours', 'EXPIRED')`,
			f.tenant, f.site, mac, challenge, ip); err != nil {
			t.Fatalf("seed verification: %v", err)
		}
	}
	grant(mine, "10.77.9.50")
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.otp_device_bindings(challenge_id, tenant_id, device_mac) VALUES ($1::uuid,$2,$3::macaddr)`, mine, f.tenant, mac); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "account", acct, "guest asked"))
	if st != 200 || !strings.Contains(raw, mac) || !strings.Contains(raw, `"verification_access": [{`) || !strings.Contains(raw, `"code_device_bindings": [{`) {
		t.Fatalf("a device only the person's codes touched must be theirs: %d %s", st, raw)
	}
	grant(theirs, "10.77.9.60")
	st, raw = f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "account", acct, "guest asked"))
	if st != 200 || strings.Contains(raw, "10.77.9.1\"") || strings.Contains(raw, "10.77.9.60") ||
		!strings.Contains(raw, "10.77.9.50") || !strings.Contains(raw, `"shared_devices": 1`) {
		t.Fatalf("a shared device's session, or the other person's verification, is in the export (or the person's own verification is missing): %d %s", st, raw)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := devMAC(t, f, dev); got != mac {
		t.Errorf("a device someone else verified on was anonymised: %s", got)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.verification_access_grants WHERE device_mac = $1::macaddr AND challenge_id = $2::uuid`, mac, theirs); got != "1" {
		t.Errorf("the other person's verification record was changed: %s rows left under their code", got)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.verification_access_grants WHERE device_mac = $1::macaddr AND challenge_id = $2::uuid`, mac, mine); got != "0" {
		t.Errorf("the person's own verification record was kept: %s rows left under their code", got)
	}
}

// A surname shared with a relative is neither handed over nor removed; a reservation with one named
// guest still never lists other people's sharer entries in the holder's export.
func TestIntegration_OwnershipReview_ASharedSurnameIsNobodysAndSharersAreFiltered(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	rivals := `[{"id":"G1","iface":"00000000-0000-0000-0000-000000000001","first":"JOHN","last":"SMITH","rival":true}]`
	none := `[{"id":"G1","iface":"00000000-0000-0000-0000-000000000001","first":"JOHN","last":"SMITH","rival":false}]`
	run := func(guests, payload string, keep bool) string {
		var out string
		if err := f.pool.QueryRow(ctx, `SELECT iam_v2.event_payload_for_guests($1::jsonb, $2::jsonb, $3, '00000000-0000-0000-0000-000000000001'::uuid)::text`, payload, guests, keep).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Mary Smith has no first name in the message; John has a relative of the same surname on the stay.
	pay := `{"sharers":[{"last_name":"Smith"},{"first_name":"Mary","last_name":"Smith"}]}`
	if got := run(rivals, pay, false); !strings.Contains(got, `"Mary"`) || !strings.Contains(got, "Smith") || strings.Count(got, "last_name") != 2 {
		t.Errorf("erasing John removed or changed a relative's sharer entry: %s", got)
	}
	if got := run(rivals, pay, true); strings.Contains(got, "Smith") {
		t.Errorf("exporting John handed over a relative's sharer entry: %s", got)
	}
	// With no relative and a surname that only one sharer has, the entry is the guest's own.
	alone := `{"last_name":"Smith","sharers":[{"last_name":"Smith"}]}`
	if got := run(none, alone, false); strings.Contains(got, "Smith") {
		t.Errorf("an unambiguous surname-only entry was kept: %s", got)
	}
	if got := run(none, alone, true); !strings.Contains(got, `"last_name": "Smith"`) {
		t.Errorf("an unambiguous surname-only entry was not exported: %s", got)
	}

	// A reservation with one named guest, whose PMS message lists the holder and someone else.
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-L2-"+sfx, "CHECKED_OUT")
	pmsEvent(t, f, ifaceOf(t, f, stay), stay,
		fmt.Sprintf(`{"reservation":"R-L2-%s","sharers":[{"external_guest_id":"G-R-L2-%s","first_name":"Ada","last_name":"Lovelace"},{"external_guest_id":"G-ZED","first_name":"Zed","last_name":"Zane"}]}`, sfx, sfx),
		false, 48*time.Hour)
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", stay, "guest asked"))
	if st != 200 || strings.Contains(raw, "Zane") || strings.Contains(raw, "G-ZED") {
		t.Fatalf("the holder's export lists another person's sharer entry: %d %s", st, raw)
	}
}

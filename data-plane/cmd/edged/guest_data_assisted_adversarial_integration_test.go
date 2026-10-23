//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func insertSession(t *testing.T, f *apiFixture, ent, dev, ip, startedAgo string, endedAgo string) string {
	t.Helper()
	var id string
	ended := "NULL"
	state := "active"
	reason := "NULL"
	if endedAgo != "" {
		ended = "now() - interval '" + endedAgo + "'"
		state = "ended"
		reason = "'TEST'"
	}
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO iam_v2.sessions
		(tenant_id, site_id, entitlement_id, device_id, ip, mac, state, credential_method, started, ended, end_reason, bytes_down)
		SELECT $1,$2,$3::uuid,$4::uuid,$5::inet,dv.mac,'`+state+`','PMS', now() - interval '`+startedAgo+`', `+ended+`, `+reason+`, 512
		  FROM iam_v2.devices dv WHERE dv.id=$4::uuid RETURNING id::text`, f.tenant, f.site, ent, dev, ip).Scan(&id); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return id
}

// A second reservation on the same PMS interface as the first (a returning guest, or the same room rebooked).
func secondStaySameInterface(t *testing.T, f *apiFixture, first, res string) string {
	t.Helper()
	ctx := context.Background()
	var stay string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(tenant_id,site_id,pms_interface_id,external_reservation_id,
			  external_stay_identity,normalized_room_number,status,arrival,departure,effective_checkout_at)
			SELECT tenant_id,site_id,pms_interface_id,$2,$2,'102','CHECKED_OUT',current_date - 3,current_date + 1, now() - interval '1 day'
			  FROM iam_v2.stays WHERE id=$1::uuid RETURNING id::text`, first, res).Scan(&stay); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,external_guest_id,
			  first_name_norm,last_name_norm,display_name,is_primary)
			SELECT tenant_id,site_id,pms_interface_id,id,'G-'||$2,'bruno','bianchi','Bruno Bianchi',true FROM iam_v2.stays WHERE id=$1::uuid`, stay, res)
		return err
	}); err != nil {
		t.Fatalf("second stay: %v", err)
	}
	return stay
}

func asstRows(t *testing.T, f *apiFixture, q string, args ...any) string {
	t.Helper()
	return gdStr(t, f, q, args...)
}

func TestIntegration_Assisted_AnAddressAlsoUsedUnderAnotherReservationIsNeverSelectable(t *testing.T) {
	f := newAPI(t)
	a := seedAsstRoom(t, f, "AS5A", asstMACs("3c:22:fb:50:00", 1), "10.90.5")
	b := seedAsstRoom(t, f, "AS5B", asstMACs("3c:22:fb:51:00", 1), "10.90.6")
	otherSess := onDevice(t, f, b.room, a.devs[0], "10.90.6.99", "PMS", false)

	id, ref := asstOpen(t, f, asstDefaults("ACCESS", a.macs, a.anchor()))
	rv := asstReviewOf(t, f, id)
	if got := asstClasses(rv, "session"); len(got) != 2 {
		t.Fatalf("both sessions are reviewed: %v", got)
	}
	if c := rv.find("session", a.sess[0]); c == nil || c.Class != "CONFLICTING" || c.Reason != "device_used_under_another_access" {
		t.Errorf("the requester's own room session on a phone also used under another reservation = %+v", c)
	}
	if c := rv.find("session", otherSess); c == nil || c.Class != "CONFLICTING" || c.Reason != "another_person_or_access" {
		t.Errorf("the other reservation's session = %+v", c)
	}
	if c := rv.of("device"); len(c) != 1 || c[0].Class != "CONFLICTING" {
		t.Errorf("the shared phone = %+v", c)
	}
	if len(rv.selectable()) != 0 {
		t.Fatalf("a shared phone has something selectable: %v", rv.selectable())
	}
	for _, s := range []string{a.sess[0], otherSess} {
		if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("session", s)}); st != 409 || !strings.Contains(raw, "not_eligible") {
			t.Errorf("export of a conflicting record = %d %s", st, raw)
		}
	}
	eid, eref := asstOpen(t, f, asstDefaults("ERASURE", a.macs, a.anchor()))
	erv := asstReviewOf(t, f, eid)
	if st, raw := asstAct(f, t, "erase", eid, erv, eref, []map[string]any{sel("session", a.sess[0]), sel("session", otherSess)}); st != 409 {
		t.Errorf("erase of conflicting records = %d %s", st, raw)
	}
	if got := sessAddr(t, f, otherSess); got != "10.90.6.99,"+a.macs[0] || devMAC(t, f, a.devs[0]) != a.macs[0] {
		t.Errorf("a refused erasure changed something: %s", got)
	}
}

// A returning guest adds the earlier reservation as a second context: then the address is theirs to claim.
func TestIntegration_Assisted_AReturningGuestStatesBothReservations(t *testing.T) {
	f := newAPI(t)
	a := seedAsstRoom(t, f, "AS6", asstMACs("3c:22:fb:60:00", 1), "10.90.7")
	stay2 := secondStaySameInterface(t, f, a.stay, "R-AS6B-"+fmt.Sprint(time.Now().UnixNano()))
	room2 := newAccess(t, f, "as6b"+fmt.Sprint(time.Now().UnixNano()), "stay_id", stay2, false)
	sess2 := onDevice(t, f, room2, a.devs[0], "10.90.7.50", "PMS", false)

	id1, _ := asstOpen(t, f, asstDefaults("ACCESS", a.macs, a.anchor()))
	if got := asstReviewOf(t, f, id1).selectable(); len(got) != 0 {
		t.Fatalf("with one of the two reservations stated nothing is selectable: %v", got)
	}
	both := append(a.anchor(), map[string]any{"kind": "stay", "id": stay2})
	id2, ref2 := asstOpen(t, f, asstDefaults("ACCESS", a.macs, both))
	rv := asstReviewOf(t, f, id2)
	for _, s := range []string{a.sess[0], sess2} {
		if c := rv.find("session", s); c == nil || c.Class != "CLAIMABLE" {
			t.Errorf("session %s with both reservations stated = %+v", s, c)
		}
	}
	if st, raw := asstAct(f, t, "export", id2, rv, ref2, rv.selectable()); st != 200 || !strings.Contains(raw, "10.90.7.50") {
		t.Errorf("export = %d %s", st, raw)
	}
	c := seedAsstRoom(t, f, "AS6C", asstMACs("3c:22:fb:61:00", 1), "10.90.8")
	o := asstDefaults("ACCESS", a.macs, append(a.anchor(), c.anchor()...))
	if st, body := f.do(t, "POST", "/guest-data-rights/assisted", o.body()); st != 400 || !strings.Contains(fmt.Sprint(body["message"]), "PMS interfaces") {
		t.Errorf("anchors from two PMS interfaces = %d %v", st, body)
	}
}

// A room move across PMS interfaces is the same stay continued: the walk the automatic request uses.
func TestIntegration_Assisted_ARoomMoveAcrossInterfacesKeepsTheSameContext(t *testing.T) {
	f := newAPI(t)
	a := seedAsstRoom(t, f, "AS7A", asstMACs("3c:22:fb:70:00", 1), "10.90.9")
	b := seedAsstRoom(t, f, "AS7B", nil, "10.90.10")
	sessB := onDevice(t, f, b.room, a.devs[0], "10.90.10.7", "PMS", false)
	id1, _ := asstOpen(t, f, asstDefaults("ACCESS", a.macs, a.anchor()))
	if got := asstReviewOf(t, f, id1).selectable(); len(got) != 0 {
		t.Fatalf("an unlinked second reservation on the address: %v", got)
	}
	linkStays(t, f, a.stay, b.stay)
	id2, _ := asstOpen(t, f, asstDefaults("ACCESS", a.macs, a.anchor()))
	rv := asstReviewOf(t, f, id2)
	for _, s := range []string{a.sess[0], sessB} {
		if c := rv.find("session", s); c == nil || c.Class != "CLAIMABLE" {
			t.Errorf("after the room move, session %s = %+v", s, c)
		}
	}
}

func TestIntegration_Assisted_AnotherPersonOnTheDeviceIsAConflict(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	r := seedAsstRoom(t, f, "AS8", asstMACs("3c:22:fb:80:00", 1), "10.90.11")
	otherEmail := "otto-" + sfx + "@example.com"
	var principal string
	if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Otto') RETURNING id)
		INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
		SELECT $1, gp.id, 'EMAIL', '', $2, now() - interval '9 days', '{}'::jsonb FROM gp RETURNING guest_principal_id::text`,
		f.tenant, otherEmail).Scan(&principal); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	// Someone else's identity remembered this phone.
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.principal_device_credentials
		  (tenant_id, site_id, guest_principal_id, device_id, method, secret_hmac, key_id, expires_at)
		VALUES ($1,$2,$3::uuid,$4::uuid,'OTP',public.gen_random_bytes(32),'k1', now() + interval '30 days')`, f.tenant, f.site, principal, r.devs[0]); err != nil {
		t.Fatalf("seed remembered device: %v", err)
	}
	id, ref := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	if c := rv.find("session", r.sess[0]); c == nil || c.Class != "CONFLICTING" || c.Reason != "device_used_by_another_person" {
		t.Fatalf("a phone another person's identity remembered = %+v", c)
	}
	if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("session", r.sess[0])}); st != 409 {
		t.Errorf("export = %d %s", st, raw)
	}
	// Stating that identity as part of the requester's own context (staff verified it is theirs) makes it theirs.
	anchors := append(r.anchor(), map[string]any{"kind": "principal", "id": principal})
	id2, _ := asstOpen(t, f, asstDefaults("ACCESS", r.macs, anchors))
	if c := asstReviewOf(t, f, id2).find("session", r.sess[0]); c == nil || c.Class != "CLAIMABLE" {
		t.Errorf("with the identity stated as theirs = %+v", c)
	}
}

func TestIntegration_Assisted_AVoucherAndOpenAccessAreSharedByNature(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "AS9", asstMACs("3c:22:fb:90:00", 1), "10.90.12")
	var voucher, pkgRev, pkg string
	if err := f.pool.QueryRow(ctx, `WITH sp AS (INSERT INTO iam_v2.service_plans (tenant_id, site_id, code) VALUES ($1,$2,'vplan') RETURNING id),
		spr AS (INSERT INTO iam_v2.service_plan_revisions (tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices)
		        SELECT $1,$2,sp.id,1,10000,5000,4 FROM sp RETURNING id),
		pk AS (INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code) VALUES ($1,$2,'vpkg') RETURNING id),
		pr AS (INSERT INTO iam_v2.internet_package_revisions (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type, settlement_methods)
		       SELECT $1,$2,pk.id,1,spr.id,'FREE_STAY',ARRAY['PREPAID'] FROM pk, spr RETURNING id, package_id)
		SELECT pr.id::text, pr.package_id::text FROM pr`, f.tenant, f.site).Scan(&pkgRev, &pkg); err != nil {
		t.Fatalf("seed voucher package: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1::uuid WHERE id=$2::uuid`, pkgRev, pkg); err != nil {
		t.Fatalf("make it current: %v", err)
	}
	var gen string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.voucher_code_key_generations(tenant_id,site_id,generation_no,hmac_key_ciphertext,aead_params,encryption_key_id)
		VALUES ($1,$2,1,public.gen_random_bytes(32),'{}'::jsonb,gen_random_uuid()) RETURNING id::text`, f.tenant, f.site).Scan(&gen); err != nil {
		t.Fatalf("seed voucher key generation: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.vouchers(tenant_id,site_id,package_revision_id,code_hmac,code_ciphertext,code_nonce,code_key_generation_id,code_last4)
		VALUES ($1,$2,$3::uuid,public.gen_random_bytes(32),public.gen_random_bytes(16),public.gen_random_bytes(12),$4::uuid,'4321')
		RETURNING id::text`, f.tenant, f.site, pkgRev, gen).Scan(&voucher); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	vEnt := newAccess(t, f, "voucher"+fmt.Sprint(time.Now().UnixNano()), "voucher_id", voucher, false)
	vSess := onDevice(t, f, vEnt, r.devs[0], "10.90.12.77", "VOUCHER", false)

	id, ref := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	if c := rv.find("session", vSess); c == nil || c.Class != "SHARED" || c.Reason != "voucher_or_open_access" {
		t.Errorf("a voucher session = %+v", c)
	}
	if c := rv.find("session", r.sess[0]); c == nil || c.Class != "CONFLICTING" {
		t.Errorf("the room session on a phone also used with a voucher = %+v", c)
	}
	if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("session", vSess)}); st != 409 {
		t.Errorf("a voucher session was selectable: %d %s", st, raw)
	}
}

func TestIntegration_Assisted_TheRequestersOwnAccountNeedsNoStatements(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	own := "3c:22:fb:a0:00:01"
	acct := selfAccount(t, f, "ownacct-"+sfx+"@example.com", true, "", own)
	ent := newAccess(t, f, "own"+sfx, "guest_account_id", acct, false)
	dev := newDevice(t, f, own)
	sess := onDevice(t, f, ent, dev, "10.90.13.1", "ACCOUNT", false)

	o := asstDefaults("ERASURE", []string{own}, []map[string]any{{"kind": "account", "id": acct}})
	o.device, o.soleUse = false, false // they are not needed for what is the person's by their own credential
	id, ref := asstOpen(t, f, o)
	rv := asstReviewOf(t, f, id)
	if c := rv.find("session", sess); c == nil || c.Class != "ATTRIBUTABLE" {
		t.Fatalf("the account's own session = %+v", c)
	}
	if st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable()); st != 200 {
		t.Fatalf("erase = %d %s", st, raw)
	}
	if got := sessAddr(t, f, sess); got != "" || !strings.HasPrefix(devMAC(t, f, dev), "03:") {
		t.Errorf("the account's own phone was not anonymised: %q %s", got, devMAC(t, f, dev))
	}
	// The account itself is the automatic request's, not this one's.
	if got := asstRows(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != "false" {
		t.Errorf("an assisted erasure anonymised the account: %s", got)
	}
}

func TestIntegration_Assisted_EventsTiedToTheAddressAreClassifiedOneByOne(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	r := seedAsstRoom(t, f, "AS10", asstMACs("3c:22:fb:b0:00", 1), "10.90.14")
	other := seedAsstRoom(t, f, "AS10B", nil, "10.90.15")
	mac := r.macs[0]
	var typed, stayOther string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id,site_id,occurred_at,result,verifier_kind,submitted_room,device_mac,device_ip)
		VALUES ($1,$2,now() - interval '4 days','CREDENTIAL_MISMATCH','UNKNOWN','101',$3::macaddr,'10.90.14.1') RETURNING id::text`,
		f.tenant, f.site, mac).Scan(&typed); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id,site_id,occurred_at,result,verifier_kind,submitted_room,device_mac,device_ip,matched_stay_id)
		VALUES ($1,$2,now() - interval '4 days','VERIFIED_NO_ELIGIBLE_PACKAGE','FULL_NAME','102',$3::macaddr,'10.90.14.2',$4::uuid) RETURNING id::text`,
		f.tenant, f.site, mac, other.stay).Scan(&stayOther); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	// Verification access from the requester's own one-time code and from someone else's, on the same address.
	var ownOTP, otherOTP, ownGrant, otherGrant string
	ownEmail, otherEmail := "own-"+sfx+"@example.com", "else-"+sfx+"@example.com"
	for _, x := range []struct {
		email string
		dst   *string
	}{{ownEmail, &ownOTP}, {otherEmail, &otherOTP}} {
		if err := f.pool.QueryRow(ctx, `INSERT INTO public.auth_otps(tenant_id,channel,destination,code_hash,issued_at,expires_at,ip)
			VALUES ($1,'email',$2,'h',now() - interval '4 days',now() - interval '4 days' + interval '10 minutes','10.90.14.9') RETURNING id::text`,
			f.tenant, x.email).Scan(x.dst); err != nil {
			t.Fatalf("seed otp: %v", err)
		}
	}
	for _, x := range []struct {
		otp string
		dst *string
		ago string
	}{{ownOTP, &ownGrant, "4 days"}, {otherOTP, &otherGrant, "5 days"}} {
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.verification_access_grants(tenant_id,site_id,device_mac,ip,bridge,challenge_id,destination_digest,
			  started_at,expires_at,data_cap_bytes,down_kbps,up_kbps,used_bytes,ended_at,end_reason)
			VALUES ($1,$2,$3::macaddr,'10.90.14.3','br0',$4::uuid,encode(public.gen_random_bytes(32),'hex'),
			  now() - $5::interval, now() - $5::interval + interval '20 minutes',1000,100,100,10, now() - $5::interval + interval '10 minutes','EXPIRED')
			RETURNING id::text`, f.tenant, f.site, mac, x.otp, x.ago).Scan(x.dst); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
	}
	var principal string
	if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Mine') RETURNING id)
		INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
		SELECT $1, gp.id, 'EMAIL', '', $2, now() - interval '9 days', '{}'::jsonb FROM gp RETURNING guest_principal_id::text`,
		f.tenant, ownEmail).Scan(&principal); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	var restr, redirect string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_signin_restrictions(tenant_id,site_id,device_mac,guest_network_name,last_submitted_room,restricted_at,expires_at,
		  failure_count,reason,released_at,released_by,release_reason)
		VALUES ($1,$2,$3::macaddr,'Guest','101',now() - interval '4 days',now() - interval '4 days' + interval '1 hour',5,'FAILED_CREDENTIAL_THRESHOLD',
		  now() - interval '4 days' + interval '30 minutes','op-1','released') RETURNING id::text`, f.tenant, f.site, mac).Scan(&restr); err != nil {
		t.Fatalf("seed restriction: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO public.social_oauth_states(state,tenant_id,provider,client_ip,client_mac,redirect_uri,user_agent,created_at,expires_at,consumed_at)
		VALUES ('st-'||$1,$2,'google','10.90.14.4',$3::macaddr,'https://example.invalid/cb','UA',now() - interval '4 days',now() - interval '4 days' + interval '10 minutes',
		  now() - interval '4 days' + interval '1 minute') RETURNING md5(state)::uuid::text`, sfx, f.tenant, mac).Scan(&redirect); err != nil {
		t.Fatalf("seed redirect: %v", err)
	}

	// Stating the room only: the address's own attempt and restriction and redirect are the requester's to claim;
	// the attempt that reached another reservation, and both verified addresses, are not.
	id, _ := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	// The address was also used with another reservation's credentials (the second attempt), so it is a conflict for
	// everything keyed only by the address, and the only thing claimable is nothing.
	for name, want := range map[string][3]string{
		"typed":      {"sign_in_attempt", typed, "CONFLICTING"},
		"other stay": {"sign_in_attempt", stayOther, "CONFLICTING"},
		"own grant":  {"verification_access", ownGrant, "CONFLICTING"},
		"else grant": {"verification_access", otherGrant, "CONFLICTING"},
		"restr":      {"sign_in_restriction", restr, "CONFLICTING"},
		"redirect":   {"sign_in_redirect", redirect, "CONFLICTING"},
	} {
		if c := rv.find(want[0], want[1]); c == nil || c.Class != want[2] {
			t.Errorf("%s = %+v, want %s", name, c, want[2])
		}
	}
	if len(rv.selectable()) != 0 {
		t.Errorf("selectable on an address another reservation's credentials were typed on: %v", rv.selectable())
	}

	// Without the attempt that reached another reservation, the address is clean for the room; the requester's own
	// verified address (stated as their identity) is attributable, the other person's is a conflict.
	if _, err := f.pool.Exec(ctx, `DELETE FROM iam_v2.sign_in_attempts WHERE id=$1::uuid`, stayOther); err != nil {
		t.Skipf("sign-in attempts cannot be removed in this schema (%v)", err)
	}
	anchors := append(r.anchor(), map[string]any{"kind": "principal", "id": principal})
	id2, ref2 := asstOpen(t, f, asstDefaults("ACCESS", r.macs, anchors))
	rv2 := asstReviewOf(t, f, id2)
	// Another person's verified address on the device is a conflict for the whole address: nothing that is only
	// claimed (the room's, or keyed by the address) is selectable. What is the requester's own by their address is.
	for _, x := range rv2.Records {
		if x.Selectable && x.Class != "ATTRIBUTABLE" {
			t.Errorf("%s %s (%s) selectable while another person verified an address on the device", x.Kind, x.ID, x.Class)
		}
	}
	if c := rv2.find("verification_access", ownGrant); c == nil || c.Class != "ATTRIBUTABLE" || !c.Selectable {
		t.Errorf("the requester's own verified address = %+v", c)
	}
	if c := rv2.find("verification_access", otherGrant); c == nil || c.Class != "CONFLICTING" {
		t.Errorf("another person's verified address = %+v", c)
	}
	// The own record can be handed over alone; the other person's grant on the same address stays out.
	st, raw := asstAct(f, t, "export", id2, rv2, ref2, []map[string]any{sel("verification_access", ownGrant)})
	if st != 200 || strings.Count(raw, `"device_mac_address"`) != 1 {
		t.Errorf("hand-over of the requester's own verified access = %d %s", st, raw)
	}
	if st, raw := asstAct(f, t, "export", id2, rv2, ref2, []map[string]any{sel("verification_access", otherGrant)}); st != 409 {
		t.Errorf("hand-over of another person's verified access = %d %s", st, raw)
	}
}

func TestIntegration_Assisted_ARecordOutsideTheRangeIsNeverTouched(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS11", asstMACs("3c:22:fb:c0:00", 1), "10.90.16")
	early := insertSession(t, f, r.room, r.devs[0], "10.90.16.50", "7 days", "5 days")
	id, ref := asstOpen(t, f, asstDefaults("ERASURE", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	if c := rv.find("session", early); c == nil || c.Class != "UNVERIFIABLE" || c.Reason != "straddles_range" {
		t.Errorf("a session that began before the range = %+v", c)
	}
	if c := rv.of("device"); len(c) != 1 || c[0].Class != "UNVERIFIABLE" || c[0].Reason != "device_has_records_outside_the_request" {
		t.Errorf("a device with a record outside the range = %+v", c)
	}
	if c := rv.find("session", r.sess[0]); c == nil || c.Class != "CLAIMABLE" {
		t.Fatalf("the session inside = %+v", c)
	}
	if st, raw := asstAct(f, t, "erase", id, rv, ref, []map[string]any{sel("session", early)}); st != 409 {
		t.Errorf("erase of a record outside the range = %d %s", st, raw)
	}
	if st, raw := asstAct(f, t, "erase", id, rv, ref, []map[string]any{sel("session", r.sess[0])}); st != 200 {
		t.Fatalf("erase of the session inside = %d %s", st, raw)
	}
	if got := sessAddr(t, f, r.sess[0]); got != "" {
		t.Errorf("the session inside kept its address: %s", got)
	}
	if got := sessAddr(t, f, early); got != "10.90.16.50,"+r.macs[0] {
		t.Errorf("the session outside the range changed: %s", got)
	}
	if got := devMAC(t, f, r.devs[0]); got != r.macs[0] {
		t.Errorf("a device with a record outside the range was anonymised: %s", got)
	}
}

func TestIntegration_Assisted_RecentAndLiveRecordsAreNotErasedButMayBeHandedOver(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-AS12-"+sfx, "IN_HOUSE")
	room := newAccess(t, f, "as12"+sfx, "stay_id", stay, true)
	mac := "3c:22:fb:d0:00:01"
	dev := newDevice(t, f, mac)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO iam_v2.entitlement_devices(tenant_id,site_id,entitlement_id,device_id) VALUES ($1,$2,$3::uuid,$4::uuid)`,
		f.tenant, f.site, room, dev); err != nil {
		t.Fatal(err)
	}
	recent := insertSession(t, f, room, dev, "10.90.17.1", "3 hours", "2 hours")
	live := insertSession(t, f, room, dev, "10.90.17.2", "1 hour", "")
	anchors := []map[string]any{{"kind": "stay", "id": stay}}
	rng := func(o *asstOpts) {
		o.to = time.Now().Add(-30 * time.Minute)
		o.from = time.Now().Add(-6 * 24 * time.Hour)
	}

	// Handing records over is a read: allowed for a recent record, never for a live session.
	oa := asstDefaults("ACCESS", []string{mac}, anchors)
	rng(&oa)
	ida, refa := asstOpen(t, f, oa)
	rva := asstReviewOf(t, f, ida)
	if c := rva.find("session", live); c == nil || c.Class != "UNVERIFIABLE" {
		t.Errorf("a live session = %+v", c)
	}
	if c := rva.find("session", recent); c == nil || c.Class != "CLAIMABLE" || !c.Selectable {
		t.Errorf("a recent session for a hand-over = %+v", c)
	}
	if st, raw := asstAct(f, t, "export", ida, rva, refa, []map[string]any{sel("session", recent)}); st != 200 {
		t.Errorf("hand-over of a recent record = %d %s", st, raw)
	}

	// Erasing it is refused: the sign-in protection may still count it, and the guest is in the house.
	oe := asstDefaults("ERASURE", []string{mac}, anchors)
	rng(&oe)
	ide, refe := asstOpen(t, f, oe)
	rve := asstReviewOf(t, f, ide)
	if c := rve.find("session", recent); c == nil || c.Selectable || c.Block == nil || *c.Block != "recent" {
		t.Errorf("a recent session for an erasure = %+v", c)
	}
	if c := rve.of("device"); len(c) != 1 || c[0].Block == nil || *c[0].Block != "live" {
		t.Errorf("a device linked to a live entitlement = %+v", c)
	}
	if st, raw := asstAct(f, t, "erase", ide, rve, refe, []map[string]any{sel("session", recent)}); st != 409 || !strings.Contains(raw, "blocked_") {
		t.Errorf("erase of a recent record = %d %s", st, raw)
	}
	if got := sessAddr(t, f, recent); got != "10.90.17.1,"+mac || devMAC(t, f, dev) != mac {
		t.Errorf("a refused erasure changed something: %s", got)
	}
	if got := asstRows(t, f, `SELECT status FROM iam_v2.entitlements WHERE id=$1`, room); got != "ACTIVE" {
		t.Errorf("the live entitlement changed: %s", got)
	}
}

func TestIntegration_Assisted_ARecordThatAppearedAfterTheReviewStopsTheErasure(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS13", asstMACs("3c:22:fb:e0:00", 1), "10.90.18")
	other := seedAsstRoom(t, f, "AS13B", nil, "10.90.19")
	id, ref := asstOpen(t, f, asstDefaults("ERASURE", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	if len(rv.selectable()) != 2 {
		t.Fatalf("expected the session and the device to be selectable: %v", rv.selectable())
	}
	// After the review, another reservation's guest uses the same phone.
	late := onDevice(t, f, other.room, r.devs[0], "10.90.19.5", "PMS", false)
	if st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable()); st != 409 || !strings.Contains(raw, "changed") {
		t.Fatalf("erase after the review changed = %d %s", st, raw)
	}
	if got := sessAddr(t, f, r.sess[0]); got != r.ips[0]+","+r.macs[0] {
		t.Errorf("a stale review changed a record: %s", got)
	}
	if got := sessAddr(t, f, late); got != "10.90.19.5,"+r.macs[0] {
		t.Errorf("the late session changed: %s", got)
	}
	if got := asstStatus(t, f, id)["status"]; got != "OPEN" {
		t.Errorf("a refused erasure must leave the request open: %v", got)
	}
	if c := asstReviewOf(t, f, id).find("session", r.sess[0]); c == nil || c.Class != "CONFLICTING" {
		t.Errorf("the fresh review = %+v", c)
	}
}

// A device is selected only with every record it carries.
func TestIntegration_Assisted_ADeviceIsNotSelectableWithoutItsRecords(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS14", asstMACs("3c:22:fb:f0:00", 1), "10.90.20")
	second := insertSession(t, f, r.room, r.devs[0], "10.90.20.40", "2900 minutes", "2800 minutes")
	id, ref := asstOpen(t, f, asstDefaults("ERASURE", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	if st, raw := asstAct(f, t, "erase", id, rv, ref, []map[string]any{sel("device", r.devs[0]), sel("session", r.sess[0])}); st != 409 || !strings.Contains(raw, "device_incomplete") {
		t.Errorf("a device without one of its sessions = %d %s", st, raw)
	}
	if got := devMAC(t, f, r.devs[0]); got != r.macs[0] {
		t.Errorf("the device changed: %s", got)
	}
	if st, raw := asstAct(f, t, "erase", id, rv, ref, []map[string]any{sel("device", r.devs[0]), sel("session", r.sess[0]), sel("session", second)}); st != 200 {
		t.Fatalf("device with all its sessions = %d %s", st, raw)
	}
	if !strings.HasPrefix(devMAC(t, f, r.devs[0]), "03:") || sessAddr(t, f, second) != "" {
		t.Error("the whole device was not anonymised")
	}
}

func TestIntegration_Assisted_TwoErasuresAtOnceRunOnce(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS15", asstMACs("3c:22:fb:15:00", 1), "10.90.21")
	// The same request run twice, and a second request on the same address.
	id1, ref1 := asstOpen(t, f, asstDefaults("ERASURE", r.macs, r.anchor()))
	id2, ref2 := asstOpen(t, f, asstDefaults("ERASURE", r.macs, r.anchor()))
	rv1, rv2 := asstReviewOf(t, f, id1), asstReviewOf(t, f, id2)
	var wg sync.WaitGroup
	codes := make([]int, 3)
	run := func(i int, id string, rv asstReview, ref string) {
		defer wg.Done()
		st, _ := asstAct(f, t, "erase", id, rv, ref, rv.selectable())
		codes[i] = st
	}
	wg.Add(3)
	go run(0, id1, rv1, ref1)
	go run(1, id1, rv1, ref1)
	go run(2, id2, rv2, ref2)
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case 409:
			conflict++
		default:
			t.Errorf("unexpected status %v", codes)
		}
	}
	if ok != 1 || conflict != 2 {
		t.Errorf("exactly one of three concurrent erasures of the same records must run: %v", codes)
	}
	if got := asstRows(t, f, `SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE subject_kind='assisted' AND tenant_id=$1`, f.tenant); got != "1" {
		t.Errorf("erasures recorded: %s", got)
	}
}

func TestIntegration_Assisted_OnlyTheDataRightsRolesAndOnlyValidRequests(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS16", asstMACs("3c:22:fb:16:00", 1), "10.90.22")
	for _, role := range []string{"front_office_operator", "guest_relations_operator", "site_viewer"} {
		g := newAPIIn(t, f.tenant, role)
		for _, c := range []struct{ m, p string }{{"GET", "/guest-data-rights/assisted"}, {"POST", "/guest-data-rights/assisted"}} {
			if st, _ := g.do(t, c.m, c.p, asstDefaults("ACCESS", r.macs, r.anchor()).body()); st != 403 {
				t.Errorf("%s %s as %s = %d, want 403", c.m, c.p, role, st)
			}
		}
	}
	if st, _ := newAPIIn(t, f.tenant, "hotel_it_manager").do(t, "GET", "/guest-data-rights/assisted", nil); st != 200 {
		t.Errorf("the IT manager cannot list: %d", st)
	}
	bad := map[string]func(*asstOpts){
		"no anchor":          func(o *asstOpts) { o.anchors = nil },
		"no address":         func(o *asstOpts) { o.macs = nil },
		"six addresses":      func(o *asstOpts) { o.macs = asstMACs("3c:22:fb:99:00", 6) },
		"not an address":     func(o *asstOpts) { o.macs = []string{"not-a-mac"} },
		"a group address":    func(o *asstOpts) { o.macs = []string{"01:00:5e:00:00:01"} },
		"future range":       func(o *asstOpts) { o.to = time.Now().Add(48 * time.Hour) },
		"two year range":     func(o *asstOpts) { o.from = time.Now().Add(-800 * 24 * time.Hour) },
		"reversed range":     func(o *asstOpts) { o.from, o.to = o.to, o.from },
		"unknown purpose":    func(o *asstOpts) { o.purpose = "DELETE" },
		"no reason":          func(o *asstOpts) { o.reason = "" },
		"no method":          func(o *asstOpts) { o.method = "" },
		"no requester":       func(o *asstOpts) { o.requester = "" },
		"short evidence":     func(o *asstOpts) { o.evidence = "ok" },
		"reason names guest": func(o *asstOpts) { o.reason = "Ada Lovelace asked at the desk" },
		"reason has address": func(o *asstOpts) { o.reason = "phone " + r.macs[0] + " was shown" },
		"reason has email":   func(o *asstOpts) { o.reason = "wrote from ada@example.com" },
		"unknown kind":       func(o *asstOpts) { o.anchors = []map[string]any{{"kind": "device", "id": r.devs[0]}} },
	}
	for name, mod := range bad {
		o := asstDefaults("ACCESS", r.macs, r.anchor())
		mod(&o)
		if st, body := f.do(t, "POST", "/guest-data-rights/assisted", o.body()); st != 400 {
			t.Errorf("%s: status %d %v, want 400", name, st, body)
		}
	}
	o := asstDefaults("ACCESS", r.macs, []map[string]any{{"kind": "stay", "id": "00000000-0000-4000-8000-000000000000"}})
	if st, _ := f.do(t, "POST", "/guest-data-rights/assisted", o.body()); st != 404 {
		t.Errorf("an anchor that does not exist = %d, want 404", st)
	}
	if st, _ := f.do(t, "GET", "/guest-data-rights/assisted/00000000-0000-4000-8000-000000000000", nil); st != 404 {
		t.Errorf("a request that does not exist = %d", st)
	}
	if st, _ := f.do(t, "GET", "/guest-data-rights/assisted/not-an-id/review", nil); st != 400 {
		t.Errorf("a malformed id = %d", st)
	}
}

// Nothing crosses to another site of the same customer.
func TestIntegration_Assisted_AnotherSiteSeesNothingOfThisOne(t *testing.T) {
	f := newAPI(t)
	g := newAPIIn(t, f.tenant)
	r := seedAsstRoom(t, f, "AS17", asstMACs("3c:22:fb:17:00", 1), "10.90.23")
	mine := seedAsstRoom(t, g, "AS17G", nil, "10.90.24")
	id, ref := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))

	// The other site cannot read, review, hand over or close this site's request.
	for _, c := range []struct{ m, p string }{{"GET", "/guest-data-rights/assisted/" + id}, {"GET", "/guest-data-rights/assisted/" + id + "/review"}} {
		if st, _ := g.do(t, c.m, c.p, nil); st != 404 {
			t.Errorf("%s %s from another site = %d, want 404", c.m, c.p, st)
		}
	}
	if st, _ := g.do(t, "POST", "/guest-data-rights/assisted/"+id+"/close", map[string]any{"outcome": "CANCELLED", "reason": "tidy up"}); st != 404 {
		t.Errorf("close from another site = %d", st)
	}
	// It cannot name this site's reservation, and the same address at its own site finds nothing of this site's.
	o := asstDefaults("ACCESS", r.macs, r.anchor())
	if st, _ := g.do(t, "POST", "/guest-data-rights/assisted", o.body()); st != 404 {
		t.Errorf("another site's reservation as an anchor = %d, want 404", st)
	}
	gid, _ := asstOpen(t, g, asstDefaults("ACCESS", r.macs, mine.anchor()))
	if rv := asstReviewOf(t, g, gid); len(rv.Records) != 0 {
		t.Errorf("another site's review of this site's address holds %d records", len(rv.Records))
	}
	st, body := g.do(t, "GET", "/guest-data-rights/assisted", nil)
	if list, _ := body["requests"].([]any); st != 200 || len(list) != 1 {
		t.Errorf("another site's list = %d %v", st, body)
	}
	_ = ref
}

func TestIntegration_Assisted_AStaleRequestExpiresAndIsScrubbed(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS18", asstMACs("3c:22:fb:18:00", 1), "10.90.25")
	id, _ := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	for _, q := range []string{
		`ALTER TABLE iam_v2.assisted_guest_requests DISABLE TRIGGER assisted_guest_requests_guard`,
		`UPDATE iam_v2.assisted_guest_requests SET created_at = now() - interval '31 days' WHERE id='` + id + `'::uuid`,
		`ALTER TABLE iam_v2.assisted_guest_requests ENABLE TRIGGER assisted_guest_requests_guard`,
	} {
		if _, err := f.pool.Exec(context.Background(), q); err != nil {
			t.Fatalf("age the request: %v", err)
		}
	}
	var out string
	if err := f.pool.QueryRow(context.Background(), `SELECT public.run_staff_retention($1::uuid,$2::uuid)::text`, f.tenant, f.site).Scan(&out); err != nil || !strings.Contains(out, `"assisted_requests_expired": 1`) {
		t.Fatalf("the retention run: %v %s", err, out)
	}
	got := asstStatus(t, f, id)
	if got["status"] != "CANCELLED" || got["addresses"] != nil || got["requester"] != "[removed]" || got["closed_by"] != "system" {
		t.Errorf("an expired request must be cancelled and scrubbed: %v", got)
	}
}

func TestIntegration_Assisted_TheRequestAndItsHistoryCannotBeEditedOrDeleted(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "AS19", asstMACs("3c:22:fb:19:00", 1), "10.90.26")
	id, _ := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	for name, q := range map[string]string{
		"change the purpose": `UPDATE iam_v2.assisted_guest_requests SET purpose='ERASURE' WHERE id=$1::uuid`,
		"change the range":   `UPDATE iam_v2.assisted_guest_requests SET range_from = range_from - interval '1 day' WHERE id=$1::uuid`,
		"unverify":           `UPDATE iam_v2.assisted_guest_requests SET attest_device = NOT attest_device WHERE id=$1::uuid`,
		"delete the request": `DELETE FROM iam_v2.assisted_guest_requests WHERE id=$1::uuid`,
		"edit an event":      `UPDATE iam_v2.assisted_guest_request_events SET actor='someone' WHERE request_id=$1::uuid`,
		"delete an event":    `DELETE FROM iam_v2.assisted_guest_request_events WHERE request_id=$1::uuid`,
	} {
		if _, err := f.pool.Exec(ctx, q, id); err == nil {
			t.Errorf("%s was allowed", name)
		}
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/assisted/"+id+"/close", map[string]any{"outcome": "COMPLETED", "reason": "nothing was handed over"}); st != 409 {
		t.Errorf("completing an access request nothing was handed over for = %d, want 409", st)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/assisted/"+id+"/close", map[string]any{"outcome": "CANCELLED", "reason": "the requester withdrew"}); st != 200 {
		t.Fatalf("cancel = %d", st)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.assisted_guest_requests SET close_reason='x' WHERE id=$1::uuid`, id); err == nil {
		t.Error("a closed request was edited")
	}
}

func TestIntegration_Assisted_TheRuntimeRoleReachesOnlyTheFunctions(t *testing.T) {
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
		`SELECT 1 FROM iam_v2.assisted_guest_requests`,
		`SELECT 1 FROM iam_v2.assisted_guest_request_records`,
		`SELECT 1 FROM iam_v2.assisted_guest_request_events`,
		`SELECT 1 FROM iam_v2.assisted_guest_request_anchors`,
		`INSERT INTO iam_v2.assisted_guest_requests(ref) VALUES ('AR-0000000000')`,
		`SELECT iam_v2.assisted_request_erase_run(gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),'x','xxxx','x','x','[]'::jsonb)`,
		`SELECT iam_v2.assisted_request_export_doc(gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),'x','xxxx','x','x','[]'::jsonb)`,
		`SELECT iam_v2.assisted_validate_selection(gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),'x','[]'::jsonb,false)`,
		`SELECT iam_v2.assisted_request_open(gen_random_uuid(),gen_random_uuid(),'x','ACCESS',ARRAY['3c:22:fb:00:00:01'],now()-interval '1 day',now(),'[]'::jsonb,'xx','OTHER_DOCUMENTED','xxxxxxxxxx',true,true,true,false,'xxxx')`,
		`SELECT iam_v2.assisted_request_review(gen_random_uuid(),gen_random_uuid(),gen_random_uuid())`,
	} {
		if _, err := ep.Exec(ctx, q); err == nil {
			t.Errorf("svc_edged could run: %s", q)
		}
	}
	for _, q := range []string{
		`SELECT iam_v2.assisted_request_list(gen_random_uuid(),gen_random_uuid())`,
		`SELECT public.assisted_reason_check(gen_random_uuid(),gen_random_uuid(),'[]'::jsonb,'a fine reason')`,
	} {
		if _, err := ep.Exec(ctx, q); err != nil {
			t.Errorf("svc_edged cannot run %q: %v", q, err)
		}
	}
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS20", asstMACs("3c:22:fb:20:20", 1), "10.90.27")
	// ACCESS, then ERASURE, end to end as the runtime role: open, review, hand over, close; open, review, anonymise.
	for _, purpose := range []string{"ACCESS", "ERASURE"} {
		var opened struct {
			ID  string `json:"id"`
			Ref string `json:"ref"`
		}
		if err := ep.QueryRow(ctx, `SELECT public.assisted_request_open($1::uuid,$2::uuid,'op',$5,$3::text[],now()-interval '6 days',now()-interval '1 day',
			jsonb_build_array(jsonb_build_object('kind','stay','id',$4::text)),'Ms Example','OTHER_DOCUMENTED','a documented check',true,true,true,false,'asked at the desk')`,
			f.tenant, f.site, r.macs, r.stay, purpose).Scan(&opened); err != nil || opened.Ref == "" {
			t.Fatalf("svc_edged cannot open a %s request through the wrapper: %v", purpose, err)
		}
		var rv asstReview
		if err := ep.QueryRow(ctx, `SELECT iam_v2.assisted_request_review_json($1::uuid,$2::uuid,$3::uuid)`, f.tenant, f.site, opened.ID).Scan(&rv); err != nil || len(rv.selectable()) != 2 {
			t.Fatalf("svc_edged cannot review a %s request: %v %+v", purpose, err, rv.Records)
		}
		sel, _ := json.Marshal(rv.selectable())
		if _, err := ep.Exec(ctx, `SELECT iam_v2.assisted_request_get($1::uuid,$2::uuid,$3::uuid)`, f.tenant, f.site, opened.ID); err != nil {
			t.Fatalf("svc_edged cannot read the request: %v", err)
		}
		if purpose == "ACCESS" {
			var out string
			if err := ep.QueryRow(ctx, `SELECT public.assisted_request_export($1::uuid,$2::uuid,$3::uuid,'op','asked at the desk',$4,$5,$6::jsonb)::text`,
				f.tenant, f.site, opened.ID, opened.Ref, rv.Signature, string(sel)).Scan(&out); err != nil || !strings.Contains(out, r.macs[0]) {
				t.Fatalf("svc_edged cannot hand over through the wrapper: %v %s", err, out)
			}
			if _, err := ep.Exec(ctx, `SELECT public.assisted_request_close($1::uuid,$2::uuid,$3::uuid,'op','COMPLETED','handed over in person')`, f.tenant, f.site, opened.ID); err != nil {
				t.Fatalf("svc_edged cannot close: %v", err)
			}
		} else {
			var out string
			if err := ep.QueryRow(ctx, `SELECT public.assisted_request_erase($1::uuid,$2::uuid,$3::uuid,'op','asked at the desk',$4,$5,$6::jsonb)::text`,
				f.tenant, f.site, opened.ID, opened.Ref, rv.Signature, string(sel)).Scan(&out); err != nil || !strings.Contains(out, `"erasure_ref"`) {
				t.Fatalf("svc_edged cannot anonymise through the wrapper: %v %s", err, out)
			}
			if got := devMAC(t, f, r.devs[0]); !strings.HasPrefix(got, "03:") {
				t.Errorf("the erasure run as svc_edged did not anonymise the device: %s", got)
			}
		}
	}
}

func TestIntegration_Assisted_TheActivityLogNamesNoAddressAndNoPerson(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS21", asstMACs("3c:22:fb:21:00", 1), "10.90.28")
	id, ref := asstOpen(t, f, asstDefaults("ERASURE", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	if st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable()); st != 200 {
		t.Fatalf("erase = %d %s", st, raw)
	}
	rows, err := f.pool.Query(context.Background(), `SELECT action, payload::text FROM public.audit_log WHERE tenant_id=$1 AND action LIKE 'guest_data.assisted_%' ORDER BY ts`, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var action, payload string
		if err := rows.Scan(&action, &payload); err != nil {
			t.Fatal(err)
		}
		seen[action] = true
		for _, leak := range []string{r.macs[0], r.ips[0], "Lovelace", "Ms R. Example", "identity card"} {
			if strings.Contains(payload, leak) {
				t.Errorf("%s carries %q: %s", action, leak, payload)
			}
		}
	}
	for _, want := range []string{"guest_data.assisted_opened", "guest_data.assisted_reviewed", "guest_data.assisted_erased"} {
		if !seen[want] {
			t.Errorf("no %s entry in the activity log (have %v)", want, seen)
		}
	}
	_ = http.StatusOK
}

// A credential staff issued is shared by whoever was given it: a phone that used it can be claimed on the statements,
// and only that phone's records.
func TestIntegration_Assisted_AStaffIssuedCredentialIsClaimedPhoneByPhone(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	acct := selfAccount(t, f, "conference-"+sfx, false, "", "")
	ent := newAccess(t, f, "conf"+sfx, "guest_account_id", acct, false)
	macs := asstMACs("3c:22:fb:c1:00", 2)
	var sess []string
	for i, m := range macs {
		d := newDevice(t, f, m)
		sess = append(sess, onDevice(t, f, ent, d, fmt.Sprintf("10.90.30.%d", i+1), "ACCOUNT", false))
	}
	id, ref := asstOpen(t, f, asstDefaults("ACCESS", macs[:1], []map[string]any{{"kind": "account", "id": acct}}))
	rv := asstReviewOf(t, f, id)
	if c := rv.find("session", sess[0]); c == nil || c.Class != "CLAIMABLE" {
		t.Fatalf("the claimed phone's session on a shared credential = %+v", c)
	}
	if rv.find("session", sess[1]) != nil {
		t.Error("another phone's session on the same credential is part of the review")
	}
	st, raw := asstAct(f, t, "export", id, rv, ref, rv.selectable())
	if st != 200 || strings.Contains(raw, macs[1]) || strings.Contains(raw, "10.90.30.2") || !strings.Contains(raw, "10.90.30.1") {
		t.Errorf("hand-over = %d %s", st, raw)
	}
	o := asstDefaults("ACCESS", macs[:1], []map[string]any{{"kind": "account", "id": acct}})
	o.soleUse = false
	id2, _ := asstOpen(t, f, o)
	if c := asstReviewOf(t, f, id2).find("session", sess[0]); c == nil || c.Class != "SHARED" {
		t.Errorf("without the statements = %+v", c)
	}
}

// A reservation that names more than one guest shares its room between identified people: a statement cannot say
// whose phone is whose, so its room access is never claimable -- directly, as a named guest, or through a stay link.
func TestIntegration_Assisted_ARoomSharedByNamedGuestsIsNeverClaimable(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS23", asstMACs("3c:22:fb:23:00", 1), "10.90.31")
	ada := guestRow(t, f, r.stay, "G-ADA", "ada", "lovelace")
	addGuest(t, f, r.stay, "G-BOB", "bob", "byron")
	for name, anchors := range map[string][]map[string]any{
		"as the named guest": {{"kind": "stay_guest", "id": ada}},
		"as the reservation": r.anchor(),
	} {
		id, ref := asstOpen(t, f, asstDefaults("ACCESS", r.macs, anchors))
		rv := asstReviewOf(t, f, id)
		if c := rv.find("session", r.sess[0]); c == nil || c.Class != "SHARED" || c.Reason != "room_shared_by_named_guests" {
			t.Errorf("%s: the session on a room shared by two named guests = %+v", name, c)
		}
		if len(rv.selectable()) != 0 {
			t.Errorf("%s: selectable: %v", name, rv.selectable())
		}
		if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("session", r.sess[0])}); st != 409 {
			t.Errorf("%s: hand-over = %d %s", name, st, raw)
		}
	}
	// Bob's phone, used only under the shared room, is not claimable through a one-guest stay linked to it.
	a := seedAsstRoom(t, f, "AS23A", nil, "10.90.32")
	linkStays(t, f, a.stay, r.stay)
	id, _ := asstOpen(t, f, asstDefaults("ACCESS", r.macs, a.anchor()))
	if len(asstReviewOf(t, f, id).selectable()) != 0 {
		t.Error("another guest's phone became claimable through a stay link to the shared reservation")
	}
}

// The address on an account the person registered goes with the device.
func TestIntegration_Assisted_TheAccountRegisteredOnAnAnonymisedDeviceLosesItsAddress(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	mac := "3c:22:fb:24:00:01"
	acct := selfAccount(t, f, "reg-"+sfx+"@example.com", true, "", mac)
	ent := newAccess(t, f, "reg"+sfx, "guest_account_id", acct, false)
	dev := newDevice(t, f, mac)
	onDevice(t, f, ent, dev, "10.90.33.1", "ACCOUNT", false)
	id, ref := asstOpen(t, f, asstDefaults("ERASURE", []string{mac}, []map[string]any{{"kind": "account", "id": acct}}))
	rv := asstReviewOf(t, f, id)
	if st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable()); st != 200 {
		t.Fatalf("erase = %d %s", st, raw)
	}
	if got := gdStr(t, f, `SELECT COALESCE(created_by_device_mac::text,'none') FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != "none" {
		t.Errorf("the account kept the address it was registered on: %s", got)
	}
}

// A reason carries no address in any spelling, and no IP address.
func TestIntegration_Assisted_AReasonCarriesNoAddressInAnySpelling(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS25", asstMACs("3c:22:fb:25:00", 1), "10.90.34")
	for _, reason := range []string{
		"phone 3c:22:fb:25:00:01 shown", "phone 3c-22-fb-25-00-01 shown", "phone 3c.22.fb.25.00.01 shown", "phone 3c 22 fb 25 00 01 shown",
		"phone 3c22.fb25.0001 shown", "phone 3c22fb250001 shown", "from 10.66.0.11 at the desk",
	} {
		o := asstDefaults("ACCESS", r.macs, r.anchor())
		o.reason = reason
		if st, _ := f.do(t, "POST", "/guest-data-rights/assisted", o.body()); st != 400 {
			t.Errorf("reason %q was accepted (%d)", reason, st)
		}
	}
	o := asstDefaults("ACCESS", r.macs, r.anchor())
	o.reason = "written request of 2026-10-20 received at 15:30"
	if st, body := f.do(t, "POST", "/guest-data-rights/assisted", o.body()); st != 201 {
		t.Errorf("an ordinary reason with a date and a time was refused: %d %v", st, body)
	}
}

// A body is bounded.
func TestIntegration_Assisted_AnOversizedBodyIsRefused(t *testing.T) {
	f := newAPI(t)
	big := strings.Repeat("x", 1<<20)
	st, _ := f.doRaw(t, "POST", "/guest-data-rights/assisted", map[string]any{"purpose": "ACCESS", "reason": big})
	if st != 400 && st != 413 {
		t.Errorf("a megabyte body = %d", st)
	}
}

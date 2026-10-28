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

func guestRow(t *testing.T, f *apiFixture, stay, ext, first, last string) string {
	t.Helper()
	var id string
	if err := controlled(context.Background(), f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `UPDATE iam_v2.stay_guests SET external_guest_id=NULLIF($2,''),
			  first_name_norm=$3, last_name_norm=$4, display_name=initcap($3||' '||$4)
			WHERE stay_id=$1::uuid RETURNING id::text`, stay, ext, first, last).Scan(&id)
	}); err != nil {
		t.Fatalf("set guest row: %v", err)
	}
	return id
}

func addGuest(t *testing.T, f *apiFixture, stay, ext, first, last string) string {
	t.Helper()
	var id string
	if err := controlled(context.Background(), f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,
			  external_guest_id,first_name_norm,last_name_norm,display_name,is_primary)
			SELECT tenant_id,site_id,pms_interface_id,id,NULLIF($2,''),$3,$4,initcap($3||' '||$4),false FROM iam_v2.stays WHERE id=$1
			RETURNING id::text`, stay, ext, first, last).Scan(&id)
	}); err != nil {
		t.Fatalf("add guest: %v", err)
	}
	return id
}

func linkStays(t *testing.T, f *apiFixture, from, to string) {
	t.Helper()
	if err := controlled(context.Background(), f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `SELECT iam_v2.p5_begin_controlled_operation('entitlement_transfer')`); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `INSERT INTO iam_v2.stay_links(tenant_id,site_id,from_stay,to_stay,reason)
			VALUES ($1,$2,$3::uuid,$4::uuid,'CROSS_PMS_TRANSFER')`, f.tenant, f.site, from, to)
		return err
	}); err != nil {
		t.Fatalf("link stays: %v", err)
	}
}

func gdReq(f *apiFixture, kind, id, reason string) map[string]any {
	return map[string]any{"kind": kind, "id": id, "reason": reason, "password": f.password}
}

func gdStr(t *testing.T, f *apiFixture, q string, args ...any) string {
	t.Helper()
	var v string
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// X alone in S1 moves into S2, shared with Y; Y later moves on to S3, alone. Erasing X by S1 must not pass
// through S2 into Y's S3, and must not touch Y's row on S2.
func TestIntegration_GuestDataIsolation_AWalkStopsAtASharedStay(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	s1 := seedGuestDataStay(t, f, "R-W1-"+sfx, "CHECKED_OUT")
	s2 := seedGuestDataStay(t, f, "R-W2-"+sfx, "CHECKED_OUT")
	s3 := seedGuestDataStay(t, f, "R-W3-"+sfx, "CHECKED_OUT")
	guestRow(t, f, s1, "G-X-"+sfx, "XAVIER", "XU")
	xOnS2 := guestRow(t, f, s2, "G-X-"+sfx, "XAVIER", "XU")
	yOnS2 := addGuest(t, f, s2, "G-Y-"+sfx, "YARA", "YOUNG")
	yOnS3 := guestRow(t, f, s3, "G-Y-"+sfx, "YARA", "YOUNG")
	linkStays(t, f, s1, s2)
	linkStays(t, f, s2, s3)

	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", s1, "guest asked"))
	if st != 200 || strings.Contains(raw, "R-W3-"+sfx) || strings.Contains(strings.ToUpper(raw), "YOUNG") || strings.Contains(raw, "G-Y-") {
		t.Fatalf("export of X reached Y: %d %s", st, raw)
	}
	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", s1, "guest asked"))
	if st != 200 {
		t.Fatalf("erase X = %d %v", st, body)
	}
	for name, got := range map[string][2]string{
		"S3 kept":          {gdStr(t, f, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, s3), "R-W3-" + sfx},
		"Y on S3 kept":     {gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm,external_guest_id) FROM iam_v2.stay_guests WHERE id=$1`, yOnS3), "YARA,YOUNG,G-Y-" + sfx},
		"Y on S2 kept":     {gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm,external_guest_id) FROM iam_v2.stay_guests WHERE id=$1`, yOnS2), "YARA,YOUNG,G-Y-" + sfx},
		"X on S2 erased":   {gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm,external_guest_id) FROM iam_v2.stay_guests WHERE id=$1`, xOnS2), ""},
		"S1 anonymised":    {gdStr(t, f, `SELECT (external_reservation_id LIKE 'anon-%')::text FROM iam_v2.stays WHERE id=$1`, s1), "true"},
		"S2 kept (shared)": {gdStr(t, f, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, s2), "R-W2-" + sfx},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
}

// Two guests are one person by PMS number, or by a unique full name; a surname alone, or a shared full name, never merges them.
func TestIntegration_GuestDataIsolation_OnePersonNotOneName(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())

	// Ann alone, then in a room with Bob Smith (her relative): no PMS numbers. Ann's export carries Ann, not Bob.
	a1 := seedGuestDataStay(t, f, "R-N1-"+sfx, "CHECKED_OUT")
	a2 := seedGuestDataStay(t, f, "R-N2-"+sfx, "CHECKED_OUT")
	guestRow(t, f, a1, "", "ANN", "SMITH")
	annOnA2 := guestRow(t, f, a2, "", "ANN", "SMITH")
	bob := addGuest(t, f, a2, "", "BOB", "SMITH")
	linkStays(t, f, a1, a2)
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", a1, "guest asked"))
	if st != 200 || !strings.Contains(strings.ToUpper(raw), "ANN") || strings.Contains(strings.ToUpper(raw), "BOB") {
		t.Fatalf("export of Ann = %d %s", st, raw)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", a1, "guest asked")); st != 200 {
		t.Fatalf("erase Ann = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm) FROM iam_v2.stay_guests WHERE id=$1`, bob); got != "BOB,SMITH" {
		t.Errorf("Bob Smith was touched: %q", got)
	}
	if got := gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm) FROM iam_v2.stay_guests WHERE id=$1`, annOnA2); got != "" {
		t.Errorf("Ann's row on the shared stay was kept: %q", got)
	}

	// John Doe alone, then a room with two guests both named John Doe: the request is refused as ambiguous.
	j1 := seedGuestDataStay(t, f, "R-J1-"+sfx, "CHECKED_OUT")
	j2 := seedGuestDataStay(t, f, "R-J2-"+sfx, "CHECKED_OUT")
	guestRow(t, f, j1, "", "JOHN", "DOE")
	twinA := guestRow(t, f, j2, "", "JOHN", "DOE")
	twinB := addGuest(t, f, j2, "", "JOHN", "DOE")
	linkStays(t, f, j1, j2)
	if st, body := f.do(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", j1, "guest asked")); st != 400 ||
		!strings.Contains(fmt.Sprint(body["message"]), "ambiguous") {
		t.Fatalf("an ambiguous name must be refused: %d %v", st, body)
	}
	// Two rows of one stay are two guests, never one: a twin whose name also decides a linked stay is refused as
	// ambiguous, and the other twin is not touched.
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay_guest", twinA, "guest asked")); st != 400 ||
		!strings.Contains(fmt.Sprint(body["message"]), "ambiguous") {
		t.Fatalf("erase one twin = %d %v, want 400 ambiguous", st, body)
	}
	if got := gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm) FROM iam_v2.stay_guests WHERE id=$1`, twinB); got != "JOHN,DOE" {
		t.Errorf("the other John Doe was erased: %q", got)
	}
}

// The reason is kept in the erasure and activity logs, so it may not name the guest.
func TestIntegration_GuestDataIsolation_AReasonNamingTheGuestIsRefused(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-RS-"+sfx, "CHECKED_OUT")
	guestRow(t, f, stay, "G-RS-"+sfx, "GRACE", "HOPPERFIELD")
	for _, reason := range []string{"Grace Hopperfield asked by phone", "mail from grace@example.com", "call +44 20 7946 0958", "reservation R-RS-" + sfx} {
		if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, reason)); st != 400 {
			t.Errorf("reason %q = %d %v, want 400", reason, st, body)
		}
		if st, _ := f.do(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", stay, reason)); st != 400 {
			t.Errorf("export reason %q = %d, want 400", reason, st)
		}
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked at the desk")); st != 200 {
		t.Fatalf("a plain reason must work: %d %v", st, body)
	}
}

// Erasure keeps an activity entry's who and when, takes out what names the guest (including a staff search
// for part of their name), and leaves entries about another guest whose address merely contains theirs.
func TestIntegration_GuestDataIsolation_TheActivityLogKeepsWhoAndWhen(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "ann-" + sfx + "@example.com"
	acct, _, _ := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.88.0.40")
	other := "joann-" + sfx + "@example.com"
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
		VALUES (now() - interval '1 day',$1,'operator','op-7','guest_account.password_set','guest_account',$2,jsonb_build_object('username',$3::text)),
		       (now() - interval '1 day',$1,'operator','op-7','guest_data.searched','guest_data',NULL,jsonb_build_object('search',$5::text)),
		       (now() - interval '1 day',$1,'operator','op-7','guest_account.created','guest_account','other-'||$4,jsonb_build_object('username',$6::text))`,
		f.tenant, acct, email, sfx, "ann-"+sfx[:6], other); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked"))
	if st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	ref := fmt.Sprint(body["erasure_ref"])
	type entry struct {
		actor, action, target string
		payload               map[string]any
	}
	read := func(action string) entry {
		var e entry
		var raw []byte
		var target *string
		if err := f.pool.QueryRow(ctx, `SELECT actor_id, action, target_id, payload FROM public.audit_log
			WHERE tenant_id=$1 AND action=$2 AND actor_id='op-7' ORDER BY ts DESC LIMIT 1`, f.tenant, action).Scan(&e.actor, &e.action, &target, &raw); err != nil {
			t.Fatalf("read %s: %v", action, err)
		}
		if target != nil {
			e.target = *target
		}
		_ = json.Unmarshal(raw, &e.payload)
		return e
	}
	if e := read("guest_account.password_set"); e.actor != "op-7" || e.target != "" || e.payload["username"] != "[erased]" || e.payload["erasure_ref"] != ref {
		t.Errorf("entry about the guest = %+v, want who/when kept, target cleared, username erased, ref %s", e, ref)
	}
	if e := read("guest_data.searched"); e.actor != "op-7" || e.payload["search"] != "[erased]" {
		t.Errorf("staff search for part of the guest's address = %+v, want the text erased", e)
	}
	if e := read("guest_account.created"); e.payload["username"] != other || e.payload["erasure_ref"] != nil {
		t.Errorf("another guest's entry was touched: %+v", e)
	}
}

// A guest who checked in after the night's full room list, with no departure date, is still in house.
func TestIntegration_GuestDataIsolation_NotWhileTheGuestMayBeInHouse(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	stay := seedGuestDataStay(t, f, "R-IH-"+fmt.Sprint(time.Now().UnixNano()), "IN_HOUSE")
	var iface string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE iam_v2.stays SET departure = NULL WHERE id=$1::uuid RETURNING pms_interface_id::text`, stay).Scan(&iface)
	}); err != nil {
		t.Fatal(err)
	}
	// The room list was published an hour BEFORE the stay's check-in message arrived.
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		  (tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode, resync_generation_seq, published_resync_generation,
		   last_complete_sync_at)
		VALUES ($1,$2,$3::uuid,1,'NONE',1,1, now() - interval '1 hour')`, f.tenant, f.site, iface); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 409 || fmt.Sprint(body["live"]) != "[pms_open]" {
		t.Fatalf("a guest who checked in after the room list must be refused: %d %v", st, body)
	}
}

// An identity also used at another site is not this site's to find, export or erase.
func TestIntegration_GuestDataIsolation_NoOtherSitesGuest(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "roam-" + sfx + "@example.com"
	var principal, otherSite string
	if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Roam') RETURNING id)
		INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
		SELECT $1, gp.id, 'EMAIL', '', $2, now(), '{}'::jsonb FROM gp RETURNING guest_principal_id::text`, f.tenant, email).Scan(&principal); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO public.sites(tenant_id,code,name) VALUES ($1,'other-'||$2,'Other site') RETURNING id::text`,
		f.tenant, sfx).Scan(&otherSite); err != nil {
		t.Fatalf("seed other site: %v", err)
	}
	gn, _ := seedGuestNetwork(t, f.pool, f.tenant, otherSite, "Other VLAN "+sfx, "10.96.0.0/24")
	var device string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,gen_random_uuid(),'02:aa:bb:cc:dd:51'::macaddr) RETURNING id::text`, f.tenant, otherSite).Scan(&device); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := controlled(ctx, f.pool, []string{"auth_context"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_principal_id,device_id,guest_network_id,expires_at)
			VALUES ($1,$2,'OTP',$3::uuid,$4::uuid,$5::uuid,now()+interval '10 minutes')`, f.tenant, otherSite, principal, device, gn)
		return err
	}); err != nil {
		t.Fatalf("sign in at the other site: %v", err)
	}
	st, body := f.searchGuestData(t, email)
	if m, _ := body["matches"].([]any); st != 200 || len(m) != 0 {
		t.Fatalf("another site's guest was offered: %d %v", st, body)
	}
	for _, path := range []string{"/guest-data-rights/export", "/guest-data-rights/erase"} {
		if st, body := f.do(t, "POST", path, gdReq(f, "principal", principal, "guest asked")); st != 409 ||
			!strings.Contains(fmt.Sprint(body["live"]), "other_site") {
			t.Errorf("%s of another site's guest = %d %v, want 409 other_site", path, st, body)
		}
	}
}

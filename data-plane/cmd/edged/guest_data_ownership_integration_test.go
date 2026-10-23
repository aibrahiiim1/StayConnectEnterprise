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

// Room credentials prove eligibility, not identity: a PMS may name only the reservation holder while several
// people use the room. A request about one person must never export or change another person's data.

// newAccess creates one entitlement whose subject is the given column (stay_id, guest_account_id or
// guest_principal_id): ended two days ago, or live when live is true.
func newAccess(t *testing.T, f *apiFixture, tag, col, subject string, live bool) string {
	t.Helper()
	ctx := context.Background()
	var svcRev, pkgRev, purchase, ent string
	if err := f.pool.QueryRow(ctx, `WITH sp AS (INSERT INTO iam_v2.service_plans (tenant_id, site_id, code) VALUES ($1,$2,'plan-'||$3) RETURNING id)
		INSERT INTO iam_v2.service_plan_revisions (tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices)
		SELECT $1,$2,sp.id,1,10000,5000,8 FROM sp RETURNING id::text`, f.tenant, f.site, tag).Scan(&svcRev); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `WITH p AS (INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code) VALUES ($1,$2,'pkg-'||$3) RETURNING id)
		INSERT INTO iam_v2.internet_package_revisions (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type)
		SELECT $1,$2,p.id,1,$4,'FREE_STAY' FROM p RETURNING id::text`, f.tenant, f.site, tag, svcRev).Scan(&pkgRev); err != nil {
		t.Fatalf("seed package: %v", err)
	}
	if err := controlled(ctx, f.pool, []string{"commerce_intent"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, trigger)
			VALUES ($1,$2,$3,'ADMIN_GRANT') RETURNING id::text`, f.tenant, f.site, pkgRev).Scan(&purchase)
	}); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	end := "now() - interval '2 days'"
	if live {
		end = "now() + interval '1 day'"
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, purchase_id, `+col+`, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, window_ends_at)
		VALUES ($1,$2,$3,$4,'{}'::jsonb,$5,$6,'VALIDITY_WINDOW','VALIDITY_WINDOW', `+end+`) RETURNING id::text`,
		f.tenant, f.site, purchase, subject, svcRev, pkgRev).Scan(&ent); err != nil {
		t.Fatalf("seed entitlement (%s): %v", col, err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now() - interval '3 days','ADMIN_GRANT')`, ent); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !live {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now() - interval '2 days','ADMIN')`, ent); err != nil {
			t.Fatalf("terminate: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit entitlement: %v", err)
	}
	return ent
}

func newDevice(t *testing.T, f *apiFixture, mac string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac, first_seen, last_seen, last_ip)
		VALUES ($1,$2,gen_random_uuid(),$3::macaddr, now() - interval '5 days', now() - interval '2 days', '10.66.0.9'::inet)
		RETURNING id::text`, f.tenant, f.site, mac).Scan(&id); err != nil {
		t.Fatalf("seed device %s: %v", mac, err)
	}
	return id
}

// onDevice authorises the entitlement on the device and records one session there (ended unless online).
func onDevice(t *testing.T, f *apiFixture, ent, dev, ip, method string, online bool) string {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id)
		VALUES ($1,$2,$3::uuid,$4::uuid)`, f.tenant, f.site, ent, dev); err != nil {
		t.Fatalf("authorise device: %v", err)
	}
	var sess string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id, site_id, entitlement_id, device_id, ip, mac, state, credential_method, started, ended, end_reason, bytes_down)
		SELECT $1,$2,$3::uuid,$4::uuid,$5::inet,dv.mac,CASE WHEN $7 THEN 'active' ELSE 'ended' END,$6,
		       now() - interval '3 days', CASE WHEN $7 THEN NULL ELSE now() - interval '2 days' END, CASE WHEN $7 THEN NULL ELSE 'TEST' END, 2048
		  FROM iam_v2.devices dv WHERE dv.id = $4::uuid RETURNING id::text`,
		f.tenant, f.site, ent, dev, ip, method, online).Scan(&sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sess
}

func selfAccount(t *testing.T, f *apiFixture, name string, selfRegistered bool, stay, createdOnMAC string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO iam_v2.guest_access_accounts
		  (tenant_id, site_id, username, password_hash, display_name, self_registered, stay_id, created_by_device_mac, created_at)
		VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash',$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::macaddr, now() - interval '10 days')
		RETURNING id::text`, f.tenant, f.site, name, "Name of "+name, selfRegistered, stay, createdOnMAC).Scan(&id); err != nil {
		t.Fatalf("seed account %s: %v", name, err)
	}
	return id
}

func devMAC(t *testing.T, f *apiFixture, dev string) string {
	return gdStr(t, f, `SELECT mac::text FROM iam_v2.devices WHERE id=$1`, dev)
}
func sessAddr(t *testing.T, f *apiFixture, sess string) string {
	return gdStr(t, f, `SELECT concat_ws(',', host(ip), mac::text) FROM iam_v2.sessions WHERE id=$1`, sess)
}
func countsOf(body map[string]any) map[string]any {
	c, _ := body["counts"].(map[string]any)
	return c
}

// One reservation, one named guest, three people in the room: the room's devices and sessions are everyone's.
func TestIntegration_Ownership_RoomCredentialsAreSharedByManyOccupants(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-ROOM-"+sfx, "CHECKED_OUT")
	room := newAccess(t, f, "room"+sfx, "stay_id", stay, false)
	macs := []string{"02:aa:00:01:00:01", "02:aa:00:01:00:02", "02:aa:00:01:00:03"}
	var devs, sess []string
	for i, m := range macs {
		d := newDevice(t, f, m)
		devs = append(devs, d)
		sess = append(sess, onDevice(t, f, room, d, fmt.Sprintf("10.77.1.%d", i+1), "PMS", false))
	}

	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", stay, "guest asked"))
	if st != 200 || !strings.Contains(raw, "Lovelace") {
		t.Fatalf("export of the reservation = %d %s", st, raw)
	}
	for _, m := range macs {
		if strings.Contains(raw, m) {
			t.Errorf("the reservation holder's export carries an occupant's device %s", m)
		}
	}
	if strings.Contains(raw, "10.77.1.") || !strings.Contains(raw, `"room_access_records": 1`) || !strings.Contains(raw, `"devices_seen_by_that_access": 3`) {
		t.Errorf("the export must count the shared access, not list it: %s", raw)
	}

	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked"))
	if c := countsOf(body); st != 200 || c["room_access_kept_shared"] != float64(1) || c["devices"] != float64(0) || c["sessions"] != float64(0) {
		t.Fatalf("erase the reservation = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT (external_reservation_id LIKE 'anon-%')::text FROM iam_v2.stays WHERE id=$1`, stay); got != "true" {
		t.Error("the reservation itself must be anonymised")
	}
	for i := range macs {
		if got := devMAC(t, f, devs[i]); got != macs[i] {
			t.Errorf("an occupant's device was anonymised: %s -> %s", macs[i], got)
		}
		if got := sessAddr(t, f, sess[i]); got != fmt.Sprintf("10.77.1.%d,%s", i+1, macs[i]) {
			t.Errorf("an occupant's session address was cleared: %s", got)
		}
	}
}

// A client account the guest registered is hers alone, whatever reservation it is linked to; the reservation
// and its holder are not hers. Either request leaves the other untouched.
func TestIntegration_Ownership_AnAccountAndItsReservationAreSeparate(t *testing.T) {
	for _, order := range []string{"account first", "reservation first"} {
		t.Run(order, func(t *testing.T) {
			f := newAPI(t)
			sfx := fmt.Sprint(time.Now().UnixNano())
			stay := seedGuestDataStay(t, f, "R-PAIR-"+sfx, "CHECKED_OUT")
			room := newAccess(t, f, "room"+sfx, "stay_id", stay, false)
			roomDev := newDevice(t, f, "02:aa:00:02:00:01")
			roomSess := onDevice(t, f, room, roomDev, "10.77.2.1", "PMS", false)
			ownMAC := "02:aa:00:02:00:09"
			acct := selfAccount(t, f, "roommate-"+sfx+"@example.com", true, stay, ownMAC)
			own := newAccess(t, f, "own"+sfx, "guest_account_id", acct, false)
			ownDev := newDevice(t, f, ownMAC)
			ownSess := onDevice(t, f, own, ownDev, "10.77.2.9", "ACCOUNT", false)

			if order == "account first" {
				st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "account", acct, "guest asked"))
				if st != 200 || !strings.Contains(raw, ownMAC) || !strings.Contains(raw, `"linked_to_a_reservation": true`) {
					t.Fatalf("export of the account = %d %s", st, raw)
				}
				for _, leak := range []string{"R-PAIR-" + sfx, "Lovelace", "G-R-PAIR", "02:aa:00:02:00:01", "10.77.2.1\""} {
					if strings.Contains(raw, leak) {
						t.Errorf("the account holder's export carries the reservation's or the room's %q", leak)
					}
				}
				if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
					t.Fatalf("erase the account = %d %v", st, body)
				}
				if got := gdStr(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != "true" {
					t.Error("the account must be anonymised")
				}
				if got := devMAC(t, f, ownDev); !strings.HasPrefix(got, "03:") {
					t.Errorf("the account's own device was kept: %s", got)
				}
				if got := sessAddr(t, f, ownSess); got != "" {
					t.Errorf("the account's own session address was kept: %s", got)
				}
				for name, got := range map[string][2]string{
					"reservation kept":  {gdStr(t, f, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, stay), "R-PAIR-" + sfx},
					"holder kept":       {gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm) FROM iam_v2.stay_guests WHERE stay_id=$1`, stay), "ada,lovelace"},
					"room device kept":  {devMAC(t, f, roomDev), "02:aa:00:02:00:01"},
					"room session kept": {sessAddr(t, f, roomSess), "10.77.2.1,02:aa:00:02:00:01"},
				} {
					if got[0] != got[1] {
						t.Errorf("%s = %q, want %q", name, got[0], got[1])
					}
				}
				return
			}
			if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 200 {
				t.Fatalf("erase the reservation = %d %v", st, body)
			}
			for name, got := range map[string][2]string{
				"account kept":      {gdStr(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, acct), "false"},
				"account name kept": {gdStr(t, f, `SELECT display_name FROM iam_v2.guest_access_accounts WHERE id=$1`, acct), "Name of roommate-" + sfx + "@example.com"},
				"own device kept":   {devMAC(t, f, ownDev), ownMAC},
				"own session kept":  {sessAddr(t, f, ownSess), "10.77.2.9," + ownMAC},
				"room device kept":  {devMAC(t, f, roomDev), "02:aa:00:02:00:01"},
			} {
				if got[0] != got[1] {
					t.Errorf("%s = %q, want %q", name, got[0], got[1])
				}
			}
		})
	}
}

// An OTP or social identity is one person: their own device (remembered by their own tick) is theirs; a phone
// that was also used with the room's credentials is shared, because a MAC address does not say who held it.
func TestIntegration_Ownership_AnIdentityOwnsItsRememberedDeviceNotASharedOne(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "otp-" + sfx + "@example.com"
	var principal string
	if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Otto') RETURNING id)
		INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
		SELECT $1, gp.id, 'EMAIL', '', $2, now() - interval '9 days', '{}'::jsonb FROM gp RETURNING guest_principal_id::text`,
		f.tenant, email).Scan(&principal); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	own := newAccess(t, f, "otp"+sfx, "guest_principal_id", principal, false)
	remembered := newDevice(t, f, "02:aa:00:03:00:01")
	both := newDevice(t, f, "02:aa:00:03:00:02")
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.principal_device_credentials
		  (tenant_id, site_id, guest_principal_id, device_id, method, secret_hmac, key_id, expires_at)
		VALUES ($1,$2,$3::uuid,$4::uuid,'OTP',public.gen_random_bytes(32),'k1', now() + interval '30 days')`, f.tenant, f.site, principal, remembered); err != nil {
		t.Fatalf("seed remembered device: %v", err)
	}
	remSess := onDevice(t, f, own, remembered, "10.77.3.1", "OTP", false)
	bothOwn := onDevice(t, f, own, both, "10.77.3.2", "OTP", false)
	stay := seedGuestDataStay(t, f, "R-IDN-"+sfx, "CHECKED_OUT")
	room := newAccess(t, f, "room"+sfx, "stay_id", stay, false)
	bothRoom := onDevice(t, f, room, both, "10.77.3.2", "PMS", false)

	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "principal", principal, "guest asked"))
	if st != 200 || !strings.Contains(raw, email) || !strings.Contains(raw, "02:aa:00:03:00:01") {
		t.Fatalf("export of the identity = %d %s", st, raw)
	}
	if strings.Contains(raw, "02:aa:00:03:00:02") || strings.Contains(raw, "10.77.3.2") || strings.Contains(raw, "R-IDN-") ||
		!strings.Contains(raw, `"shared_devices": 1`) {
		t.Errorf("the shared device or the reservation is in the identity's export: %s", raw)
	}
	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "principal", principal, "guest asked"))
	if c := countsOf(body); st != 200 || c["devices"] != float64(1) || c["devices_kept_shared"] != float64(1) {
		t.Fatalf("erase the identity = %d %v", st, body)
	}
	for name, got := range map[string][2]string{
		"remembered device anonymised": {fmt.Sprint(strings.HasPrefix(devMAC(t, f, remembered), "03:")), "true"},
		"its session cleared":          {sessAddr(t, f, remSess), ""},
		"shared device kept":           {devMAC(t, f, both), "02:aa:00:03:00:02"},
		"shared device, own session":   {sessAddr(t, f, bothOwn), "10.77.3.2,02:aa:00:03:00:02"},
		"shared device, room session":  {sessAddr(t, f, bothRoom), "10.77.3.2,02:aa:00:03:00:02"},
		"identity anonymised":          {gdStr(t, f, `SELECT (factor_value_norm LIKE 'anon-%')::text FROM iam_v2.guest_principal_identities WHERE guest_principal_id=$1`, principal), "true"},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
}

// A staff-issued account may be given to a whole party, so its devices are never attributed to one person; two
// self-registered accounts that used one device share it. Both are left alone and counted, never guessed at.
func TestIntegration_Ownership_AmbiguousDevicesAreLeftAloneAndCounted(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())

	staff := selfAccount(t, f, "party-"+sfx, false, "", "")
	staffEnt := newAccess(t, f, "party"+sfx, "guest_account_id", staff, false)
	staffDev := newDevice(t, f, "02:aa:00:04:00:01")
	staffSess := onDevice(t, f, staffEnt, staffDev, "10.77.4.1", "ACCOUNT", false)
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "account", staff, "guest asked"))
	if st != 200 || !strings.Contains(raw, "party-"+sfx) || strings.Contains(raw, "02:aa:00:04:00:01") || !strings.Contains(raw, `"shared_devices": 1`) {
		t.Fatalf("export of a staff-issued account = %d %s", st, raw)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", staff, "guest asked")); st != 200 || countsOf(body)["devices_kept_shared"] != float64(1) {
		t.Fatalf("erase a staff-issued account = %d %v", st, body)
	}
	if devMAC(t, f, staffDev) != "02:aa:00:04:00:01" || sessAddr(t, f, staffSess) != "10.77.4.1,02:aa:00:04:00:01" {
		t.Error("a staff-issued account's device was changed")
	}

	a := selfAccount(t, f, "ann-"+sfx+"@example.com", true, "", "")
	b := selfAccount(t, f, "ben-"+sfx+"@example.com", true, "", "")
	aEnt := newAccess(t, f, "a"+sfx, "guest_account_id", a, false)
	bEnt := newAccess(t, f, "b"+sfx, "guest_account_id", b, false)
	tablet := newDevice(t, f, "02:aa:00:04:00:02")
	aSess := onDevice(t, f, aEnt, tablet, "10.77.4.2", "ACCOUNT", false)
	bSess := onDevice(t, f, bEnt, tablet, "10.77.4.2", "ACCOUNT", false)
	if st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "account", a, "guest asked")); st != 200 || strings.Contains(raw, "02:aa:00:04:00:02") {
		t.Fatalf("a shared tablet is in one account's export: %d %s", st, raw)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", a, "guest asked")); st != 200 {
		t.Fatalf("erase one of two = %d %v", st, body)
	}
	for name, got := range map[string][2]string{
		"tablet kept":         {devMAC(t, f, tablet), "02:aa:00:04:00:02"},
		"her session kept":    {sessAddr(t, f, aSess), "10.77.4.2,02:aa:00:04:00:02"},
		"his session kept":    {sessAddr(t, f, bSess), "10.77.4.2,02:aa:00:04:00:02"},
		"the other account":   {gdStr(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, b), "false"},
		"this account erased": {gdStr(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, a), "true"},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
}

// A room move continues the same reservation: both stays are the holder's PMS data, neither stay's room access
// is, and an account linked to the second stay is its own person's.
func TestIntegration_Ownership_ARoomMoveKeepsEveryRoomAndAccountOut(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	s1 := seedGuestDataStay(t, f, "R-MV1-"+sfx, "CHECKED_OUT")
	s2 := seedGuestDataStay(t, f, "R-MV2-"+sfx, "CHECKED_OUT")
	linkStays(t, f, s1, s2)
	r1 := newAccess(t, f, "r1"+sfx, "stay_id", s1, false)
	r2 := newAccess(t, f, "r2"+sfx, "stay_id", s2, false)
	d1, d2 := newDevice(t, f, "02:aa:00:05:00:01"), newDevice(t, f, "02:aa:00:05:00:02")
	ss1 := onDevice(t, f, r1, d1, "10.77.5.1", "PMS", false)
	ss2 := onDevice(t, f, r2, d2, "10.77.5.2", "PMS", false)
	acct := selfAccount(t, f, "guest2-"+sfx+"@example.com", true, s2, "")

	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", s1, "guest asked")); st != 200 || countsOf(body)["hotel_stays"] != float64(2) ||
		countsOf(body)["room_access_kept_shared"] != float64(2) {
		t.Fatalf("erase the first room = %d %v", st, body)
	}
	for name, got := range map[string][2]string{
		"first room device":  {devMAC(t, f, d1), "02:aa:00:05:00:01"},
		"second room device": {devMAC(t, f, d2), "02:aa:00:05:00:02"},
		"first session":      {sessAddr(t, f, ss1), "10.77.5.1,02:aa:00:05:00:01"},
		"second session":     {sessAddr(t, f, ss2), "10.77.5.2,02:aa:00:05:00:02"},
		"linked account":     {gdStr(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, acct), "false"},
		"second stay (PMS)":  {gdStr(t, f, `SELECT (external_reservation_id LIKE 'anon-%')::text FROM iam_v2.stays WHERE id=$1`, s2), "true"},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}
}

// A PMS guest number means something only inside its own PMS interface: the same number on another interface is
// another person; only a unique full name can tie a guest across interfaces (a transfer between two PMSs).
func TestIntegration_Ownership_APmsGuestNumberBelongsToItsInterface(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	g1 := "G-1-" + sfx
	sa := seedGuestDataStay(t, f, "R-IFA-"+sfx, "CHECKED_OUT") // each seeded stay has its own PMS interface
	sb := seedGuestDataStay(t, f, "R-IFB-"+sfx, "CHECKED_OUT")
	sd := seedGuestDataStay(t, f, "R-IFD-"+sfx, "CHECKED_OUT")
	annA := guestRow(t, f, sa, g1, "ANN", "LEE")
	cy := addGuest(t, f, sa, "G-CY-"+sfx, "CY", "COOK")
	bob := guestRow(t, f, sb, g1, "BOB", "RAY") // the same number as Ann, on another PMS
	dee := addGuest(t, f, sb, "G-DEE-"+sfx, "DEE", "DUNN")
	annD := guestRow(t, f, sd, "G-9-"+sfx, "ANN", "LEE") // another number, another PMS, the same full name, unique there
	fay := addGuest(t, f, sd, "G-FAY-"+sfx, "FAY", "FORD")
	linkStays(t, f, sa, sb)
	linkStays(t, f, sa, sd)

	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay_guest", annA, "guest asked")); st != 200 {
		t.Fatalf("erase Ann = %d %v", st, body)
	}
	row := func(id string) string {
		return gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm,external_guest_id) FROM iam_v2.stay_guests WHERE id=$1`, id)
	}
	for name, got := range map[string][2]string{
		"Ann erased":                        {row(annA), ""},
		"Ann by unique name on another PMS": {row(annD), ""},
		"Bob (same number, other PMS) kept": {row(bob), "BOB,RAY," + g1},
		"Dee kept":                          {row(dee), "DEE,DUNN,G-DEE-" + sfx},
		"Cy kept":                           {row(cy), "CY,COOK,G-CY-" + sfx},
		"Fay kept":                          {row(fay), "FAY,FORD,G-FAY-" + sfx},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %q, want %q", name, got[0], got[1])
		}
	}

	// The rule itself: a number decides only on one interface; across interfaces only a full name can.
	same := func(aExt, aFirst, aLast, aIf, bExt, bFirst, bLast, bIf string) string {
		return gdStr(t, f, `SELECT coalesce(iam_v2.same_guest(NULLIF($1,''),$2,$3,NULLIF($4,'')::uuid,NULLIF($5,''),$6,$7,NULLIF($8,'')::uuid),'no')`,
			aExt, aFirst, aLast, aIf, bExt, bFirst, bLast, bIf)
	}
	ifA := gdStr(t, f, `SELECT pms_interface_id::text FROM iam_v2.stays WHERE id=$1`, sa)
	ifB := gdStr(t, f, `SELECT pms_interface_id::text FROM iam_v2.stays WHERE id=$1`, sb)
	for name, got := range map[string][2]string{
		"same interface, same number, another spelling": {same("G1", "ANN", "LEE", ifA, "G1", "ANNE", "LEE", ifA), "id"},
		"same interface, different numbers":             {same("G1", "ANN", "LEE", ifA, "G2", "ANN", "LEE", ifA), "no"},
		"other interface, same number, other name":      {same("G1", "ANN", "LEE", ifA, "G1", "BOB", "RAY", ifB), "no"},
		"other interface, same number, same full name":  {same("G1", "ANN", "LEE", ifA, "G1", "ANN", "LEE", ifB), "name"},
		"a surname alone is nobody":                     {same("", "", "LEE", ifA, "", "", "LEE", ifB), "no"},
	} {
		if got[0] != got[1] {
			t.Errorf("same_guest, %s = %q, want %q", name, got[0], got[1])
		}
	}
}

// What was typed to enter a reservation is the reservation's; who typed it is not, and stays.
func TestIntegration_Ownership_ARoomAttemptKeepsTheTypist(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-ATT-"+sfx, "CHECKED_OUT")
	var attempt string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts
		  (tenant_id, site_id, occurred_at, result, verifier_kind, submitted_room, matched_stay_id, device_mac, device_ip,
		   sensitive_ciphertext, sensitive_nonce, encryption_key_id, cipher_version)
		VALUES ($1,$2,now() - interval '1 day','VERIFIED','FULL_NAME','101',$3::uuid,'02:aa:00:06:00:01','10.77.6.1',
		        '\xdeadbeef','\x0102030405060708090a0b0c',gen_random_uuid(),1) RETURNING id::text`, f.tenant, f.site, stay).Scan(&attempt); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", stay, "guest asked")); st != 200 || strings.Contains(raw, "10.77.6.1") || strings.Contains(raw, "02:aa:00:06:00:01") {
		t.Fatalf("the typist's address is in the reservation holder's export: %d %s", st, raw)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	got := gdStr(t, f, `SELECT concat_ws('|', coalesce(submitted_room,'-'), (sensitive_ciphertext IS NULL)::text, host(device_ip), device_mac::text)
		FROM iam_v2.sign_in_attempts WHERE id=$1`, attempt)
	if got != "-|true|10.77.6.1|02:aa:00:06:00:01" {
		t.Errorf("attempt after erasing the reservation = %q, want the typed content gone and the typist kept", got)
	}
}

// The activity log is not rewritten for what is shared, and a MAC search never offers a reservation.
func TestIntegration_Ownership_SharedAccessIsNotRedactedOrSearchedByDevice(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-LOG-"+sfx, "CHECKED_OUT")
	room := newAccess(t, f, "room"+sfx, "stay_id", stay, false)
	dev := newDevice(t, f, "02:aa:00:07:00:01")
	onDevice(t, f, room, dev, "10.77.7.1", "PMS", false)
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
		VALUES (now() - interval '1 day',$1,'operator','op-9','entitlement.suspended','entitlement',$2,jsonb_build_object('note','roommate laptop 02:aa:00:07:00:01'))`,
		f.tenant, room); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	st, body := f.do(t, "GET", "/guest-data-rights/search?q=02:aa:00:07:00:01", nil)
	if m, _ := body["matches"].([]any); st != 200 || len(m) != 0 {
		t.Fatalf("a MAC search offered %v (%d), want nothing: the device is the room's, not one person's", body, st)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT target_id || '|' || (payload->>'note') FROM public.audit_log WHERE action='entitlement.suspended' AND tenant_id=$1`, f.tenant); got != room+"|roommate laptop 02:aa:00:07:00:01" {
		t.Errorf("an entry about the room's shared access was rewritten: %q", got)
	}
}

// The room's access is not changed, but a reservation is still never erased from under people who are using it.
func TestIntegration_Ownership_LiveRoomAccessStillBlocksTheReservation(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-LIVE-"+sfx, "CHECKED_OUT")
	room := newAccess(t, f, "room"+sfx, "stay_id", stay, true)
	dev := newDevice(t, f, "02:aa:00:08:00:01")
	onDevice(t, f, room, dev, "10.77.8.1", "PMS", true)
	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked"))
	live := fmt.Sprint(body["live"])
	if st != 409 || !strings.Contains(live, "access_live") || !strings.Contains(live, "online") {
		t.Fatalf("erasing a reservation people are online on = %d %v, want 409 access_live and online", st, body)
	}
	if got := gdStr(t, f, `SELECT (external_reservation_id LIKE 'anon-%')::text FROM iam_v2.stays WHERE id=$1`, stay); got != "false" {
		t.Error("the reservation was anonymised while its room was online")
	}
}

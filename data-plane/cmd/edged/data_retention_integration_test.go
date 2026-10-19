//go:build integration

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

// GDPR plan C1 (migration 0127): guest-data retention is an audited operational setting with the approved
// defaults, and the hourly run anonymises what outlived it -- and nothing that is still live, recent, or not
// personal data.

func TestIntegration_DataRetention_TheSetting(t *testing.T) {
	f := newAPI(t)
	st, body := f.do(t, "GET", "/data-retention/", nil)
	set, _ := body["settings"].(map[string]any)
	if st != 200 || set == nil || body["config_version"] != float64(0) {
		t.Fatalf("default = %d %v", st, body)
	}
	want := map[string]float64{"pms_stay_days": 30, "client_activity_days": 365, "inactive_identity_days": 365,
		"expired_record_days": 7, "audit_log_days": 730, "sign_in_attempt_days": 30, "log_days": 14}
	for k, v := range want {
		if set[k] != v {
			t.Fatalf("default %s = %v, want %v (Product-Owner decision D-C1)", k, set[k], v)
		}
	}
	good := func() map[string]any {
		return map[string]any{"pms_stay_days": 60, "client_activity_days": 180, "inactive_identity_days": 400,
			"expired_record_days": 3, "audit_log_days": 365, "sign_in_attempt_days": 14, "log_days": 365}
	}
	for name, mutate := range map[string]func(map[string]any){
		"pms too short":      func(m map[string]any) { m["pms_stay_days"] = 6 },
		"audit too short":    func(m map[string]any) { m["audit_log_days"] = 89 },
		"attempts too long":  func(m map[string]any) { m["sign_in_attempt_days"] = 366 },
		"logs too long":      func(m map[string]any) { m["log_days"] = 731 },
		"logs zero":          func(m map[string]any) { m["log_days"] = 0 },
		"one missing":        func(m map[string]any) { delete(m, "client_activity_days") },
		"an unknown setting": func(m map[string]any) { m["forever_days"] = 30 },
	} {
		s := good()
		mutate(s)
		if st, _ := f.do(t, "PUT", "/data-retention/", map[string]any{"settings": s, "reason": "bad " + name, "password": f.password}); st != 400 {
			t.Fatalf("accepted %s (%d)", name, st)
		}
	}
	if st, _ := f.do(t, "PUT", "/data-retention/", map[string]any{"settings": good(), "reason": "", "password": f.password}); st != 400 {
		t.Fatal("saved without a reason")
	}
	if st, _ := f.do(t, "PUT", "/data-retention/", map[string]any{"settings": good(), "reason": "a change", "password": "wrong"}); st != 401 {
		t.Fatal("a wrong step-up password changed the retention")
	}
	st, body = f.do(t, "PUT", "/data-retention/", map[string]any{"settings": good(), "reason": "hotel policy v2", "password": f.password})
	if st != 200 || body["config_version"] != float64(1) {
		t.Fatalf("save = %d %v", st, body)
	}
	st, body = f.do(t, "GET", "/data-retention/", nil)
	set, _ = body["settings"].(map[string]any)
	ch, _ := body["changes"].([]any)
	if st != 200 || set["pms_stay_days"] != float64(60) || set["sign_in_attempt_days"] != float64(14) || set["log_days"] != float64(365) || len(ch) != 1 {
		t.Fatalf("after the change = %v", body)
	}
	var audited int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM public.audit_log WHERE action='data_retention.changed' AND target_id=$1`, f.site).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit rows %d (%v), want 1", audited, err)
	}
	it := newAPIIn(t, f.tenant, "hotel_it_manager")
	if st, _ := it.do(t, "GET", "/data-retention/", nil); st != 200 {
		t.Fatalf("a role that can see backups cannot see the retention: %d", st)
	}
	if st, _ := it.do(t, "PUT", "/data-retention/", map[string]any{"settings": good(), "reason": "it try", "password": it.password}); st != 403 {
		t.Fatalf("a role without backups write changed the retention: %d", st)
	}
	vouchers := newAPIIn(t, f.tenant, "voucher_operator")
	if st, _ := vouchers.do(t, "GET", "/data-retention/", nil); st != 403 {
		t.Fatalf("a role with no backups access read the retention: %d", st)
	}
}

func TestIntegration_DataRetention_AnonymisesOnlyWhatOutlivedTheRetention(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	const longAgo = 2 * 365 * 24 * time.Hour

	// --- PMS: a stay checked out 60 days ago (past the 30-day default) and one 5 days ago.
	var iface string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
		VALUES (gen_random_uuid(),$1,$2,'protel-fias',$3,'ACTIVE') RETURNING id::text`,
		f.tenant, f.site, fmt.Sprintf("PMS retention %d", time.Now().UnixNano())).Scan(&iface); err != nil {
		t.Fatalf("seed interface: %v", err)
	}
	seedStay := func(res, room string, checkedOutDaysAgo int) string {
		var stay string
		if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(tenant_id,site_id,pms_interface_id,external_reservation_id,
				  external_stay_identity,normalized_room_number,status,arrival,departure,effective_checkout_at,vip,travel_agent)
				VALUES ($1,$2,$3::uuid,$4,$4,$5,'CHECKED_OUT',current_date - ($6::int + 3),current_date - $6::int,
				        now() - make_interval(days => $6::int),true,'Agency Ltd') RETURNING id::text`,
				f.tenant, f.site, iface, res, room, checkedOutDaysAgo).Scan(&stay); err != nil {
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
				  event_type,processing_status,received_at,payload)
				VALUES ($1,$2,$3::uuid,'EV-'||$4,'GUEST_OUT','PENDING',now() - make_interval(days => $5::int),
				        jsonb_build_object('reservation',$4::text,'room',$6::text,'last_name','Lovelace')) RETURNING id::text`,
				f.tenant, f.site, iface, res, checkedOutDaysAgo, room).Scan(&ev); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status='APPLIED', stay_id=$2::uuid, processed_at=now()
				WHERE id=$1::uuid`, ev, stay)
			return err
		}); err != nil {
			t.Fatalf("seed stay %s: %v", res, err)
		}
		return stay
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	oldStay := seedStay("R-OLD-"+suffix, "101", 60)
	recentStay := seedStay("R-NEW-"+suffix, "102", 5)
	// GDPR audit B2 (0137): a stay the PMS never closed is anonymised once its departure is past the retention;
	// one still in-house with a departure to come is not.
	forgotten := seedGuestDataStay(t, f, "R-FORGOT-"+suffix, "IN_HOUSE")
	inHouse := seedGuestDataStay(t, f, "R-HERE-"+suffix, "IN_HOUSE")
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET arrival = current_date - 70, departure = current_date - 60 WHERE id=$1::uuid`, forgotten)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Migration 0066's rollback copy of the names and room, for both stays (GDPR audit A6, migration 0135).
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.backfill_0066_identity_text (kind,row_id,tenant_id,site_id,prior_first_name,prior_last_name)
		SELECT 'stay_guest', g.id, g.tenant_id, g.site_id, 'Ada', 'Lovelace' FROM iam_v2.stay_guests g WHERE g.stay_id = ANY($1::uuid[]);
		`, []string{oldStay, recentStay}); err != nil {
		t.Fatalf("seed 0066 copy: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.backfill_0066_identity_text (kind,row_id,tenant_id,site_id,prior_room)
		SELECT 'stay_room', s.id, s.tenant_id, s.site_id, '101A' FROM iam_v2.stays s WHERE s.id = ANY($1::uuid[])`,
		[]string{oldStay, recentStay}); err != nil {
		t.Fatalf("seed 0066 copy: %v", err)
	}

	// --- An account used two years ago (its entitlement ended then, its device last seen then) and a live one.
	oldAcct, oldDevice, oldSession := seedEndedAccount(t, f, "old-"+suffix+"@example.com", longAgo, "10.77.0.10")
	liveAcct, _, liveSessions := seedAccountWithSessions(t, f, "live-"+suffix+"@example.com", 0, "10.77.0.11")
	// Make the live account and its device LOOK old, so only the live-use checks (an active entitlement, an open
	// session) can be what protects them.
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.guest_access_accounts SET created_at = now() - interval '2 years' WHERE id=$1`, liveAcct); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.devices SET first_seen = now() - interval '3 years', last_seen = now() - interval '2 years'
		WHERE id = (SELECT device_id FROM iam_v2.sessions WHERE id=$1)`, liveSessions[0].id); err != nil {
		t.Fatal(err)
	}
	var removedAcct string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, enabled, created_at)
		VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash',false, now() - interval '2 years') RETURNING id::text`,
		f.tenant, f.site, "gone-"+suffix+"@example.com.removed-abcd1234.invalid").Scan(&removedAcct); err != nil {
		t.Fatalf("seed removed account: %v", err)
	}

	// --- Guest identities: one verified two years ago and never used since, one verified today.
	seedPrincipal := func(email string, verifiedAgo time.Duration) string {
		var p string
		if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Ada') RETURNING id)
			INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
			SELECT $1, gp.id, 'EMAIL', '', $2, now() - $3::interval, '{"email":"x"}'::jsonb FROM gp RETURNING guest_principal_id::text`,
			f.tenant, email, fmt.Sprintf("%d seconds", int(verifiedAgo.Seconds()))).Scan(&p); err != nil {
			t.Fatalf("seed principal: %v", err)
		}
		return p
	}
	oldPrincipal := seedPrincipal("old-"+suffix+"@example.com", longAgo)
	newPrincipal := seedPrincipal("new-"+suffix+"@example.com", 0)

	// --- Expired verification codes: one expired 30 days ago, one live.
	seedOTP := func(dest string, expiresIn string) string {
		var id string
		if err := f.pool.QueryRow(ctx, `INSERT INTO public.auth_otps(tenant_id,channel,destination,code_hash,issued_at,expires_at,ip,user_agent)
			VALUES ($1,'email',$2,'h',now() + $3::interval - interval '10 minutes',now() + $3::interval,'10.77.0.5','UA') RETURNING id::text`,
			f.tenant, dest, expiresIn).Scan(&id); err != nil {
			t.Fatalf("seed otp: %v", err)
		}
		return id
	}
	oldOTP := seedOTP("old-"+suffix+"@example.com", "-30 days")
	liveOTP := seedOTP("live-"+suffix+"@example.com", "10 minutes")

	// --- The audit log: a row three years old (the oldest in the database, so the first window holds it) and a
	// row from now.
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,ip,user_agent,payload)
		VALUES (now() - interval '3 years',$1,'operator','op-1','guest_account.removed','guest_account',$2,'10.1.1.1','UA',
		        jsonb_build_object('username','old@example.com')),
		       (now(),$1,'operator','op-1','guest_account.removed','guest_account',$2,'10.1.1.1','UA',
		        jsonb_build_object('username','live@example.com'))`, f.tenant, suffix); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	// THE GUARDS STILL REFUSE EVERYONE ELSE: the anonymise flag alone, without being the table owner, opens
	// nothing.
	if _, err := f.pool.Exec(ctx, `DO $$ BEGIN
		PERFORM set_config('iam_v2.anonymise','retention',true);
		PERFORM iam_v2.begin_controlled_operation('stay');
		UPDATE iam_v2.stay_events SET payload='{}'::jsonb WHERE stay_id='`+oldStay+`'::uuid;
		END $$`); err == nil {
		t.Fatal("a non-owner rewrote a stay_events payload by setting the anonymise flag")
	}

	run := func() map[string]int {
		out := map[string]int{}
		for _, fn := range []string{"iam_v2.run_data_retention", "public.run_data_retention_public"} {
			rows, err := f.pool.Query(ctx, `SELECT o_category, o_anonymised FROM `+fn+`($1::uuid,$2::uuid,500)`, f.tenant, f.site)
			if err != nil {
				t.Fatalf("%s: %v", fn, err)
			}
			for rows.Next() {
				var c string
				var n int
				if err := rows.Scan(&c, &n); err != nil {
					t.Fatal(err)
				}
				out[c] += n
			}
			rows.Close()
		}
		return out
	}
	got := run()
	for _, c := range []string{"pms_stays", "client_activity", "inactive_identities", "expired_records", "audit_log"} {
		if got[c] == 0 {
			t.Errorf("category %s anonymised nothing (%v)", c, got)
		}
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
	prefixed := func(what, got, prefix string) {
		t.Helper()
		if len(got) < len(prefix) || got[:len(prefix)] != prefix {
			t.Errorf("%s = %q, want an anonymised value starting %q", what, got, prefix)
		}
	}

	// The old stay: identifiers are random tokens, guest data is gone, the history (status, dates) is kept.
	prefixed("old stay reservation", str(`SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, oldStay), "anon-")
	expect("old stay room", str(`SELECT normalized_room_number FROM iam_v2.stays WHERE id=$1`, oldStay), "<null>")
	expect("old stay travel agent", str(`SELECT travel_agent FROM iam_v2.stays WHERE id=$1`, oldStay), "<null>")
	expect("old stay status", str(`SELECT status FROM iam_v2.stays WHERE id=$1`, oldStay), "CHECKED_OUT")
	prefixed("forgotten in-house stay", str(`SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, forgotten), "anon-")
	expect("current in-house stay", str(`SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, inHouse), "R-HERE-"+suffix)
	expect("old stay departure kept", str(`SELECT (departure IS NOT NULL)::text FROM iam_v2.stays WHERE id=$1`, oldStay), "true")
	expect("old guest names", str(`SELECT concat_ws(',',first_name_norm,last_name_norm,display_name,external_guest_id) FROM iam_v2.stay_guests WHERE stay_id=$1`, oldStay), "")
	expect("old event payload", str(`SELECT payload::text FROM iam_v2.stay_events WHERE stay_id=$1`, oldStay), `{"anonymised": true}`)
	expect("old event status kept", str(`SELECT processing_status FROM iam_v2.stay_events WHERE stay_id=$1`, oldStay), "APPLIED")
	// The recent stay is untouched.
	expect("recent stay reservation", str(`SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, recentStay), "R-NEW-"+suffix)
	expect("recent guest name", str(`SELECT last_name_norm FROM iam_v2.stay_guests WHERE stay_id=$1`, recentStay), "lovelace")
	copyOf := func(stay string) string {
		return str(`SELECT coalesce(string_agg(concat_ws(',',x.prior_first_name,x.prior_last_name,x.prior_room), ';' ORDER BY x.kind),'')
			FROM iam_v2.backfill_0066_identity_text x
			WHERE x.row_id = $1::uuid OR x.row_id IN (SELECT g.id FROM iam_v2.stay_guests g WHERE g.stay_id = $1::uuid)`, stay)
	}
	expect("old stay's 0066 copy", copyOf(oldStay), ";")
	expect("recent stay's 0066 copy", copyOf(recentStay), "Ada,Lovelace;101A")

	// The old account, its device and its session.
	prefixed("old account username", str(`SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, oldAcct), "anon-")
	expect("old account still removed-shaped", str(`SELECT (username LIKE '%.removed-%.invalid' AND NOT enabled)::text FROM iam_v2.guest_access_accounts WHERE id=$1`, oldAcct), "true")
	prefixed("removed account username", str(`SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, removedAcct), "anon-")
	prefixed("old device mac", str(`SELECT mac::text FROM iam_v2.devices WHERE id=$1`, oldDevice), "03:")
	expect("old session address", str(`SELECT concat_ws(',',host(ip),mac::text) FROM iam_v2.sessions WHERE id=$1`, oldSession), "")
	expect("old session totals kept", str(`SELECT bytes_down::text FROM iam_v2.sessions WHERE id=$1`, oldSession), "4096")
	// The live account, its device and its session are untouched.
	expect("live account username", str(`SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, liveAcct), "live-"+suffix+"@example.com")
	expect("live session ip", str(`SELECT host(ip) FROM iam_v2.sessions WHERE id=$1`, liveSessions[0].id), "10.77.0.11")
	expect("live device mac kept", str(`SELECT (d.mac::text NOT LIKE '03:%')::text FROM iam_v2.sessions s JOIN iam_v2.devices d ON d.id=s.device_id WHERE s.id=$1`, liveSessions[0].id), "true")

	// Identities.
	prefixed("old identity", str(`SELECT factor_value_norm FROM iam_v2.guest_principal_identities WHERE guest_principal_id=$1`, oldPrincipal), "anon-")
	expect("old principal name", str(`SELECT display_name FROM iam_v2.guest_principals WHERE id=$1`, oldPrincipal), "<null>")
	expect("new identity", str(`SELECT factor_value_norm FROM iam_v2.guest_principal_identities WHERE guest_principal_id=$1`, newPrincipal), "new-"+suffix+"@example.com")

	// Verification codes.
	expect("old otp", str(`SELECT destination || coalesce(host(ip),'') || coalesce(user_agent,'') FROM public.auth_otps WHERE id=$1`, oldOTP), "anonymised")
	expect("live otp", str(`SELECT destination FROM public.auth_otps WHERE id=$1`, liveOTP), "live-"+suffix+"@example.com")

	// The audit log: what happened and which operator stay; addresses, targets and payload go.
	expect("old audit row", str(`SELECT concat_ws('|',action,actor_id,payload::text,coalesce(host(ip),'-'),coalesce(target_id,'-')) FROM public.audit_log
		WHERE tenant_id=$1 AND ts < now() - interval '2 years'`, f.tenant), `guest_account.removed|op-1|{"anonymised": true}|-|-`)
	expect("recent audit row", str(`SELECT payload->>'username' FROM public.audit_log WHERE tenant_id=$1 AND target_id=$2 AND ts > now() - interval '1 day'`, f.tenant, suffix), "live@example.com")

	// Every run that changed something is recorded, and a second run finds nothing left in the iam_v2 tables.
	var runs int
	if err := f.pool.QueryRow(ctx, `SELECT count(DISTINCT category) FROM iam_v2.data_retention_runs WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&runs); err != nil || runs != 5 {
		t.Errorf("recorded run categories = %d (%v), want 5", runs, err)
	}
	again := run()
	for _, c := range []string{"pms_stays", "client_activity", "inactive_identities"} {
		if again[c] != 0 {
			t.Errorf("a second run changed %d more in %s: anonymisation must be idempotent", again[c], c)
		}
	}
}

// The sign-in attempt purge follows the site's setting, per site.
func TestIntegration_DataRetention_SignInAttemptsFollowTheSiteSetting(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.data_retention_set($1,$2,30,365,365,7,730,7,14,'test','short attempts')`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	other := newAPI(t) // default 30 days
	seed := func(fx *apiFixture, daysAgo int) string {
		var id string
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id,site_id,occurred_at,result,verifier_kind)
			VALUES ($1,$2,now() - make_interval(days => $3::int),'CREDENTIAL_MISMATCH','UNKNOWN') RETURNING id::text`,
			fx.tenant, fx.site, daysAgo).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	shortOld, otherTen := seed(f, 10), seed(other, 10)
	if _, err := signinattempt.NewStore(f.pool, nil, "").Purge(ctx, time.Now()); err != nil {
		t.Fatalf("purge: %v", err)
	}
	count := func(id string) (n int) {
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sign_in_attempts WHERE id=$1::uuid`, id).Scan(&n)
		return n
	}
	if count(shortOld) != 0 {
		t.Error("a 10-day-old attempt survived on a site that keeps attempts 7 days")
	}
	if count(otherTen) != 1 {
		t.Error("a 10-day-old attempt was purged on a site that keeps the default 30 days")
	}
}

// seedEndedAccount is a client account whose one entitlement was used and ended `ago`, on a device last seen
// then, with one ended session carrying an address and a usage total.
func seedEndedAccount(t *testing.T, f *apiFixture, username string, ago time.Duration, ip string) (account, device, session string) {
	t.Helper()
	ctx := context.Background()
	agoI := fmt.Sprintf("%d seconds", int(ago.Seconds()))
	var svcRev, pkgRev, purchase string
	if err := f.pool.QueryRow(ctx, `WITH sp AS (INSERT INTO iam_v2.service_plans (tenant_id, site_id, code) VALUES ($1,$2,'plan-'||$3) RETURNING id)
		INSERT INTO iam_v2.service_plan_revisions (tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices)
		SELECT $1,$2,sp.id,1,10000,5000,4 FROM sp RETURNING id::text`, f.tenant, f.site, username).Scan(&svcRev); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `WITH p AS (INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code) VALUES ($1,$2,'pkg-'||$3) RETURNING id)
		INSERT INTO iam_v2.internet_package_revisions (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type)
		SELECT $1,$2,p.id,1,$4,'FREE_STAY' FROM p RETURNING id::text`, f.tenant, f.site, username, svcRev).Scan(&pkgRev); err != nil {
		t.Fatalf("seed package: %v", err)
	}
	if err := controlled(ctx, f.pool, []string{"commerce_intent"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, trigger)
			VALUES ($1,$2,$3,'ADMIN_GRANT') RETURNING id::text`, f.tenant, f.site, pkgRev).Scan(&purchase)
	}); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, display_name, notes, created_at, self_registered)
		VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash','Ada Lovelace','room 101 guest', now() - $4::interval, true) RETURNING id::text`,
		f.tenant, f.site, username, agoI).Scan(&account); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, purchase_id, guest_account_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, window_ends_at)
		VALUES ($1,$2,$3,$4,'{}'::jsonb,$5,$6,'VALIDITY_WINDOW','VALIDITY_WINDOW', now() - $7::interval)
		RETURNING id::text`, f.tenant, f.site, purchase, account, svcRev, pkgRev, agoI).Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now() - $2::interval - interval '1 day','ADMIN_GRANT')`, ent, agoI); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now() - $2::interval,'ADMIN')`, ent, agoI); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit entitlement: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac, first_seen, last_seen, last_ip)
		VALUES ($1,$2,gen_random_uuid(),'02:aa:bb:cc:dd:01'::macaddr, now() - $3::interval - interval '1 day', now() - $3::interval, $4::inet)
		RETURNING id::text`, f.tenant, f.site, agoI, ip).Scan(&device); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sessions
		(tenant_id, site_id, entitlement_id, device_id, ip, mac, state, credential_method, started, ended, end_reason, bytes_down)
		VALUES ($1,$2,$3,$4,$5::inet,'02:aa:bb:cc:dd:01'::macaddr,'ended','ACCOUNT', now() - $6::interval - interval '2 hours', now() - $6::interval, 'TEST', 4096)
		RETURNING id::text`, f.tenant, f.site, ent, device, ip, agoI).Scan(&session); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return account, device, session
}

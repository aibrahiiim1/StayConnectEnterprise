//go:build integration

package main

// Staff End Package / Reset Package (migration 0202) against the real router, RBAC middleware and database.
//
// What it proves: End ends the whole entitlement and only it, idempotently; Reset is a new, linked, zero-price ADMIN_GRANT cycle
// that leaves the original's usage and purchase untouched, is refused twice, and moves no money; After Check-out keeps its fixed
// cohort; and, when EDGED_ROLE_TEST_DSN is set, every call works as the Admin Console's own role (svc_edged), which the
// superuser integration database would otherwise hide (the 0200 lesson). Request ids are random so a second run on the same
// database (scripts/db-integration-combined.sh) is a new request, not a replay.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newPackageControlsAPI(t *testing.T, roles ...string) *apiFixture {
	t.Helper()
	f := newAPI(t, roles...)
	if len(roles) == 0 {
		roles = []string{"site_admin"}
	}
	s := &server{db: f.pool, sessions: newSessionStore(2 * time.Hour), tenantID: f.tenant, siteID: f.site}
	f.sessTok = s.sessions.create(&session{OperatorID: f.operator, Email: "op@test.local", Roles: roles})
	f.app = s
	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			mountResource(r, s, "package-controls", s.packageControlsRoutes)
		})
	})
	f.srv.Close()
	f.srv = httptest.NewServer(r)
	return f
}

type pkgFx struct{ stay, ent, pkgRev, device, otherEnt string }

// seedPackage builds an IN_HOUSE stay with a live, PAID room package (price 500) and one authorised device, plus an unrelated
// stay with its own live package that nothing here may touch. endMode GRACE_AFTER_CHECKOUT builds a checked-out stay instead.
func seedPackage(t *testing.T, f *apiFixture, endMode string) pkgFx {
	t.Helper()
	ctx := context.Background()
	var x pkgFx
	stayStatus := "IN_HOUSE"
	if endMode == "GRACE_AFTER_CHECKOUT" {
		stayStatus = "CHECKED_OUT"
	}
	var other string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH
	  ia AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  sa AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,
	           normalized_room_number, effective_checkout_at)
	         SELECT gen_random_uuid(),$1,$2,ia.id,'RES-P','SP',$3,'301', CASE WHEN $3='CHECKED_OUT' THEN now() END FROM ia RETURNING id),
	  so AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,
	           normalized_room_number)
	         SELECT gen_random_uuid(),$1,$2,ia.id,'RES-O','SO','IN_HOUSE','302' FROM ia RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code) VALUES (gen_random_uuid(),$1,$2,'PC-PLAN') RETURNING id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,max_concurrent_devices,
	            time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(),$1,$2,sp.id,1,3,'VALIDITY_WINDOW',1000000000 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,'PC-PAID',false,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,
	            package_type,price_minor,currency,currency_exponent,settlement_methods)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,spr.id,'GENERAL',500,'EUR',2,ARRAY['ONLINE_PAYMENT']::text[] FROM ip, spr RETURNING id)
	SELECT (SELECT id FROM sa)::text, (SELECT id FROM so)::text, (SELECT id FROM ipr)::text`,
			f.tenant, f.site, stayStatus).Scan(&x.stay, &other, &x.pkgRev)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, x.pkgRev); err != nil {
		t.Fatalf("current revision: %v", err)
	}
	grant := func(stay, mode string, grandfathered bool) (string, string) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, op := range []string{"commerce_intent", "device_auth"} {
			if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, op); err != nil {
				t.Fatalf("open %s: %v", op, err)
			}
		}
		var purchase, ent, dev string
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases (tenant_id,site_id,package_revision_id,stay_id,trigger,amount_minor,
			currency,currency_exponent,state) VALUES ($1,$2,$3,$4,'ADMIN_GRANT',500,'EUR',2,'GRANTED') RETURNING id::text`,
			f.tenant, f.site, x.pkgRev, stay).Scan(&purchase); err != nil {
			t.Fatalf("purchase: %v", err)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements (tenant_id,site_id,stay_id,purchase_id,policy_snapshot,
			service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,window_ends_at,status,consumed_data_bytes)
			SELECT $1,$2,$3,$4,'{}'::jsonb,ipr.service_plan_revision_id,$5,'VALIDITY_WINDOW',$6,now()+interval '4 hours','PENDING',123456
			  FROM iam_v2.internet_package_revisions ipr WHERE ipr.id=$5 RETURNING id::text`,
			f.tenant, f.site, stay, purchase, x.pkgRev, mode).Scan(&ent); err != nil {
			t.Fatalf("entitlement: %v", err)
		}
		// Activated four hours ago would put the window in the past; an hour ago keeps a 5-hour original duration.
		if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now()-interval '1 hour','GRANT')`, ent); err != nil {
			t.Fatalf("activate: %v", err)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac)
			VALUES ($1,$2,gen_random_uuid(),('02:'||(SELECT string_agg(lpad(to_hex((random()*255)::int),2,'0'),':') FROM generate_series(1,5)))::macaddr)
			RETURNING id::text`,
			f.tenant, f.site).Scan(&dev); err != nil {
			t.Fatalf("device: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices (tenant_id,site_id,entitlement_id,device_id,status,grandfathered,
			first_authorized,last_authorized) VALUES ($1,$2,$3,$4,'AUTHORIZED',$5,now(),now())`, f.tenant, f.site, ent, dev, grandfathered); err != nil {
			t.Fatalf("device row: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
			VALUES ($1,$2,$3,$4,1,now())`, f.tenant, f.site, ent, dev); err != nil {
			t.Fatalf("authorization: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return ent, dev
	}
	x.ent, x.device = grant(x.stay, endMode, endMode == "GRACE_AFTER_CHECKOUT")
	x.otherEnt, _ = grant(other, "VALIDITY_WINDOW", false)
	return x
}

func pkgAction(t *testing.T, f *apiFixture, ent, action, request string) (int, map[string]any) {
	t.Helper()
	return f.do(t, http.MethodPost, "/package-controls/"+ent+"/"+action,
		map[string]any{"password": f.password, "reason": "guest asked at the desk", "request_id": request})
}

func status(t *testing.T, f *apiFixture, ent string) (string, string) {
	t.Helper()
	var st string
	var reason *string
	if err := f.pool.QueryRow(context.Background(), `SELECT status, terminal_reason FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&st, &reason); err != nil {
		t.Fatalf("status: %v", err)
	}
	if reason == nil {
		return st, ""
	}
	return st, *reason
}

func TestIntegration_PackageControls_EndEndsTheWholePackageOnceAndNothingElse(t *testing.T) {
	f := newPackageControlsAPI(t)
	x := seedPackage(t, f, "VALIDITY_WINDOW")
	req := newUUID()
	code, body := pkgAction(t, f, x.ent, "end", req)
	if code != http.StatusOK || body["outcome"] != "ENDED" {
		t.Fatalf("end: %d %v", code, body)
	}
	if st, r := status(t, f, x.ent); st != "TERMINATED" || r != "ADMIN" {
		t.Fatalf("after end: %s %s", st, r)
	}
	if st, _ := status(t, f, x.otherEnt); st != "ACTIVE" {
		t.Fatal("ending one package touched another stay's")
	}
	var open int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.entitlement_device_authorizations
		WHERE entitlement_id=$1 AND deauthorized_at IS NULL`, x.ent).Scan(&open)
	if open != 0 {
		t.Fatalf("%d device authorizations left open on an ended package", open)
	}
	// The same request again: the recorded outcome, nothing new.
	code2, body2 := pkgAction(t, f, x.ent, "end", req)
	if code2 != http.StatusOK || body2["action_id"] != body["action_id"] {
		t.Fatalf("a repeated request was not idempotent: %d %v", code2, body2)
	}
	// Another request on an ended package: ALREADY_ENDED.
	if _, b := pkgAction(t, f, x.ent, "end", newUUID()); b["outcome"] != "ALREADY_ENDED" {
		t.Fatalf("a second end of an ended package: %v", b)
	}
	// The request id cannot be reused for a different action.
	if c, b := pkgAction(t, f, x.ent, "reset", req); c != http.StatusConflict || b["error"] != "request_reused" {
		t.Fatalf("a request id reused for another action: %d %v", c, b)
	}
	var action string
	if err := f.pool.QueryRow(context.Background(), `SELECT action FROM public.audit_log WHERE target_id=$1 ORDER BY ts DESC LIMIT 1`, x.ent).Scan(&action); err != nil || action != "package.ended" {
		t.Fatalf("audit: %q %v", action, err)
	}
}

func TestIntegration_PackageControls_ResetIsANewFreeLinkedCycleAndMovesNoMoney(t *testing.T) {
	f := newPackageControlsAPI(t)
	x := seedPackage(t, f, "VALIDITY_WINDOW")
	ctx := context.Background()
	var settlementsBefore, postingsBefore int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.settlements WHERE tenant_id=$1`, f.tenant).Scan(&settlementsBefore)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.posting_outbox WHERE tenant_id=$1`, f.tenant).Scan(&postingsBefore)

	code, body := pkgAction(t, f, x.ent, "reset", newUUID())
	if code != http.StatusOK {
		t.Fatalf("reset: %d %v", code, body)
	}
	newEnt, _ := body["new_entitlement_id"].(string)
	var supersedes, trigger string
	var amount int64
	var consumed int64
	var window time.Time
	if err := f.pool.QueryRow(ctx, `SELECT e.supersedes_entitlement_id::text, p.trigger, p.amount_minor, e.consumed_data_bytes, e.window_ends_at
		FROM iam_v2.entitlements e JOIN iam_v2.purchases p ON p.id=e.purchase_id WHERE e.id=$1`, newEnt).Scan(&supersedes, &trigger, &amount, &consumed, &window); err != nil {
		t.Fatalf("new entitlement: %v", err)
	}
	if supersedes != x.ent || trigger != "ADMIN_GRANT" || amount != 0 || consumed != 0 {
		t.Fatalf("the new cycle is not a free, linked grant: supersedes=%s trigger=%s amount=%d consumed=%d", supersedes, trigger, amount, consumed)
	}
	// A 5-hour original (activated an hour ago, ending in four) restarts as 5 hours from now.
	if d := time.Until(window); d < 4*time.Hour+50*time.Minute || d > 5*time.Hour+time.Minute {
		t.Fatalf("the reset window ends in %v, want the original 5 hours", d)
	}
	// The original keeps its record exactly: terminated SUPERSEDED, its usage and its paid purchase unchanged.
	var origConsumed, origAmount int64
	_ = f.pool.QueryRow(ctx, `SELECT e.consumed_data_bytes, p.amount_minor FROM iam_v2.entitlements e JOIN iam_v2.purchases p ON p.id=e.purchase_id
		WHERE e.id=$1`, x.ent).Scan(&origConsumed, &origAmount)
	if st, r := status(t, f, x.ent); st != "TERMINATED" || r != "SUPERSEDED" || origConsumed != 123456 || origAmount != 500 {
		t.Fatalf("the original was rewritten: %s %s consumed=%d amount=%d", st, r, origConsumed, origAmount)
	}
	// The device moved with the access.
	var moved int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_device_authorizations WHERE entitlement_id=$1 AND device_id=$2
		AND deauthorized_at IS NULL`, newEnt, x.device).Scan(&moved)
	if moved != 1 {
		t.Fatal("the online device did not keep its access on the new cycle")
	}
	var settlementsAfter, postingsAfter int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.settlements WHERE tenant_id=$1`, f.tenant).Scan(&settlementsAfter)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.posting_outbox WHERE tenant_id=$1`, f.tenant).Scan(&postingsAfter)
	if settlementsAfter != settlementsBefore || postingsAfter != postingsBefore {
		t.Fatal("a reset created a settlement or a PMS posting")
	}
	// The original cannot be reset again: the current package is the one to reset.
	if c, b := pkgAction(t, f, x.ent, "reset", newUUID()); c != http.StatusConflict || b["error"] != "superseded" {
		t.Fatalf("a second reset of the same original: %d %v", c, b)
	}
	// Two concurrent resets of the new cycle: exactly one wins.
	type res struct {
		code int
		body map[string]any
	}
	out := make(chan res, 2)
	for _, id := range []string{newUUID(), newUUID()} {
		go func(id string) { c, b := pkgAction(t, f, newEnt, "reset", id); out <- res{c, b} }(id)
	}
	ok := 0
	for i := 0; i < 2; i++ {
		if r := <-out; r.code == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d of two concurrent resets succeeded, want exactly one", ok)
	}
	var live int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, x.stay).Scan(&live)
	if live != 1 {
		t.Fatalf("the stay holds %d live entitlements, want one", live)
	}
}

func TestIntegration_PackageControls_AfterCheckoutResetKeepsTheFixedCohort(t *testing.T) {
	f := newPackageControlsAPI(t)
	x := seedPackage(t, f, "GRACE_AFTER_CHECKOUT")
	ctx := context.Background()
	if c, b := pkgAction(t, f, x.ent, "end", newUUID()); c != http.StatusOK {
		t.Fatalf("end: %d %v", c, b)
	}
	code, body := pkgAction(t, f, x.ent, "reset", newUUID())
	if code != http.StatusOK {
		t.Fatalf("reset After Check-out: %d %v", code, body)
	}
	newEnt, _ := body["new_entitlement_id"].(string)
	var reason string
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(disconnected_reason,'') FROM iam_v2.entitlement_devices WHERE entitlement_id=$1 AND device_id=$2 AND grandfathered`,
		newEnt, x.device).Scan(&reason); err != nil || reason != "PACKAGE_RESET_COHORT" {
		t.Fatalf("the cohort device was not carried: %q %v", reason, err)
	}
	// The cohort device may return; a device that was never on the stay may not.
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, newEnt, x.device); err != nil {
		t.Fatalf("the cohort device could not return: %v", err)
	}
	var stranger string
	_ = f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,gen_random_uuid(),'02:00:00:00:fe:fe'::macaddr) RETURNING id::text`,
		f.tenant, f.site).Scan(&stranger)
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1,$2,now())`, newEnt, stranger); err == nil ||
		!strings.Contains(err.Error(), "AFTER_CHECKOUT_NEW_DEVICE_REJECTED") {
		t.Fatalf("a new device joined an After Check-out reset: %v", err)
	}
	// No second automatic After Check-out grant: the reset is an ADMIN_GRANT.
	var grace int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND trigger='CHECKOUT_GRACE'`, x.stay).Scan(&grace)
	if grace != 0 {
		t.Fatal("a reset created an automatic After Check-out grant")
	}
}

func TestIntegration_PackageControls_RefusesWhatItMustNotOverride(t *testing.T) {
	f := newPackageControlsAPI(t)
	x := seedPackage(t, f, "VALIDITY_WINDOW")
	ctx := context.Background()
	// Checked out: a room package is not reset after the PMS boundary.
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=now() WHERE id=$1`, x.stay)
		return err
	}); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if c, b := pkgAction(t, f, x.ent, "reset", newUUID()); c != http.StatusConflict || b["error"] != "stay_not_in_house" {
		t.Fatalf("a checked-out room package was reset: %d %v", c, b)
	}
	// A role without the write key is refused by the middleware.
	g := newPackageControlsAPI(t, "site_viewer")
	if c, _ := pkgAction(t, g, x.otherEnt, "end", newUUID()); c != http.StatusForbidden {
		t.Fatalf("site_viewer ended a package: %d", c)
	}
	// No reason, no action.
	if c, _ := f.do(t, http.MethodPost, "/package-controls/"+x.otherEnt+"/end",
		map[string]any{"password": f.password, "reason": "", "request_id": newUUID()}); c != http.StatusBadRequest {
		t.Fatalf("an end without a reason: %d", c)
	}
}

// The whole journey as svc_edged: preview, end and reset need no privilege beyond the Gate-P grants (the 0200 lesson).
func TestIntegration_PackageControls_RunAsTheAdminConsoleRole(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set; skipping the svc_edged privilege proof (the golden lab always sets it)")
	}
	f := newPackageControlsAPI(t)
	x := seedPackage(t, f, "VALIDITY_WINDOW")
	rp, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	super := f.app.db
	f.app.db = rp
	defer func() { f.app.db = super }()
	if c, b := f.do(t, http.MethodGet, "/package-controls?stay="+x.stay, nil); c != http.StatusOK {
		t.Fatalf("list as svc_edged: %d %v", c, b)
	}
	if c, b := f.do(t, http.MethodGet, "/package-controls/"+x.ent, nil); c != http.StatusOK {
		t.Fatalf("preview as svc_edged: %d %v", c, b)
	}
	if c, b := pkgAction(t, f, x.ent, "reset", newUUID()); c != http.StatusOK {
		t.Fatalf("reset as svc_edged: %d %v", c, b)
	}
	if c, b := pkgAction(t, f, x.otherEnt, "end", newUUID()); c != http.StatusOK {
		t.Fatalf("end as svc_edged: %d %v", c, b)
	}
}

// After an End, the same free benefit is not acquired again automatically for that stay (Product Owner, 2026-11-01). A priced
// purchase is not blocked, an unrelated stay is not affected, and a staff Reset lifts the block.
func TestIntegration_PackageControls_AnEndedFreeBenefitIsNotTakenAgainAutomatically(t *testing.T) {
	f := newPackageControlsAPI(t)
	x := seedPackage(t, f, "VALIDITY_WINDOW")
	ctx := context.Background()
	if c, b := pkgAction(t, f, x.ent, "end", newUUID()); c != http.StatusOK || b["outcome"] != "ENDED" {
		t.Fatalf("end: %d %v", c, b)
	}
	// acquire tries a self-service acquisition of the same package for the stay, then rolls it back.
	acquire := func(trigger string, amount int64) error {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
			t.Fatalf("controlled operation: %v", err)
		}
		var purchase string
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases (tenant_id,site_id,package_revision_id,stay_id,trigger,amount_minor,state)
			VALUES ($1,$2,$3,$4,$5,$6,'GRANTED') RETURNING id::text`, f.tenant, f.site, x.pkgRev, x.stay, trigger, amount).Scan(&purchase); err != nil {
			t.Fatalf("purchase: %v", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO iam_v2.entitlements (tenant_id,site_id,stay_id,purchase_id,policy_snapshot,
			service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status)
			SELECT $1,$2,$3,$4,'{}'::jsonb,ipr.service_plan_revision_id,$5,'VALIDITY_WINDOW','VALIDITY_WINDOW','PENDING'
			  FROM iam_v2.internet_package_revisions ipr WHERE ipr.id=$5`, f.tenant, f.site, x.stay, purchase, x.pkgRev)
		return err
	}
	for _, trig := range []string{"ACCOUNT_AUTO_GRANT", "OTP_SOCIAL_DEFAULT"} {
		if err := acquire(trig, 0); err == nil || !strings.Contains(err.Error(), "PKG_ENDED_BY_STAFF") {
			t.Fatalf("the ended free package was taken again automatically (%s): %v", trig, err)
		}
	}
	if err := acquire("ACCOUNT_AUTO_GRANT", 500); err != nil {
		t.Fatalf("a priced purchase of the package was blocked after an End: %v", err)
	}
	if err := acquire("ADMIN_GRANT", 0); err != nil {
		t.Fatalf("a staff grant was blocked after an End: %v", err)
	}
	ended := func(stay string) bool {
		var b bool
		if err := f.pool.QueryRow(ctx, `SELECT iam_v2.package_ended_by_staff($1,$2,$3,$4,NULL,NULL,NULL,NULL)`,
			f.tenant, f.site, x.pkgRev, stay).Scan(&b); err != nil {
			t.Fatalf("package_ended_by_staff: %v", err)
		}
		return b
	}
	var otherStay string
	_ = f.pool.QueryRow(ctx, `SELECT stay_id::text FROM iam_v2.entitlements WHERE id=$1`, x.otherEnt).Scan(&otherStay)
	if !ended(x.stay) || ended(otherStay) {
		t.Fatalf("the rule is not scoped to the stay: this stay %v, another stay %v", ended(x.stay), ended(otherStay))
	}
	// The scd login (offer list and quote) can read the rule with Gate-P's grants alone.
	if dsn := os.Getenv("SCD_ROLE_TEST_DSN"); dsn != "" {
		rp, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer rp.Close()
		var b bool
		if err := rp.QueryRow(ctx, `SELECT iam_v2.package_ended_by_staff($1,$2,$3,$4,NULL,NULL,NULL,NULL)`,
			f.tenant, f.site, x.pkgRev, x.stay).Scan(&b); err != nil || !b {
			t.Fatalf("svc_scd cannot read the rule: %v %v", b, err)
		}
	}
	// A staff Reset is the deliberate way back.
	if c, b := pkgAction(t, f, x.ent, "reset", newUUID()); c != http.StatusOK {
		t.Fatalf("reset after end: %d %v", c, b)
	}
	if ended(x.stay) {
		t.Fatal("the block stayed after a staff Reset")
	}
}

// The staff reason is a registered staff note (0147): an erasure of the guest removes it, and outside that scrub the record stays
// append-only.
func TestIntegration_PackageControls_AnErasureRemovesTheStaffReason(t *testing.T) {
	f := newPackageControlsAPI(t)
	x := seedPackage(t, f, "VALIDITY_WINDOW")
	ctx := context.Background()
	if c, b := pkgAction(t, f, x.ent, "end", newUUID()); c != http.StatusOK {
		t.Fatalf("end: %d %v", c, b)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.package_admin_actions SET reason = 'edited later' WHERE entitlement_id = $1`, x.ent); err == nil {
		t.Fatal("a staff action record was edited outside the scrub")
	}
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.anonymise_stay($1)`, x.stay); err != nil {
		t.Fatalf("anonymise the stay: %v", err)
	}
	var reason string
	if err := f.pool.QueryRow(ctx, `SELECT reason FROM iam_v2.package_admin_actions WHERE entitlement_id = $1`, x.ent).Scan(&reason); err != nil ||
		reason != "Removed after the staff-notes period" {
		t.Fatalf("the staff reason survived the erasure: %q %v", reason, err)
	}
}

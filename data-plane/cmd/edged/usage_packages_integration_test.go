//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A stay seeded as room 3311 was found on the test appliance: a free package, an unlimited one, then the after-check-out
// package; one device stayed connected through all three. Seeded raw (replica role), since the shapes are what is under test.
type upFixture struct {
	tenant, site, stay, unlimitedStay string
	free, full, grace, full2, free2   string
	carried, single, purged           string
	t0, t1, t2, t3                    time.Time
}

func upSeed(t *testing.T, p *pgxpool.Pool) upFixture {
	t.Helper()
	ctx := context.Background()
	var f upFixture
	base := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	f.t0, f.t1, f.t2, f.t3 = base, base.Add(time.Hour), base.Add(2*time.Hour), base.Add(3*time.Hour)
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("seed %.60s: %v", sql, err)
		}
		return id
	}
	f.tenant = q(`INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 't' FROM gen_random_uuid() g RETURNING id::text`)
	f.site = q(`INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, $1, g::text, 's' FROM gen_random_uuid() g RETURNING id::text`, f.tenant)
	iface := q(`INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
		VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id::text`, f.tenant, f.site)
	stay := func(room, res string) string {
		return q(`INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
			normalized_room_number,status,effective_checkout_at,lifecycle_version,last_applied_event_version)
			VALUES (gen_random_uuid(),$1,$2,$3,$4,$4,$5,'CHECKED_OUT',$6,1,0) RETURNING id::text`, f.tenant, f.site, iface, res, room, f.t2)
	}
	f.stay, f.unlimitedStay = stay("3311", "UP-R1"), stay("1201", "UP-R2")
	device := func(mac string) string {
		return q(`INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac) VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`,
			f.tenant, f.site, mac)
	}
	d1, d2 := device("02:00:00:00:33:01"), device("02:00:00:00:33:02")
	plan := q(`INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled) VALUES (gen_random_uuid(),$1,$2,'plan',true) RETURNING id::text`, f.tenant, f.site)
	pkg := func(code, name, typ string, quota *int64, n int) string {
		spr := q(`INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,name,down_kbps,up_kbps,
			max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
			VALUES (gen_random_uuid(),$1,$2,$3,$4,$5,5000,2000,4,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',$6) RETURNING id::text`,
			f.tenant, f.site, plan, n, name+" plan", quota)
		ip := q(`INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system) VALUES (gen_random_uuid(),$1,$2,$3,$4) RETURNING id::text`,
			f.tenant, f.site, code, typ == "CHECKOUT_GRACE")
		return q(`INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,
			package_type,price_minor,settlement_methods,duration_policy,display)
			SELECT gen_random_uuid(),$1,$2,$3,1,$4::uuid,$5,0,ARRAY['NOT_REQUIRED']::text[],'{}'::jsonb,jsonb_build_object('name',$6::text)
			RETURNING id::text||'|'||$4::text`, f.tenant, f.site, ip, spr, typ, name)
	}
	gb := func(v int64) *int64 { return &v }
	freeRev, fullRev, graceRev := pkg("free", "Free Internet", "GENERAL", gb(1_000_000), 1), pkg("full", "Full Stay", "GENERAL", nil, 2),
		pkg("__system_checkout_grace", "Checkout grace", "CHECKOUT_GRACE", gb(200_000), 3)
	ent := func(stayID, revPair, status, reason string, consumed int64, from, until time.Time) string {
		rev, spr := revPair[:36], revPair[37:]
		pur := q(`INSERT INTO iam_v2.purchases(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
			VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`, f.tenant, f.site, rev, iface, stayID)
		var reasonArg, untilArg any
		if reason != "" {
			reasonArg, untilArg = reason, until
		}
		return q(`INSERT INTO iam_v2.entitlements(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
			service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status,terminal_reason,
			consumed_data_bytes,activated_at,terminated_at)
			VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW',$8,$9,$10,$11,$12) RETURNING id::text`,
			f.tenant, f.site, stayID, iface, pur, spr, rev, status, reasonArg, consumed, from, untilArg)
	}
	// The meters are what acctd charged each package; the samples below are what they were charged from.
	f.free = ent(f.stay, freeRev, "TERMINATED", "SUPERSEDED", 400_000, f.t0, f.t1)
	f.full = ent(f.stay, fullRev, "TERMINATED", "CONVERTED", 1_500_000, f.t1, f.t2)
	f.grace = ent(f.stay, graceRev, "TERMINATED", "DATA", 200_000, f.t2, f.t3)
	f.free2 = ent(f.unlimitedStay, freeRev, "TERMINATED", "SUPERSEDED", 900_000, f.t0, f.t1)
	f.full2 = ent(f.unlimitedStay, fullRev, "ACTIVE", "", 5_000_000, f.t1, time.Time{})

	session := func(entID, dev string, started, ended time.Time, down, up int64) string {
		return q(`INSERT INTO iam_v2.sessions(tenant_id,site_id,entitlement_id,device_id,state,started,ended,end_reason,bytes_down,bytes_up,mac)
			SELECT $1,$2,$3,$4,'ended',$5,$6,'ENTITLEMENT_ENDED',$7,$8,d.mac FROM iam_v2.devices d WHERE d.id=$4 RETURNING id::text`,
			f.tenant, f.site, entID, dev, started, ended, down, up)
	}
	bind := func(sess, entID string, seq int, from time.Time, until time.Time) {
		q(`INSERT INTO iam_v2.session_entitlement_bindings(tenant_id,site_id,session_id,entitlement_id,seq,bound_from,bound_until)
			VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, f.tenant, f.site, sess, entID, seq, from, until)
	}
	seq := int64(0)
	sample := func(sess string, at time.Time, down, up int64) {
		seq++
		q(`INSERT INTO iam_v2.accounting_records(tenant_id,site_id,session_id,sample_seq,bytes_up,bytes_down,sampled_at,ingested_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7) RETURNING id::text`, f.tenant, f.site, sess, seq, up, down, at)
	}

	// The carried session: 300k+30k under Free, 1.2M+100k under Full Stay (the sample exactly at the change belongs to the
	// package it moved to), 180k+20k under the after-check-out package (the closing-instant sample belongs to it).
	f.carried = session(f.grace, d1, f.t0.Add(5*time.Minute), f.t3, 300_000+1_200_000+180_000, 30_000+100_000+20_000)
	bind(f.carried, f.free, 1, f.t0.Add(5*time.Minute), f.t1)
	bind(f.carried, f.full, 2, f.t1, f.t2)
	bind(f.carried, f.grace, 3, f.t2, f.t3)
	sample(f.carried, f.t0.Add(10*time.Minute), 300_000, 30_000)
	sample(f.carried, f.t1, 700_000, 60_000)
	sample(f.carried, f.t1.Add(30*time.Minute), 500_000, 40_000)
	sample(f.carried, f.t2.Add(10*time.Minute), 100_000, 10_000)
	sample(f.carried, f.t3, 80_000, 10_000)

	// A second device only under Full Stay, never moved.
	f.single = session(f.full, d2, f.t1.Add(10*time.Minute), f.t2.Add(-10*time.Minute), 200_000, 0)
	bind(f.single, f.full, 1, f.t1.Add(10*time.Minute), f.t2.Add(-10*time.Minute))
	sample(f.single, f.t1.Add(20*time.Minute), 200_000, 0)

	// A carried session whose raw samples were already summarised: its bindings remain, its samples do not.
	f.purged = session(f.full, d1, f.t0.Add(-30*time.Minute), f.t1.Add(20*time.Minute), 70_000, 0)
	bind(f.purged, f.free, 1, f.t0.Add(-30*time.Minute), f.t1)
	bind(f.purged, f.full, 2, f.t1, f.t1.Add(20*time.Minute))

	// Room 1201: an old free package, then an unlimited one carrying almost all of the use.
	s2 := session(f.full2, d2, f.t1.Add(time.Minute), f.t2, 5_000_000, 0)
	bind(s2, f.full2, 1, f.t1.Add(time.Minute), f.t2)
	sample(s2, f.t1.Add(2*time.Minute), 5_000_000, 0)
	s3 := session(f.free2, d1, f.t0.Add(time.Minute), f.t1, 900_000, 0)
	bind(s3, f.free2, 1, f.t0.Add(time.Minute), f.t1)
	sample(s3, f.t0.Add(2*time.Minute), 900_000, 0)

	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func upGet(t *testing.T, s *server, path string, out any) {
	t.Helper()
	r := chi.NewRouter()
	r.Mount("/usage", s.usageRoutes())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

type upDetail struct {
	Source   usageSourceRow    `json:"source"`
	Packages []usagePackageRow `json:"packages"`
	Sessions []staySessionRow  `json:"sessions"`
}

func upCheckStay(t *testing.T, s *server, f upFixture) {
	t.Helper()
	period := "from=" + f.t0.Add(-2*time.Hour).Format(time.RFC3339) + "&to=" + time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	// THE LIST: the stay's total is every session once; the allowance beside it is the latest package's alone.
	var list struct {
		Data []usageSourceRow `json:"data"`
	}
	upGet(t, s, "/usage/sources?type=stay&"+period, &list)
	rows := map[string]usageSourceRow{}
	for _, x := range list.Data {
		rows[x.ID] = x
	}
	r, ok := rows[f.stay]
	if !ok {
		t.Fatalf("the seeded stay is not listed: %+v", list.Data)
	}
	if want := int64(1_680_000 + 150_000 + 200_000 + 70_000); r.Totals.BytesAll != want {
		t.Errorf("stay total = %d, want %d: each session once, whatever package it ended under", r.Totals.BytesAll, want)
	}
	if r.PackageName != "Checkout grace" || r.PackageType != "CHECKOUT_GRACE" || r.PackageStatus != "TERMINATED" || r.EndReason != "DATA" {
		t.Errorf("latest package = %q %q %q %q", r.PackageName, r.PackageType, r.PackageStatus, r.EndReason)
	}
	if r.QuotaBytes == nil || *r.QuotaBytes != 200_000 || r.ConsumedBytes == nil || *r.ConsumedBytes != 200_000 {
		t.Errorf("latest allowance %v used %v, want the after-check-out package's own 200000 of 200000", r.QuotaBytes, r.ConsumedBytes)
	}
	u, ok := rows[f.unlimitedStay]
	if !ok {
		t.Fatal("the unlimited stay is not listed")
	}
	if u.QuotaBytes != nil || u.PackageName != "Full Stay" || u.PackageStatus != "ACTIVE" {
		t.Errorf("an unlimited latest package must carry no allowance (the old free one's was shown): quota %v, %q %q",
			u.QuotaBytes, u.PackageName, u.PackageStatus)
	}
	var old struct {
		Data []usageStayRow `json:"data"`
	}
	upGet(t, s, "/usage/stays?limit=200&"+period, &old)
	for _, x := range old.Data {
		if x.StayID == f.unlimitedStay && (x.QuotaBytes != nil || x.PackageName != "Full Stay") {
			t.Errorf("the stays list still shows an old allowance for an unlimited stay: %+v", x)
		}
		if x.StayID == f.stay && (x.QuotaBytes == nil || *x.QuotaBytes != 200_000 || x.PackageType != "CHECKOUT_GRACE") {
			t.Errorf("the stays list disagrees with the source list: %+v", x)
		}
	}

	// THE DETAIL: every package with its own meter, oldest first.
	var d upDetail
	upGet(t, s, "/usage/sources/stay/"+f.stay+"?"+period, &d)
	if len(d.Packages) != 3 {
		t.Fatalf("packages = %+v", d.Packages)
	}
	type pk struct {
		id, name, reason string
		quota            *int64
		used             int64
	}
	q2 := func(v int64) *int64 { return &v }
	for i, want := range []pk{{f.free, "Free Internet", "SUPERSEDED", q2(1_000_000), 400_000}, {f.full, "Full Stay", "CONVERTED", nil, 1_500_000},
		{f.grace, "Checkout grace", "DATA", q2(200_000), 200_000}} {
		got := d.Packages[i]
		if got.EntitlementID != want.id || got.Name != want.name || got.EndReason != want.reason || got.UsedBytes != want.used ||
			(got.QuotaBytes == nil) != (want.quota == nil) || (got.QuotaBytes != nil && *got.QuotaBytes != *want.quota) {
			t.Errorf("package %d = %+v, want %+v", i, got, want)
		}
		if got.Activated == nil || (want.reason != "" && got.Terminated == nil) {
			t.Errorf("package %d has no dates: %+v", i, got)
		}
	}
	if d.Source.PackageName != "Checkout grace" {
		t.Errorf("detail header latest package = %q", d.Source.PackageName)
	}

	// THE CARRIED SESSION, split by its bindings, adds up to its own total; the purged one says it cannot be split.
	sess := map[string]staySessionRow{}
	for _, x := range d.Sessions {
		sess[x.SessionID] = x
	}
	c := sess[f.carried]
	if c.SplitExact == nil || !*c.SplitExact || len(c.Packages) != 3 {
		t.Fatalf("carried session: exact %v, packages %+v", c.SplitExact, c.Packages)
	}
	var sum int64
	for i, want := range []struct {
		ent      string
		down, up int64
	}{{f.free, 300_000, 30_000}, {f.full, 1_200_000, 100_000}, {f.grace, 180_000, 20_000}} {
		g := c.Packages[i]
		if g.EntitlementID != want.ent || g.BytesDown == nil || *g.BytesDown != want.down || *g.BytesUp != want.up {
			t.Errorf("carried session under package %d = %+v, want %+v", i, g, want)
			continue
		}
		sum += *g.BytesAll
	}
	if sum != c.BytesAll {
		t.Errorf("the split adds to %d, the session to %d", sum, c.BytesAll)
	}
	if g := c.Packages[2]; g.BytesAll != nil && *g.BytesAll > 200_000 {
		t.Errorf("the after-check-out stretch is %d bytes, over its 200000 allowance: the in-stay use is still counted on it", *g.BytesAll)
	}
	p := sess[f.purged]
	if p.SplitExact == nil || *p.SplitExact || len(p.Packages) != 2 || p.Packages[0].BytesAll != nil {
		t.Errorf("purged session: exact %v, packages %+v", p.SplitExact, p.Packages)
	}
	if one := sess[f.single]; one.Packages != nil || one.SplitExact != nil {
		t.Errorf("a session under one package needs no split: %+v", one)
	}
}

func TestIntegration_UsagePackagesAndCarriedSessions(t *testing.T) {
	p := testPool(t)
	defer p.Close()
	f := upSeed(t, p)
	upCheckStay(t, &server{db: p, tenantID: f.tenant, siteID: f.site}, f)
}

// The same reads as the production role: the binding history and every package table must be readable by svc_edged.
func TestIntegration_UsagePackagesRunAsTheProductionRole(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set")
	}
	p := testPool(t)
	defer p.Close()
	f := upSeed(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	role, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer role.Close()
	var who string
	if err := role.QueryRow(ctx, `SELECT current_user`).Scan(&who); err != nil || who != "svc_edged" {
		t.Fatalf("EDGED_ROLE_TEST_DSN connects as %q (%v), want svc_edged", who, err)
	}
	upCheckStay(t, &server{db: role, tenantID: f.tenant, siteID: f.site}, f)
}

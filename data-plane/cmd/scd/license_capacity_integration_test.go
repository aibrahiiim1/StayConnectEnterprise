//go:build integration

package main

// The licensed concurrent-guest cap under concurrency: drives the REAL gate (reserveLicensedSlot) from many goroutines against PostgreSQL,
// in the inserting transaction. Single-threaded tests cannot see a dropped lock or appliance scope.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var macSeq int64

// capFixture is minimal: one tenant, one site, and devices of named appliances; anything more adds unrelated failures.
type capFixture struct {
	pool       *pgxpool.Pool
	tenant     string
	site       string
	applianceA string
	applianceB string
}

func newCapFixture(t *testing.T) *capFixture {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping licensed-capacity integration")
	}
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)

	f := &capFixture{pool: p}
	// Two appliances under ONE tenant and site: the licence must be scoped to the appliance even when everything above it is shared.
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).
		Scan(&f.applianceA, &f.applianceB); err != nil {
		t.Fatalf("appliance ids: %v", err)
	}
	f.ensureTenantSite(t)
	return f
}

// mint creates one full commerce chain and returns its ACTIVE entitlement. Each admission needs its OWN entitlement: sharing one would trip the per-entitlement device limit, a different gate.
func (f *capFixture) mint(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	p := f.pool
	// Tenant and site are built for whichever schema this is: required text columns are discovered and filled, since hard-coding either shape passed locally and failed in CI.
	// mint returns its entitlement rather than storing it on the fixture: it runs concurrently, and a shared field handed every caller the same one.
	// The minimal real chain an iam_v2.sessions row requires; commerce writes open the same controlled operation the product opens.
	var ent string
	if err := pgx.BeginFunc(ctx, p, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `WITH
	  t   AS (SELECT $1::uuid AS id),
	  si  AS (SELECT $2::uuid AS id, $1::uuid AS tenant_id),
	  sp  AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code)
	          SELECT gen_random_uuid(), si.tenant_id, si.id, 'cap-plan-'||substr(gen_random_uuid()::text,1,8)
	          FROM si RETURNING id, tenant_id, site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id, 1
	          FROM sp RETURNING id, tenant_id, site_id),
	  ip  AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code)
	          SELECT gen_random_uuid(), spr.tenant_id, spr.site_id, 'cap-pkg-'||substr(gen_random_uuid()::text,1,8)
	          FROM spr RETURNING id, tenant_id, site_id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions
	            (id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type)
	          SELECT gen_random_uuid(), ip.tenant_id, ip.site_id, ip.id, 1, spr.id, 'GENERAL'
	          FROM ip, spr RETURNING id, tenant_id, site_id),
	  pu  AS (INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,trigger)
	          SELECT gen_random_uuid(), ipr.tenant_id, ipr.site_id, ipr.id, 'ADMIN_GRANT'
	          FROM ipr RETURNING id, tenant_id, site_id),
	  gp  AS (INSERT INTO iam_v2.guest_principals(id,tenant_id)
	          SELECT gen_random_uuid(), pu.tenant_id FROM pu RETURNING id, tenant_id),
	  -- ent_one_subject requires EXACTLY ONE subject. A principal is the least-coupled of the four, so the
	  -- fixture does not drag a stay, a voucher or an account into a test about the licence.
	  e   AS (INSERT INTO iam_v2.entitlements
	            (id,tenant_id,site_id,guest_principal_id,purchase_id,policy_snapshot,
	             service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,status)
	          SELECT gen_random_uuid(), pu.tenant_id, pu.site_id, gp.id, pu.id, '{}'::jsonb, spr.id,
	                 ipr.id, 'CONTINUOUS', 'FIXED_AT', 'ACTIVE'
	          FROM pu, spr, ipr, gp RETURNING id, tenant_id, site_id)
	SELECT e.id::text FROM e`, f.tenant, f.site).Scan(&ent); err != nil {
			return err
		}
		// ACTIVE is a transition, not a written value (the coherence constraint is DEFERRED), so insert and transition in ONE transaction as the real grant path does.
		_, err := tx.Exec(ctx,
			`SELECT iam_v2.apply_entitlement_transition($1::uuid,'ACTIVE',now(),'GRANT')`, ent)
		return err
	}); err != nil {
		t.Fatalf("fixture chain: %v", err)
	}
	return ent
}

// ensureTenantSite creates the tenant and site once, filling any NOT NULL text column the local schema requires.
func (f *capFixture) ensureTenantSite(t *testing.T) {
	t.Helper()
	if f.tenant != "" {
		return
	}
	ctx := context.Background()
	if err := f.pool.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).
		Scan(&f.tenant, &f.site); err != nil {
		t.Fatalf("ids: %v", err)
	}
	for _, spec := range []struct {
		table string
		cols  string
		vals  string
		args  []any
	}{
		{"public.tenants", "id", "$1::uuid", []any{f.tenant}},
		{"public.sites", "id, tenant_id", "$1::uuid, $2::uuid", []any{f.site, f.tenant}},
	} {
		cols, vals := spec.cols, spec.vals
		rows, err := f.pool.Query(ctx, `
		    SELECT column_name FROM information_schema.columns
		     WHERE table_schema = split_part($1,'.',1) AND table_name = split_part($1,'.',2)
		       AND is_nullable = 'NO' AND column_default IS NULL
		       AND data_type IN ('text','character varying')`, spec.table)
		if err != nil {
			t.Fatalf("%s columns: %v", spec.table, err)
		}
		var extra []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				t.Fatalf("scan: %v", err)
			}
			extra = append(extra, c)
		}
		rows.Close()
		for _, c := range extra {
			cols += ", " + c
			vals += ", 'cap-'||substr(gen_random_uuid()::text,1,8)"
		}
		if _, err := f.pool.Exec(ctx,
			"INSERT INTO "+spec.table+" ("+cols+") VALUES ("+vals+")", spec.args...); err != nil {
			t.Fatalf("%s insert: %v", spec.table, err)
		}
	}
}

func (f *capFixture) device(t *testing.T, appliance string, n int) string {
	t.Helper()
	var id string
	// Globally unique across the package: tests share one tenant and site, so n only traces failures and the counter ensures uniqueness.
	seq := atomic.AddInt64(&macSeq, 1)
	mac := fmt.Sprintf("02:ca:%02x:%02x:%02x:%02x",
		(seq>>24)&0xff, (seq>>16)&0xff, (seq>>8)&0xff, seq&0xff)
	_ = n
	if err := f.pool.QueryRow(context.Background(), `
	    INSERT INTO iam_v2.devices (id, tenant_id, site_id, appliance_id, mac, first_seen, last_seen)
	    VALUES (gen_random_uuid(), $1::uuid, $2::uuid, $3::uuid, $4::macaddr, now(), now())
	    RETURNING id::text`, f.tenant, f.site, appliance, mac).Scan(&id); err != nil {
		t.Fatalf("device: %v", err)
	}
	return id
}

// slot is a fully prepared admission, so the raced transaction holds ONLY the contended work (reserve, authorize, insert).
// Preparing inside the racing goroutine hid the race: the test passed even with the advisory lock removed.
type slot struct {
	entitlement string
	device      string
}

func (f *capFixture) prepare(t *testing.T, appliance string, n int) slot {
	t.Helper()
	return slot{entitlement: f.mint(t), device: f.device(t, appliance, n)}
}

func (f *capFixture) admitSlot(sl slot, limit int64, appliance string) error {
	ctx := context.Background()
	entitlement, deviceID := sl.entitlement, sl.device
	return pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		if err := reserveLicensedSlot(ctx, tx, appliance, limit); err != nil {
			return err
		}
		// Device admission and the session row cross their own controlled-writer families, as in production.
		for _, family := range []string{"device_auth", "session_binding"} {
			if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, family); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1::uuid,$2::uuid,now())`,
			entitlement, deviceID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
		    INSERT INTO iam_v2.sessions
		      (tenant_id, site_id, entitlement_id, device_id, credential_method, state, started)
		    VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,'VOUCHER','active',now())`,
			f.tenant, f.site, entitlement, deviceID)
		return err
	})
}

func (f *capFixture) admit(t *testing.T, limit int64, appliance string, n int) error {
	t.Helper()
	return f.admitSlot(f.prepare(t, appliance, n), limit, appliance)
}

// insertSession is split out so the forced-interleaving test can hold a transaction open between the capacity check and the insert.
func (f *capFixture) insertSession(ctx context.Context, tx pgx.Tx, sl slot, commit func(context.Context) error) error {
	for _, family := range []string{"device_auth", "session_binding"} {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, family); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1::uuid,$2::uuid,now())`,
		sl.entitlement, sl.device); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
	    INSERT INTO iam_v2.sessions
	      (tenant_id, site_id, entitlement_id, device_id, credential_method, state, started)
	    VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,'VOUCHER','active',now())`,
		f.tenant, f.site, sl.entitlement, sl.device); err != nil {
		return err
	}
	return commit(ctx)
}

func (f *capFixture) activeOn(t *testing.T, appliance string) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), `
	    SELECT count(*) FROM iam_v2.sessions se JOIN iam_v2.devices d ON d.id = se.device_id
	     WHERE d.appliance_id = $1::uuid AND se.ended IS NULL
	       AND se.state IN ('active','PENDING_ENFORCEMENT')`, appliance).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// The last slot: with the licence one short of the guests pressing Connect at once, exactly one may be admitted; without the advisory lock every contender sees the same count.
func TestLicensedCapacityIntegrationLastSlotIsRaceSafe(t *testing.T) {
	f := newCapFixture(t)
	const contenders = 16
	const limit int64 = 4

	for i := int64(0); i < limit-1; i++ {
		if err := f.admit(t, limit, f.applianceA, int(i)); err != nil {
			t.Fatalf("pre-fill %d: %v", i, err)
		}
	}
	if got := f.activeOn(t, f.applianceA); got != limit-1 {
		t.Fatalf("pre-fill left %d active, want %d", got, limit-1)
	}

	slots := make([]slot, contenders)
	for i := range slots {
		slots[i] = f.prepare(t, f.applianceA, 100+i)
	}
	admitted := make([]bool, contenders)
	refused := make([]bool, contenders)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			err := f.admitSlot(slots[i], limit, f.applianceA)
			switch {
			case err == nil:
				admitted[i] = true
			case errors.As(err, new(*licenseCapacityError)):
				refused[i] = true
			default:
				t.Errorf("contender %d failed for an unexpected reason: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	nAdmitted, nRefused := 0, 0
	for i := range admitted {
		if admitted[i] {
			nAdmitted++
		}
		if refused[i] {
			nRefused++
		}
	}
	if got := f.activeOn(t, f.applianceA); got > limit {
		t.Fatalf("the appliance is OVER its licence: %d active, limit %d", got, limit)
	}
	if nAdmitted != 1 {
		t.Fatalf("exactly one contender may take the last slot; %d were admitted", nAdmitted)
	}
	if nRefused != contenders-1 {
		t.Fatalf("every other contender must be refused with LICENSE_CAPACITY_REACHED; %d were", nRefused)
	}
	if got := f.activeOn(t, f.applianceA); got != limit {
		t.Fatalf("the licence should be exactly full: %d active, want %d", got, limit)
	}
}

// Forces the race instead of hoping for it: two hand-driven transactions are held between the capacity check and the insert.
// With the lock, tx B blocks inside reserve until tx A commits, then sees the true count and refuses.
// The stress test above still passed with the lock removed, so this one was verified by removing it.
func TestLicensedCapacityIntegrationForcedInterleavingCannotOverfill(t *testing.T) {
	f := newCapFixture(t)
	const limit int64 = 2
	if err := f.admit(t, limit, f.applianceA, 900); err != nil {
		t.Fatalf("pre-fill: %v", err)
	}
	slotA, slotB := f.prepare(t, f.applianceA, 901), f.prepare(t, f.applianceA, 902)

	ctx := context.Background()
	txA, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer txA.Rollback(ctx)

	if err := reserveLicensedSlot(ctx, txA, f.applianceA, limit); err != nil {
		t.Fatalf("A must find the last slot free: %v", err)
	}

	// B must block on A's advisory lock, so it runs in a goroutine and the test proves the blocking.
	type outcome struct{ err error }
	bDone := make(chan outcome, 1)
	bReserved := make(chan struct{})
	go func() {
		txB, err := f.pool.Begin(ctx)
		if err != nil {
			bDone <- outcome{err}
			return
		}
		defer txB.Rollback(ctx)
		err = reserveLicensedSlot(ctx, txB, f.applianceA, limit)
		close(bReserved)
		if err != nil {
			bDone <- outcome{err}
			return
		}
		bDone <- outcome{f.insertSession(ctx, txB, slotB, txB.Commit)}
	}()

	select {
	case <-bReserved:
		t.Fatal("B completed its capacity check while A was mid-admission: the check is not serialized, " +
			"so two admissions can both see the same last slot")
	case <-time.After(750 * time.Millisecond):
	}

	if err := f.insertSession(ctx, txA, slotA, txA.Commit); err != nil {
		t.Fatalf("A insert/commit: %v", err)
	}

	select {
	case res := <-bDone:
		if !errors.As(res.err, new(*licenseCapacityError)) {
			t.Fatalf("B must be refused with LICENSE_CAPACITY_REACHED once A committed; got %v", res.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("B never finished after A committed: the lock was not released with A's transaction")
	}

	if got := f.activeOn(t, f.applianceA); got != limit {
		t.Fatalf("the appliance is at %d active, want exactly its licence of %d", got, limit)
	}
}

// Two appliances do not share, serialize or leak capacity: the licence is per appliance, so a tenant- or site-scoped count or a broader lock fails this.
func TestLicensedCapacityIntegrationAppliancesAreIndependent(t *testing.T) {
	f := newCapFixture(t)
	const limit int64 = 2

	for i := 0; i < int(limit); i++ {
		if err := f.admit(t, limit, f.applianceA, 200+i); err != nil {
			t.Fatalf("filling appliance A: %v", err)
		}
	}
	if err := f.admit(t, limit, f.applianceA, 250); !errors.As(err, new(*licenseCapacityError)) {
		t.Fatalf("appliance A is full and must refuse; got %v", err)
	}

	for i := 0; i < int(limit); i++ {
		if err := f.admit(t, limit, f.applianceB, 300+i); err != nil {
			t.Fatalf("appliance B must admit its own guests while A is full: %v", err)
		}
	}
	if got := f.activeOn(t, f.applianceB); got != limit {
		t.Fatalf("appliance B active = %d, want %d", got, limit)
	}
	if got := f.activeOn(t, f.applianceA); got != limit {
		t.Fatalf("appliance B's guests must not count against A: A active = %d, want %d", got, limit)
	}
	if err := f.admit(t, limit, f.applianceB, 350); !errors.As(err, new(*licenseCapacityError)) {
		t.Fatalf("appliance B must enforce its OWN limit once full; got %v", err)
	}
}

// Two appliances racing at once: the LOCK must not be shared either; each must end at exactly its own limit.
func TestLicensedCapacityIntegrationAppliancesRaceIndependently(t *testing.T) {
	f := newCapFixture(t)
	const limit int64 = 2
	const contenders = 8

	for _, ap := range []string{f.applianceA, f.applianceB} {
		for i := int64(0); i < limit-1; i++ {
			if err := f.admit(t, limit, ap, 500+int(i)); err != nil {
				t.Fatalf("pre-fill: %v", err)
			}
		}
	}

	type req struct {
		appliance string
		sl        slot
	}
	reqs := make([]req, 0, contenders*2)
	for i := 0; i < contenders; i++ {
		reqs = append(reqs, req{f.applianceA, f.prepare(t, f.applianceA, 600+i)})
		reqs = append(reqs, req{f.applianceB, f.prepare(t, f.applianceB, 700+i)})
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, r := range reqs {
		wg.Add(1)
		go func(r req) {
			defer wg.Done()
			<-start
			if err := f.admitSlot(r.sl, limit, r.appliance); err != nil &&
				!errors.As(err, new(*licenseCapacityError)) {
				t.Errorf("unexpected failure on %s: %v", r.appliance, err)
			}
		}(r)
	}
	close(start)
	wg.Wait()

	for name, ap := range map[string]string{"A": f.applianceA, "B": f.applianceB} {
		if got := f.activeOn(t, ap); got != limit {
			t.Fatalf("appliance %s ended at %d active, want exactly %d", name, got, limit)
		}
	}
}

// An unlimited licence (zero or negative limit) takes no lock and counts nothing.
func TestLicensedCapacityIntegrationUnlimitedDoesNotSerialize(t *testing.T) {
	f := newCapFixture(t)
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := f.admit(t, 0, f.applianceA, 800+i); err != nil {
				t.Errorf("unlimited licence refused an admission: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := f.activeOn(t, f.applianceA); got != n {
		t.Fatalf("unlimited licence admitted %d of %d", got, n)
	}
}

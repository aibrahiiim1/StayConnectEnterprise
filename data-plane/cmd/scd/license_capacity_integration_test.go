//go:build integration

package main

// THE LICENSED CONCURRENT-GUEST CAP UNDER CONCURRENCY.
//
// The cap used to live in the superseded session manager, which took a transaction advisory lock on the
// APPLIANCE id and counted that appliance's active sessions before inserting. When the session authority
// moved to iam_v2 the cap had to move with it, and the first version of that move kept the count but dropped
// both the lock and the appliance scope. Neither loss is visible in a single-threaded test: the count is
// still correct and the limit is still enforced -- right up to the moment two guests press Connect at once.
//
// So these tests are adversarial by construction. They drive the REAL gate (reserveLicensedSlot) from many
// goroutines against a real PostgreSQL, in the same transaction that inserts the session, which is the only
// arrangement in which the answer means anything.

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

// macSeq makes every fixture device MAC unique across the whole package.
var macSeq int64

// capFixture is deliberately minimal: one tenant, one site, and devices belonging to named appliances. The
// cap is a property of the appliance and of the session's device, so nothing else is needed to exercise it,
// and anything else would only add ways for the test to fail for an unrelated reason.
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
	// Two appliance identities under ONE tenant and ONE site. That pairing is the point: the licence must be
	// scoped to the appliance even when everything above it is shared.
	if err := p.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).
		Scan(&f.applianceA, &f.applianceB); err != nil {
		t.Fatalf("appliance ids: %v", err)
	}
	// Once per fixture, before anything runs concurrently.
	f.ensureTenantSite(t)
	return f
}

// mint creates one full commerce chain and leaves its ACTIVE entitlement in f.entitle. Each admission gets
// its OWN entitlement: the licence is a property of the APPLIANCE, so the thing being raced must be distinct
// entitlements arriving at once, not several devices sharing one -- which would be the per-entitlement
// device limit refusing, a different gate entirely.
func (f *capFixture) mint(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	p := f.pool
	// TENANT AND SITE, BUILT FOR WHICHEVER SCHEMA THIS IS.
	//
	// The reconstruction schema requires public.tenants.slug and name and public.sites.code and name; the
	// Phase-3 integration schema the CI suites run against does not have them. Rather than pick one and fail
	// on the other, the required text columns are discovered and filled. Hard-coding either shape made this
	// suite pass locally and fail in CI for a reason that had nothing to do with the licence.
	// mint returns its entitlement rather than storing it on the fixture: it runs concurrently, and a shared
	// field handed every caller the SAME entitlement, so the per-entitlement device limit refused instead of
	// the licence -- the wrong gate, and a green run that proved nothing.
	// The minimal real chain an iam_v2.sessions row requires: tenant, site, service plan revision, package
	// revision, purchase, entitlement. Every column filled here is NOT NULL with no default -- nothing is
	// invented for convenience, and nothing beyond the chain is created, because anything extra would only
	// add ways for this test to fail for a reason that is not the licence.
	// The commerce writes below cross the accepted CONTROLLED-WRITER boundary, so the fixture opens the same
	// controlled operation the product opens rather than being exempted from it. A fixture that could write
	// where the runtime cannot would be testing a database this product never runs against.
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
		// ACTIVE is not a value you write; it is a state you transition into, and the coherence constraint
		// that says so is DEFERRED -- which is why the real grant path inserts the entitlement and applies
		// its initial transition in ONE transaction. The fixture does the same rather than back-dating a
		// status onto the row.
		_, err := tx.Exec(ctx,
			`SELECT iam_v2.apply_entitlement_transition($1::uuid,'ACTIVE',now(),'GRANT')`, ent)
		return err
	}); err != nil {
		t.Fatalf("fixture chain: %v", err)
	}
	return ent
}

// ensureTenantSite creates the fixture's tenant and site once, filling any NOT NULL text column the local
// schema happens to require.
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

// device creates a device owned by the given appliance and returns its id.
func (f *capFixture) device(t *testing.T, appliance string, n int) string {
	t.Helper()
	var id string
	// Globally unique across every fixture and every test in the package. The caller's n is kept only so a
	// failure can be traced back to which admission it was; uniqueness comes from the counter, because these
	// tests deliberately share one tenant and site and would otherwise collide on the device MAC.
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

// admit runs the REAL gate and, if it admits, inserts the session -- in ONE transaction, which is what the
// production path does and the only arrangement under which the advisory lock means anything.
// slot is a fully prepared admission: its entitlement and device already exist, so the transaction that
// races contains ONLY the contended work -- reserve, authorize, insert. Preparing inside the racing goroutine
// was enough to hide the race entirely: the setup dominated the transaction and the contended window shrank
// to almost nothing, so the test passed even with the advisory lock removed. It proved nothing.
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
		// Device admission and the session row cross their own accepted controlled-writer families, exactly
		// as they do in the production activation path.
		for _, family := range []string{"device_auth", "session_binding"} {
			if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, family); err != nil {
				return err
			}
		}
		// The accepted domain refuses a session for a device with no authorization binding on the
		// entitlement, so admission is a real step here as it is in production.
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

// admit prepares and admits in one step. Used where there is no race to preserve.
func (f *capFixture) admit(t *testing.T, limit int64, appliance string, n int) error {
	t.Helper()
	return f.admitSlot(f.prepare(t, appliance, n), limit, appliance)
}

// insertSession performs the admission's writes on an existing transaction and then commits it. Split out so
// the forced-interleaving test can hold a transaction open between the capacity check and the insert.
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

// THE LAST SLOT. With the licence one short of the number of guests pressing Connect at the same instant,
// exactly one may be admitted. Without the advisory lock every contender reads the same pre-insert count,
// every one finds the last slot free, and the appliance ends up over its licence by the number of
// simultaneous logins -- the failure this test exists to make impossible to reintroduce.
func TestLicensedCapacityIntegrationLastSlotIsRaceSafe(t *testing.T) {
	f := newCapFixture(t)
	const contenders = 16
	const limit int64 = 4

	// Fill to one slot short, sequentially, so the race is only over the final admission.
	for i := int64(0); i < limit-1; i++ {
		if err := f.admit(t, limit, f.applianceA, int(i)); err != nil {
			t.Fatalf("pre-fill %d: %v", i, err)
		}
	}
	if got := f.activeOn(t, f.applianceA); got != limit-1 {
		t.Fatalf("pre-fill left %d active, want %d", got, limit-1)
	}

	// Prepared BEFORE the race so the raced transaction is only the contended work.
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
			<-start // released together; staggering them would test nothing
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

// THE RACE, FORCED RATHER THAN HOPED FOR.
//
// The stress test above releases many goroutines at once and checks the outcome. That is worth having, but it
// is not proof: with the advisory lock REMOVED it still passed, three runs in a row, because each admission
// is a millisecond of work and they happened not to interleave at the one instruction that matters.
//
// So this test does not leave the interleaving to chance. It drives two transactions by hand and holds them
// both between the capacity check and the insert -- the exact window a lock exists to close:
//
//	tx A: reserve (sees the last slot free)
//	tx B: reserve (WITHOUT a lock, also sees the last slot free)
//	tx A: insert, commit
//	tx B: insert, commit      -> the appliance is now one over its licence
//
// With the lock in place tx B blocks inside reserve until tx A commits, then sees the true count and refuses.
// A test that cannot fail when the lock is removed is not evidence that the lock is doing anything, and this
// one was checked by removing it.
func TestLicensedCapacityIntegrationForcedInterleavingCannotOverfill(t *testing.T) {
	f := newCapFixture(t)
	const limit int64 = 2
	if err := f.admit(t, limit, f.applianceA, 900); err != nil {
		t.Fatalf("pre-fill: %v", err)
	}
	// One slot left, two contenders prepared in advance.
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

	// B runs concurrently and must not be able to claim the same slot. Its reserve is expected to BLOCK on
	// A's advisory lock, so it runs in a goroutine and the test proves the blocking rather than assuming it.
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

	// B must still be blocked while A holds the lock. Without the lock it would already have reserved.
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

// TWO APPLIANCES DO NOT SHARE, SERIALIZE OR LEAK CAPACITY. The licence is per appliance, so a full appliance
// must not stop a second one -- same customer, same site -- from admitting its own guests, and the second
// appliance's guests must not count against the first. A tenant- or site-scoped count fails this, and so does
// a lock taken on anything broader than the appliance.
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

	// B is untouched by A being full.
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

// TWO APPLIANCES RACING AT ONCE. The independence above is proven sequentially; this proves the LOCK is not
// shared either. Both appliances contend for their own last slot simultaneously, and each must end at exactly
// its own limit -- neither over (a lost race) nor under (one appliance's lock blocking the other's admission
// until it gave up).
func TestLicensedCapacityIntegrationAppliancesRaceIndependently(t *testing.T) {
	f := newCapFixture(t)
	const limit int64 = 2
	const contenders = 8

	// Each appliance filled to one short of its own limit.
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

// AN UNLIMITED LICENCE TAKES NO LOCK AND COUNTS NOTHING. A zero or negative limit means unlimited, and must
// not serialize admissions behind a lock that can never refuse anything.
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

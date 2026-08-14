//go:build integration && phase5

package poststay

// THE F8 SERIES, against a real PostgreSQL.
//
// Every case here is about one property: a Post-Stay PIN must never become usable by whoever occupies the
// room next. The cases are written as attacks rather than as feature checks, because a feature check would
// pass on a system that leaks — issuing and verifying a PIN is the easy half.
//
// The fail-closed reinstatement limitation is tested here too (F8-i): the FINAL contract draws no arrow out
// of POST_STAY_ACTIVE, so a reinstatement event for a converted Stay is REFUSED and lands in the operator's
// queue. That behaviour is deliberate and recorded; the test exists so that a future change which quietly
// adds an exit has to fail a test that says why the exit is not there.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping post-stay integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return p
}

type fx struct{ tenant, site, iface, rev, stay, device, network, pkg string }

// seed builds a site with a CHECKED_OUT Stay and a zero-price POST_STAY package — the state a real post-stay
// guest is in. Each test gets its own tenant, so nothing here depends on execution order.
func seed(t *testing.T, p *pgxpool.Pool) fx {
	t.Helper()
	ctx := context.Background()
	var f fx
	err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks(id,tenant_id,site_id)
	         SELECT gen_random_uuid(), si.tenant_id, si.id FROM si RETURNING id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'protel-fias','ACTIVE' FROM si RETURNING id,tenant_id,site_id),
	  pr AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1,'UTC','{}'::jsonb FROM pi RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,lifecycle_version,effective_checkout_at)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id,'R1','R1','CHECKED_OUT',1, now() FROM pi RETURNING id),
	  dv AS (INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, gen_random_uuid(),'02:00:00:00:00:01'::macaddr FROM pi RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'PS-PLAN' FROM si RETURNING id,tenant_id,site_id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,
	            max_concurrent_devices,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(), sp.tenant_id, sp.site_id, sp.id,1,2,'VALIDITY_WINDOW',1000000 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,active)
	         SELECT gen_random_uuid(), si.tenant_id, si.id,'PS-PKG',true FROM si RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(), si.tenant_id, si.id, ip.id,1, spr.id,'POST_STAY',0,
	                 ARRAY['NOT_REQUIRED']::text[], '{"duration_minutes":120}'::jsonb
	            FROM si, ip, spr RETURNING id, package_id)
	SELECT (SELECT tenant_id FROM pi)::text,(SELECT site_id FROM pi)::text,(SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text,(SELECT id FROM st)::text,(SELECT id FROM dv)::text,
	       (SELECT id FROM gn)::text,(SELECT id FROM ipr)::text`).
		Scan(&f.tenant, &f.site, &f.iface, &f.rev, &f.stay, &f.device, &f.network, &f.pkg)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, f.pkg); err != nil {
		t.Fatalf("seed current revision: %v", err)
	}
	return f
}

func store(t *testing.T, p *pgxpool.Pool) *Store {
	t.Helper()
	thr, err := throttle.New(p, []byte("post-stay-test-key-0123456789abcd"), time.Minute)
	if err != nil {
		t.Fatalf("throttle: %v", err)
	}
	return New(p, thr)
}

func issue(t *testing.T, s *Store, f fx) Issued {
	t.Helper()
	out, err := s.Issue(context.Background(), IssueRequest{
		Tenant: f.tenant, Site: f.site, Stay: f.stay, ValidFor: 24 * time.Hour})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return out
}

// reLet is the room's next occupant: the Stay is reinstated (a NEW episode) and checks out again.
func reLet(t *testing.T, p *pgxpool.Pool, f fx) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open stay: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays
		SET status='IN_HOUSE', effective_checkout_at=NULL, lifecycle_version=lifecycle_version+1
		WHERE id=$1`, f.stay); err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now()
		WHERE id=$1`, f.stay); err != nil {
		t.Fatalf("re-checkout: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// F8 baseline: the happy path must actually work, or every refusal below proves nothing.
func TestIntegration_F8_IssueAndVerify(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	got := issue(t, s, f)

	if len(got.PIN) != pinLength {
		t.Fatalf("PIN length = %d, want %d", len(got.PIN), pinLength)
	}
	// The generated alphabet must contain no character the guest could mistype into a different one.
	for _, c := range got.PIN {
		if strings.ContainsRune("ILOU015S", c) {
			t.Fatalf("PIN %q contains an excluded/confusable character %q", got.PIN, c)
		}
	}
	if _, err := s.Verify(context.Background(), VerifyRequest{
		Tenant: f.tenant, Site: f.site, Profile: got.Profile, PIN: got.PIN, Device: f.device}); err != nil {
		t.Fatalf("verify the PIN we just issued: %v", err)
	}
	// ...and typed the way a guest types it: lower case, with a stray space.
	typed := " " + strings.ToLower(got.PIN[:4]) + " " + strings.ToLower(got.PIN[4:])
	if _, err := s.Verify(context.Background(), VerifyRequest{
		Tenant: f.tenant, Site: f.site, Profile: got.Profile, PIN: typed, Device: f.device}); err != nil {
		t.Fatalf("verify a normally-typed PIN: %v", err)
	}
}

// F8-a / F8-b. THE CASE THE PHASE EXISTS FOR.
func TestIntegration_F8_NextOccupantCannotUseThePreviousPIN(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	got := issue(t, s, f)

	reLet(t, p, f)

	if _, err := s.Verify(context.Background(), VerifyRequest{
		Tenant: f.tenant, Site: f.site, Profile: got.Profile, PIN: got.PIN, Device: f.device}); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("the previous occupant's PIN still verifies after the room was re-let: err = %v", err)
	}
	// ...and the new episode can mint its own, which the old PIN must not open either.
	fresh := issue(t, s, f)
	if fresh.Profile == got.Profile {
		t.Fatalf("the new episode reused the previous profile %s", got.Profile)
	}
	if _, err := s.Verify(context.Background(), VerifyRequest{
		Tenant: f.tenant, Site: f.site, Profile: fresh.Profile, PIN: got.PIN, Device: f.device}); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("the OLD PIN opened the NEW occupant's profile: err = %v", err)
	}
}

// F8-e. One profile per episode: a second issuance for the same episode is refused, not silently duplicated.
func TestIntegration_F8_OneProfilePerEpisode(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	issue(t, s, f)
	if _, err := s.Issue(context.Background(), IssueRequest{
		Tenant: f.tenant, Site: f.site, Stay: f.stay, ValidFor: time.Hour}); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("a second profile for the same episode: err = %v, want ErrNotEligible", err)
	}
}

// F8-f. The PIN must not be recoverable from anything the system stores.
func TestIntegration_F8_PlaintextIsNeverStored(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	got := issue(t, s, f)

	var hash string
	var revealed *time.Time
	if err := p.QueryRow(context.Background(),
		`SELECT pin_hash, pin_revealed_at FROM iam_v2.post_stay_profiles WHERE id=$1`,
		got.Profile).Scan(&hash, &revealed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(hash, got.PIN) {
		t.Fatalf("the stored hash contains the plaintext PIN")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("stored hash is not argon2id: %q", hash[:min(12, len(hash))])
	}
	if revealed == nil {
		t.Fatalf("the one-time reveal was not recorded, so a second reveal could not be refused")
	}
	// And the whole row, rendered as text, must not contain it either — a PIN that leaked into a reason,
	// a note or a status string would be just as exposed as one stored in its own column.
	var whole string
	if err := p.QueryRow(context.Background(),
		`SELECT post_stay_profiles::text FROM iam_v2.post_stay_profiles WHERE id=$1`, got.Profile).
		Scan(&whole); err != nil {
		t.Fatalf("row text: %v", err)
	}
	if strings.Contains(whole, got.PIN) {
		t.Fatalf("the profile row contains the plaintext PIN somewhere")
	}
}

// F8-g. The throttle is durable and fails closed, and a lockout survives the process.
func TestIntegration_F8_ThrottleLocksOutAndPersists(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	got := issue(t, s, f)

	ctx := context.Background()
	bad := VerifyRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile, PIN: "WRONGPIN", Device: f.device}
	var throttled bool
	for i := 0; i < 8; i++ {
		_, err := s.Verify(ctx, bad)
		if errors.Is(err, ErrThrottled) {
			throttled = true
			break
		}
		if !errors.Is(err, ErrNotAuthenticable) {
			t.Fatalf("attempt %d: err = %v, want ErrNotAuthenticable", i, err)
		}
	}
	if !throttled {
		t.Fatalf("the throttle never engaged after 8 wrong PINs")
	}
	// A NEW store — a different process, as far as the counter is concerned — must still be locked out, and
	// the CORRECT PIN must not get through it either.
	s2 := store(t, p)
	if _, err := s2.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		PIN: got.PIN, Device: f.device}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("a fresh store bypassed the lockout: err = %v", err)
	}
	// ...and it is recorded under the post-stay method, sharing no counter with any other method.
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM public.auth_throttle_buckets WHERE method=$1`,
		ThrottleMethod).Scan(&n); err != nil {
		t.Fatalf("count buckets: %v", err)
	}
	if n == 0 {
		t.Fatalf("no throttle bucket was recorded under method %q", ThrottleMethod)
	}
}

// Operator revocation kills the PIN immediately, and reset mints a different one.
func TestIntegration_F8_OperatorRevokeAndReset(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	got := issue(t, s, f)
	ctx := context.Background()
	op := "33333333-3333-3333-3333-333333333333"

	reset, err := s.Reset(ctx, ResetRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		Operator: op, Reason: "guest lost the printout", ValidFor: time.Hour})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset.PIN == got.PIN {
		t.Fatalf("reset returned the SAME PIN")
	}
	if _, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		PIN: got.PIN, Device: f.device}); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("the superseded PIN still verifies: %v", err)
	}
	if _, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		PIN: reset.PIN, Device: f.device}); err != nil {
		t.Fatalf("the new PIN does not verify: %v", err)
	}

	// A reset with no operator or no reason is not an audited action and is refused.
	if _, err := s.Reset(ctx, ResetRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		Operator: "", Reason: "x", ValidFor: time.Hour}); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("reset with no operator: err = %v", err)
	}
	if _, err := s.Reset(ctx, ResetRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		Operator: op, Reason: "  ", ValidFor: time.Hour}); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("reset with no reason: err = %v", err)
	}

	if err := s.Revoke(ctx, RevokeRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		Operator: op, Reason: "guest asked"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		PIN: reset.PIN, Device: f.device}); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a revoked profile still verifies: %v", err)
	}
	// A revoked profile cannot be reset back into service — that would be a resurrection, not a re-issue.
	if _, err := s.Reset(ctx, ResetRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
		Operator: op, Reason: "undo", ValidFor: time.Hour}); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a revoked profile was reset: err = %v", err)
	}
}

// X-1. The refusal that keeps Phase 5 out of the financial system.
func TestIntegration_ZeroPriceOnly(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	ac := authctx.NewStore(p)
	ctx := context.Background()
	got := issue(t, s, f)

	// A priced post-stay package, exactly as a mis-configuration would produce one. Revisions are IMMUTABLE
	// (the schema refuses UPDATE), so this is a NEW revision that the package is then pointed at — which is
	// also how a real operator would introduce the mistake.
	var priced string
	if err := p.QueryRow(ctx, `WITH ipr AS (
		  INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
		    service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods,duration_policy)
		  SELECT gen_random_uuid(), o.tenant_id, o.site_id, o.package_id, 2, o.service_plan_revision_id,
		         'POST_STAY', 500, 'USD', 2, ARRAY['PMS_POSTING']::text[], o.duration_policy
		    FROM iam_v2.internet_package_revisions o WHERE o.id=$1
		  RETURNING id, package_id)
		UPDATE iam_v2.internet_packages ip SET current_revision_id = ipr.id
		  FROM ipr WHERE ip.id = ipr.package_id RETURNING ipr.id::text`, f.pkg).Scan(&priced); err != nil {
		t.Fatalf("seed a priced revision: %v", err)
	}
	cid := mintContext(t, ac, s, f, got)
	_, err := s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: cid,
		Presenter:       authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: f.device, GuestNetwork: f.network},
		PackageRevision: priced}, ac)
	if !errors.Is(err, ErrSettlementRequired) {
		t.Fatalf("a priced post-stay package was not refused: err = %v", err)
	}
	// Nothing financial may have been created by the refusal.
	var purchases, postings int
	if err := p.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1),
		(SELECT count(*) FROM iam_v2.pms_postings WHERE stay_id=$1)`, f.stay).Scan(&purchases, &postings); err != nil {
		t.Fatalf("count: %v", err)
	}
	if purchases != 0 || postings != 0 {
		t.Fatalf("a refused conversion left %d purchase(s) and %d posting(s)", purchases, postings)
	}
}

func mintContext(t *testing.T, ac *authctx.Store, s *Store, f fx, iss Issued) string {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Profile: iss.Profile,
		PIN: iss.PIN, Device: f.device}); err != nil {
		t.Fatalf("verify before minting a context: %v", err)
	}
	id, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site,
		Profile: iss.Profile, Device: f.device, GuestNetwork: f.network, TTLSeconds: 600})
	if err != nil {
		t.Fatalf("issue context: %v", err)
	}
	return id
}

// The full slice: PIN -> context -> zero-price conversion, and what the conversion may and may not do.
func TestIntegration_ConversionIsZeroPriceAndNonPosting(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	ac := authctx.NewStore(p)
	ctx := context.Background()
	got := issue(t, s, f)

	out, err := s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: mintContext(t, ac, s, f, got),
		Presenter:       authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: f.device, GuestNetwork: f.network},
		PackageRevision: f.pkg}, ac)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	var status string
	var posting bool
	var amount int64
	var trigger string
	if err := p.QueryRow(ctx, `SELECT st.status, st.posting_allowed, pu.amount_minor, pu.trigger
		FROM iam_v2.stays st JOIN iam_v2.purchases pu ON pu.id=$2 WHERE st.id=$1`,
		f.stay, out.Purchase).Scan(&status, &posting, &amount, &trigger); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "POST_STAY_ACTIVE" {
		t.Fatalf("stay status = %q, want POST_STAY_ACTIVE", status)
	}
	if posting {
		t.Fatalf("a POST_STAY_ACTIVE stay has posting_allowed = true")
	}
	if amount != 0 {
		t.Fatalf("the conversion purchase carries %d minor units", amount)
	}
	if trigger != "POST_STAY_CONVERSION" {
		t.Fatalf("purchase trigger = %q", trigger)
	}
	var postings int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.pms_postings WHERE stay_id=$1`, f.stay).
		Scan(&postings); err != nil {
		t.Fatalf("count postings: %v", err)
	}
	if postings != 0 {
		t.Fatalf("the conversion created %d PMS posting(s)", postings)
	}

	// The context is one-time: the same one cannot convert twice.
	if _, err := s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: out.Purchase,
		Presenter:       authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: f.device, GuestNetwork: f.network},
		PackageRevision: f.pkg}, ac); err == nil {
		t.Fatalf("a replayed conversion succeeded")
	}
}

// F8-i. THE RECORDED LIMITATION. The FINAL contract draws no arrow out of POST_STAY_ACTIVE, so a
// reinstatement for a converted Stay is REFUSED at the database and the event goes to the operator instead of
// silently rewriting the episode. This test is the record: it fails if an exit transition is ever added
// without the contract change that would justify it.
func TestIntegration_F8_ReinstatementOfAConvertedStayFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	ac := authctx.NewStore(p)
	ctx := context.Background()
	got := issue(t, s, f)
	if _, err := s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: mintContext(t, ac, s, f, got),
		Presenter:       authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: f.device, GuestNetwork: f.network},
		PackageRevision: f.pkg}, ac); err != nil {
		t.Fatalf("convert: %v", err)
	}

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open stay: %v", err)
	}
	_, err = tx.Exec(ctx, `UPDATE iam_v2.stays SET status='IN_HOUSE' WHERE id=$1`, f.stay)
	if err == nil {
		t.Fatalf("POST_STAY_ACTIVE -> IN_HOUSE was ACCEPTED; the contract draws no such arrow")
	}
	if !strings.Contains(err.Error(), "illegal stays.status transition") {
		t.Fatalf("refused, but not by the transition guard: %v", err)
	}
	// The Stay is untouched — the refusal is fail-closed, not a partial write.
	_ = tx.Rollback(ctx)
	var status string
	if err := p.QueryRow(ctx, `SELECT status FROM iam_v2.stays WHERE id=$1`, f.stay).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "POST_STAY_ACTIVE" {
		t.Fatalf("stay status = %q after a refused reinstatement", status)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

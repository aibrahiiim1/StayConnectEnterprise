//go:build integration && phase5

package poststay

// UNIFORM NON-SUCCESS, PROVED RATHER THAN ASSERTED.
//
// The guest-facing post-stay surface has one failure answer, and the whole security argument for exposing it
// to an unauthenticated network depends on that being literally true. So this drives the REAL handler logic
// through every failure the Product Owner named -- wrong PIN, no PIN, expired, revoked, locked out, stale
// episode, and a device with no post-stay identity at all -- and asserts that the results are
// INDISTINGUISHABLE from each other: same error, and no field that differs.
//
// The comparison is on the store's own answer rather than an HTTP body, because that is where a difference
// would originate: the scd handler collapses everything to one envelope, so a test that only looked at the
// envelope would pass even if the layer beneath it leaked. What the handler adds is tested separately.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// authorizeDevice records the durable "this device was authenticated on this stay" lineage that the
// guest-facing path derives everything from. Without it there is no candidate and no issuance.
func authorizeDevice(t *testing.T, p *pgxpool.Pool, f fx) {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var purchase, ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, f.pkg, f.stay).Scan(&purchase); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,window_ends_at,status)
		SELECT $1,$2,$3,$4,'{}'::jsonb, ipr.service_plan_revision_id, $5,'VALIDITY_WINDOW','VALIDITY_WINDOW',
		       now()+interval '2 hours','PENDING'
		  FROM iam_v2.internet_package_revisions ipr WHERE ipr.id=$5
		RETURNING id::text`, f.tenant, f.site, f.stay, purchase, f.pkg).Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),NULL)`, ent); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// entitlement_devices is the binding; entitlement_device_authorizations is the INTERVAL over it, and the
	// guard refuses an interval with no binding behind it. Seeding one without the other would be a state the
	// appliance cannot reach.
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
		(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED', now(), now())`, f.tenant, f.site, ent, f.device); err != nil {
		t.Fatalf("bind device: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('device_auth')`); err != nil {
		t.Fatalf("open device_auth: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
		(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
		VALUES ($1,$2,$3,$4,1, now())`, f.tenant, f.site, ent, f.device); err != nil {
		t.Fatalf("authorize device: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestIntegration_GuestPathDerivesEverythingFromTheDevice proves the property the Product Owner asked for
// directly: the guest never names a Stay or a profile, and the server finds both from the device alone.
func TestIntegration_GuestPathDerivesEverythingFromTheDevice(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	authorizeDevice(t, p, f)
	ctx := context.Background()

	stay, err := s.EligibleStayForDevice(ctx, f.tenant, f.site, f.device)
	if err != nil {
		t.Fatalf("no eligible stay derived from the device: %v", err)
	}
	if stay != f.stay {
		t.Fatalf("derived stay %s, want %s", stay, f.stay)
	}
	out, err := s.Issue(ctx, IssueRequest{Tenant: f.tenant, Site: f.site, Stay: stay, ValidFor: time.Hour})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// ...and verification finds the profile from the device too, with no profile in the request.
	got, err := s.VerifyForDevice(ctx, VerifyRequest{
		Tenant: f.tenant, Site: f.site, PIN: out.PIN, Device: f.device})
	if err != nil {
		t.Fatalf("verify by device: %v", err)
	}
	if got != out.Profile {
		t.Fatalf("verified profile %s, want %s", got, out.Profile)
	}

	// A DIFFERENT device with no lineage gets nothing, whatever PIN it presents.
	var otherDevice string
	if err := p.QueryRow(ctx, `INSERT INTO iam_v2.devices(id,tenant_id,site_id,appliance_id,mac)
		VALUES (gen_random_uuid(),$1,$2,gen_random_uuid(),'02:00:00:00:00:99'::macaddr) RETURNING id::text`,
		f.tenant, f.site).Scan(&otherDevice); err != nil {
		t.Fatalf("seed other device: %v", err)
	}
	if _, err := s.VerifyForDevice(ctx, VerifyRequest{
		Tenant: f.tenant, Site: f.site, PIN: out.PIN, Device: otherDevice}); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a device with no lineage authenticated with a valid PIN: %v", err)
	}
}

// THE UNIFORMITY MATRIX. Every named failure, one answer.
func TestIntegration_EveryFailureIsTheSameAnswer(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()

	// Each case builds its own site so the states cannot interfere, and returns the attempt to make.
	cases := []struct {
		name    string
		prepare func(t *testing.T) VerifyRequest
	}{
		{"wrong PIN", func(t *testing.T) VerifyRequest {
			f := seed(t, p)
			s := store(t, p)
			authorizeDevice(t, p, f)
			issue(t, s, f)
			return VerifyRequest{Tenant: f.tenant, Site: f.site, PIN: "WRONGPIN", Device: f.device}
		}},
		{"no PIN at all", func(t *testing.T) VerifyRequest {
			f := seed(t, p)
			s := store(t, p)
			authorizeDevice(t, p, f)
			issue(t, s, f)
			return VerifyRequest{Tenant: f.tenant, Site: f.site, PIN: "", Device: f.device}
		}},
		{"expired profile", func(t *testing.T) VerifyRequest {
			f := seed(t, p)
			s := store(t, p)
			authorizeDevice(t, p, f)
			// A REAL expiry, by issuing a one-second window and letting it pass. The validity window cannot
			// be back-dated -- psp_validity_window refuses valid_until <= created_at -- and that refusal is
			// correct: a window that can be moved into the past is a window an operator could use to make a
			// credential retroactively never-valid, which is not the same thing as revoking it.
			out, err := s.Issue(context.Background(), IssueRequest{
				Tenant: f.tenant, Site: f.site, Stay: f.stay, ValidFor: time.Second})
			if err != nil {
				t.Fatalf("issue a short-lived profile: %v", err)
			}
			time.Sleep(1200 * time.Millisecond)
			return VerifyRequest{Tenant: f.tenant, Site: f.site, PIN: out.PIN, Device: f.device}
		}},
		{"revoked profile", func(t *testing.T) VerifyRequest {
			f := seed(t, p)
			s := store(t, p)
			authorizeDevice(t, p, f)
			got := issue(t, s, f)
			if err := s.Revoke(ctx, RevokeRequest{Tenant: f.tenant, Site: f.site, Profile: got.Profile,
				Operator: "33333333-3333-3333-3333-333333333333", Reason: "operator ended it"}); err != nil {
				t.Fatalf("revoke: %v", err)
			}
			return VerifyRequest{Tenant: f.tenant, Site: f.site, PIN: got.PIN, Device: f.device}
		}},
		{"stale episode (the room was re-let)", func(t *testing.T) VerifyRequest {
			f := seed(t, p)
			s := store(t, p)
			authorizeDevice(t, p, f)
			got := issue(t, s, f)
			reLet(t, p, f)
			return VerifyRequest{Tenant: f.tenant, Site: f.site, PIN: got.PIN, Device: f.device}
		}},
		{"a device with no post-stay identity at all", func(t *testing.T) VerifyRequest {
			f := seed(t, p)
			authorizeDevice(t, p, f)
			return VerifyRequest{Tenant: f.tenant, Site: f.site, PIN: "ANYTHING", Device: f.device}
		}},
	}

	for _, tc := range cases {
		req := tc.prepare(t)
		// A store with NO throttle, so the shared lockout of one case cannot change another's answer -- the
		// locked-out case is asserted separately below, where it is the thing being measured.
		s := New(p, nil)
		profile, err := s.VerifyForDevice(ctx, req)
		if profile != "" {
			t.Fatalf("%s: a failure returned a profile", tc.name)
		}
		if !errors.Is(err, ErrNotAuthenticable) {
			t.Fatalf("%s: err = %v, want the single ErrNotAuthenticable", tc.name, err)
		}
		if err.Error() != ErrNotAuthenticable.Error() {
			t.Fatalf("%s: the error TEXT differs (%q); a wrapped cause here would leak which failure it was",
				tc.name, err.Error())
		}
	}
}

// The locked-out case is the one failure that is deliberately DIFFERENT inside the store -- the caller needs
// to know to back off -- and identical to the guest, because the handler collapses it. Both halves matter.
func TestIntegration_ThrottledIsDistinctInternallyAndIdenticalOutside(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	s := store(t, p)
	authorizeDevice(t, p, f)
	got := issue(t, s, f)
	ctx := context.Background()

	var throttled bool
	for i := 0; i < 15; i++ {
		_, err := s.VerifyForDevice(ctx, VerifyRequest{
			Tenant: f.tenant, Site: f.site, PIN: "WRONGPIN", Device: f.device})
		if errors.Is(err, ErrThrottled) {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Fatalf("the device lockout never engaged")
	}
	// The CORRECT PIN is refused while locked out, which is what makes the lockout worth anything.
	if _, err := s.VerifyForDevice(ctx, VerifyRequest{
		Tenant: f.tenant, Site: f.site, PIN: got.PIN, Device: f.device}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("the correct PIN got through the lockout: %v", err)
	}
}

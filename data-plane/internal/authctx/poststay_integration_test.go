//go:build integration && phase5

package authctx

// POST_STAY_PIN AUTH CONTEXTS.
//
// The PMS method and the post-stay method share a table and almost nothing else, and the tests that matter
// here are the ones that would have passed if POST_STAY_PIN had simply been added to the existing PMS path:
//
//   * a PMS context requires IN_HOUSE, an interface Revision, occupancy evidence and freshness. A post-stay
//     guest has checked OUT and was never proven from occupancy evidence, so reusing that issuer would have
//     rejected every legitimate post-stay context;
//   * ConsumeTx re-verifies live subject state PER METHOD. A method with no arm gets no re-verification at
//     all — the context stays usable for its whole TTL whatever happens to its subject. For post-stay that is
//     the next-occupant leak itself, so TestIntegration_PostStay_ReinstatementBetweenIssueAndConsume is the
//     single most important test in this file: it fails outright if the arm is removed.
//
// These run against the same PHASE3_TEST_DSN database as the rest of the package, with migration 0027 applied.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type psFixture struct {
	fixture
	profile string
}

const psHash = `$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNo`

// seedPostStay takes the ordinary fixture to the state a post-stay guest is actually in: a profile minted for
// the CURRENT episode, and a Stay that has checked out. Writes go through the Phase-5 controlled operation,
// because that is the only way the profile table accepts them.
func seedPostStay(t *testing.T, p *pgxpool.Pool, f fixture) psFixture {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.p5_begin_controlled_operation('post_stay_identity')`); err != nil {
		t.Fatalf("open post_stay_identity: %v", err)
	}
	var prof string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.post_stay_profiles
		(tenant_id, site_id, origin_stay_id, origin_lifecycle_version, pin_hash, valid_until, issued_via)
		SELECT $1,$2,$3, st.lifecycle_version, $4, now()+interval '1 day', 'GUEST_AUTHENTICATED_SESSION'
		  FROM iam_v2.stays st WHERE st.id=$3
		RETURNING id::text`, f.tenant, f.site, f.stay, psHash).Scan(&prof); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open stay: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id=$1`, f.stay); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return psFixture{fixture: f, profile: prof}
}

func psGrant(f psFixture, ttl int) PostStayGrant {
	return PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: f.profile,
		Device: f.device, GuestNetwork: f.network, TTLSeconds: ttl}
}

// reinstate starts a NEW episode on the origin Stay — the room's next occupant, in one statement.
func reinstate(t *testing.T, p *pgxpool.Pool, f psFixture) {
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
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestIntegration_PostStay_IssueAndConsume(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedPostStay(t, p, seed(t, p))
	s := NewStore(p)

	id, err := s.IssuePostStay(context.Background(), psGrant(f, 600))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	c, err := s.Consume(context.Background(), id, pres(f.fixture))
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if c.Method != "POST_STAY_PIN" {
		t.Fatalf("method = %q, want POST_STAY_PIN", c.Method)
	}
	// The SUBJECT is the profile. A caller that read c.Stay and treated it as the subject would be attaching
	// post-stay access to the room again, which is the thing this phase exists to prevent.
	if c.PostStayProfile != f.profile {
		t.Fatalf("PostStayProfile = %q, want %q", c.PostStayProfile, f.profile)
	}
	if _, err := s.Consume(context.Background(), id, pres(f.fixture)); !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("replay: err = %v, want ErrContextInvalid", err)
	}
}

// THE ONE THAT MATTERS. The context is minted while the profile is valid, the room is then re-let, and the
// context must die even though it is inside its TTL, unconsumed, and presented from the same device.
func TestIntegration_PostStay_ReinstatementBetweenIssueAndConsume(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedPostStay(t, p, seed(t, p))
	s := NewStore(p)

	id, err := s.IssuePostStay(context.Background(), psGrant(f, 3600))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	reinstate(t, p, f) // the room's next occupant checks in

	if _, err := s.Consume(context.Background(), id, pres(f.fixture)); !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("a context whose episode ended was accepted: err = %v, want ErrContextInvalid", err)
	}
}

func TestIntegration_PostStay_RevokedProfileCannotBeConsumed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedPostStay(t, p, seed(t, p))
	s := NewStore(p)

	id, err := s.IssuePostStay(context.Background(), psGrant(f, 3600))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.p5_begin_controlled_operation('post_stay_identity')`); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.post_stay_profiles
		SET status='REVOKED', revoked_at=now(), revoke_reason='operator revoked' WHERE id=$1`, f.profile); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := s.Consume(ctx, id, pres(f.fixture)); !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("a revoked profile's context was accepted: err = %v", err)
	}
}

// A context may not be issued at all against a profile that is already dead: minting one and letting it fail
// later would leave a row claiming an authentication that never should have happened.
func TestIntegration_PostStay_IssueRefusesDeadProfile(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedPostStay(t, p, seed(t, p))
	s := NewStore(p)
	reinstate(t, p, f)

	if _, err := s.IssuePostStay(context.Background(), psGrant(f, 600)); !errors.Is(err, ErrPostStayNotAuthenticable) {
		t.Fatalf("issue against a dead episode: err = %v, want ErrPostStayNotAuthenticable", err)
	}
	var n int
	if err := p.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.auth_contexts WHERE post_stay_profile_id=$1`, f.profile).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("a refused issuance persisted %d context row(s)", n)
	}
}

// An IN_HOUSE guest has not checked out, so there is nothing "post" about their stay yet.
func TestIntegration_PostStay_IssueRefusesBeforeCheckout(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p) // still IN_HOUSE
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.p5_begin_controlled_operation('post_stay_identity')`); err != nil {
		t.Fatalf("open: %v", err)
	}
	var prof string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.post_stay_profiles
		(tenant_id, site_id, origin_stay_id, origin_lifecycle_version, pin_hash, valid_until, issued_via)
		VALUES ($1,$2,$3,1,$4, now()+interval '1 day','GUEST_AUTHENTICATED_SESSION') RETURNING id::text`,
		f.tenant, f.site, f.stay, psHash).Scan(&prof); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	s := NewStore(p)
	g := PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: prof, Device: f.device,
		GuestNetwork: f.network, TTLSeconds: 600}
	if _, err := s.IssuePostStay(ctx, g); !errors.Is(err, ErrPostStayNotAuthenticable) {
		t.Fatalf("issue before checkout: err = %v, want ErrPostStayNotAuthenticable", err)
	}
}

// A context pinned to one device is unusable from another, exactly as for every other method.
func TestIntegration_PostStay_PresenterIsPinned(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedPostStay(t, p, seed(t, p))
	s := NewStore(p)
	id, err := s.IssuePostStay(context.Background(), psGrant(f, 600))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	other := pres(f.fixture)
	other.Device = "00000000-0000-0000-0000-0000000000ff"
	if _, err := s.Consume(context.Background(), id, other); !errors.Is(err, ErrContextInvalid) {
		t.Fatalf("another device consumed the context: err = %v", err)
	}
}

// An incomplete grant is refused BEFORE any SQL, with the same sanitized error the PMS path uses.
func TestIntegration_PostStay_IssueRejectsIncomplete(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seedPostStay(t, p, seed(t, p))
	s := NewStore(p)
	for name, mut := range map[string]func(*PostStayGrant){
		"no profile":  func(g *PostStayGrant) { g.Profile = "" },
		"nil profile": func(g *PostStayGrant) { g.Profile = "00000000-0000-0000-0000-000000000000" },
		"no device":   func(g *PostStayGrant) { g.Device = "" },
		"zero ttl":    func(g *PostStayGrant) { g.TTLSeconds = 0 },
		"absurd ttl":  func(g *PostStayGrant) { g.TTLSeconds = 999999 },
	} {
		g := psGrant(f, 600)
		mut(&g)
		if _, err := s.IssuePostStay(context.Background(), g); !errors.Is(err, ErrGrantIncomplete) {
			t.Fatalf("%s: err = %v, want ErrGrantIncomplete", name, err)
		}
	}
}

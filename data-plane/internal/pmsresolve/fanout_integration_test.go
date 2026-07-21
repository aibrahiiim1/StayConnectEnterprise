//go:build integration

package pmsresolve

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping pmsresolve PG16 integration")
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

type fixture struct {
	tenant, site, network string
	ifaces                []string
	stays                 []string
}

// seed builds a guest network mapped to n ACTIVE PMS interfaces, each with one IN_HOUSE Stay.
func seed(t *testing.T, p *pgxpool.Pool, n int) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	if err := p.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  gn AS (INSERT INTO public.guest_networks(id,tenant_id,site_id) SELECT gen_random_uuid(), si.tenant_id, si.id FROM si RETURNING id)
	SELECT (SELECT tenant_id FROM si)::text, (SELECT id FROM si)::text, (SELECT id FROM gn)::text`).
		Scan(&f.tenant, &f.site, &f.network); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 0; i < n; i++ {
		var iface, stay string
		if err := p.QueryRow(ctx, `WITH
		  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
		         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
		  m AS (INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id)
		        SELECT $1,$2,$3,id FROM pi RETURNING pms_interface_id),
		  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status,lifecycle_version,last_applied_event_version)
		         SELECT gen_random_uuid(),$1,$2,pi.id,$4,$5,'IN_HOUSE',1,0 FROM pi RETURNING id)
		SELECT (SELECT id FROM pi)::text, (SELECT id FROM st)::text`,
			f.tenant, f.site, f.network, fmt.Sprintf("R%d", i), fmt.Sprintf("S%d", i)).Scan(&iface, &stay); err != nil {
			t.Fatalf("seed interface %d: %v", i, err)
		}
		f.ifaces = append(f.ifaces, iface)
		f.stays = append(f.stays, stay)
	}
	return f
}

func stayOf(f fixture, iface string) string {
	for i, id := range f.ifaces {
		if id == iface {
			return f.stays[i]
		}
	}
	return ""
}

func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIntegration_FanOutWaitsForTheWholeVector proves the resolver never short-circuits: a SLOW verified
// candidate still wins over fast NO_MATCH answers, every mapped interface is probed exactly once, and the
// outcome is recorded.
func TestIntegration_FanOutWaitsForTheWholeVector(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 5)
	r := NewResolver(p)
	slow := f.ifaces[3]
	var probes int32
	res, err := r.Resolve(ctx, f.tenant, f.site, f.network, uuid(t, p), ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			atomic.AddInt32(&probes, 1)
			if iface == slow {
				time.Sleep(300 * time.Millisecond) // the slow one is the only VERIFIED
				return Verified, stayOf(f, iface), nil
			}
			return NoMatch, "", nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution != ResVerified || res.InterfaceID != slow || res.Stay != stayOf(f, slow) {
		t.Fatalf("outcome=%s iface=%s stay=%s, want VERIFIED on the slow candidate", res.Resolution, res.InterfaceID, res.Stay)
	}
	if got := atomic.LoadInt32(&probes); got != 5 {
		t.Fatalf("probes = %d, want the COMPLETE vector of 5", got)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_resolutions WHERE guest_network_id=$1 AND outcome_code='VERIFIED' AND resolved_stay_id=$2`,
		f.network, stayOf(f, slow)); n != 1 {
		t.Fatal("resolution not recorded")
	}
}

// TestIntegration_IndeterminateCandidateFailsClosed proves a probe that could not answer is UNAVAILABLE and
// makes the whole resolution INDETERMINATE — "we could not ask" is never treated as "it said no".
func TestIntegration_IndeterminateCandidateFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 3)
	r := NewResolver(p)
	r.ProbeTimeout = 100 * time.Millisecond
	hang := f.ifaces[1]
	res, err := r.Resolve(ctx, f.tenant, f.site, f.network, uuid(t, p), ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			if iface == hang {
				<-ctx.Done() // never answers within the probe timeout
				return "", "", ctx.Err()
			}
			if iface == f.ifaces[0] {
				return Verified, stayOf(f, iface), nil
			}
			return NoMatch, "", nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution != ResIndeterminate {
		t.Fatalf("outcome=%s, want INDETERMINATE (a candidate could not be evaluated)", res.Resolution)
	}
	if res.Stay != "" || res.GuestVisibleSuccess() {
		t.Fatal("an indeterminate resolution must expose nothing to the guest")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_resolutions WHERE guest_network_id=$1 AND outcome_code='INDETERMINATE' AND resolved_stay_id IS NULL`, f.network); n != 1 {
		t.Fatal("indeterminate resolution not recorded without a stay")
	}
}

// TestIntegration_AmbiguousNeedsDiscriminator proves two VERIFIED interfaces resolve to AMBIGUOUS with no Stay.
func TestIntegration_AmbiguousNeedsDiscriminator(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 3)
	res, err := NewResolver(p).Resolve(ctx, f.tenant, f.site, f.network, uuid(t, p), ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			if iface == f.ifaces[0] || iface == f.ifaces[2] {
				return Verified, stayOf(f, iface), nil
			}
			return NoMatch, "", nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution != ResAmbiguous || res.Stay != "" {
		t.Fatalf("outcome=%s stay=%s, want AMBIGUOUS with no stay", res.Resolution, res.Stay)
	}
}

// TestIntegration_ResolutionIsIdempotent proves a retry of the same request id replays the STORED outcome
// without re-probing and without writing a second row — even when the probes would now answer differently.
func TestIntegration_ResolutionIsIdempotent(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 3)
	r := NewResolver(p)
	req := uuid(t, p)
	first, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			if iface == f.ifaces[0] {
				return Verified, stayOf(f, iface), nil
			}
			return NoMatch, "", nil
		}))
	if err != nil || first.Resolution != ResVerified {
		t.Fatalf("first resolution: %v %+v", err, first)
	}
	var probed int32
	second, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			atomic.AddInt32(&probed, 1)
			return NoMatch, "", nil // would now be a different answer
		}))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.Resolution != first.Resolution || second.Stay != first.Stay {
		t.Fatalf("retry returned %+v, want the stored %+v replayed", second, first)
	}
	if atomic.LoadInt32(&probed) != 0 {
		t.Fatal("a replayed resolution must not re-probe anything")
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_resolutions WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`,
		f.tenant, f.site, req); n != 1 {
		t.Fatal("a retry wrote a second resolution row")
	}
}

// TestIntegration_ConcurrentResolutions proves >=24 concurrent resolutions of the SAME request id converge on
// exactly one stored resolution and one identical answer for every caller, with no deadlock.
func TestIntegration_ConcurrentResolutions(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p, 4)
	r := NewResolver(p)
	req := uuid(t, p)
	const n = 24
	var wg sync.WaitGroup
	outcomes := make([]Result, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i], errs[i] = r.Resolve(context.Background(), f.tenant, f.site, f.network, req, ProbeFunc(
				func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
					if iface == f.ifaces[1] {
						return Verified, stayOf(f, iface), nil
					}
					return NoMatch, "", nil
				}))
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent resolutions did not settle — possible deadlock")
	}
	want := stayOf(f, f.ifaces[1])
	for i := range outcomes {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if outcomes[i].Resolution != ResVerified || outcomes[i].Stay != want {
			t.Fatalf("racer %d saw %s/%s, want VERIFIED/%s — every caller of one request id must see the same answer",
				i, outcomes[i].Resolution, outcomes[i].Stay, want)
		}
	}
	if got := count(t, p, `SELECT count(*) FROM iam_v2.auth_resolutions WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`,
		f.tenant, f.site, req); got != 1 {
		t.Fatalf("stored resolutions = %d, want exactly 1", got)
	}
}

// TestIntegration_UnmappedNetworkFailsClosed proves a guest network with no mapped interface is INDETERMINATE
// (there is nothing to trust), not NO_MATCH.
func TestIntegration_UnmappedNetworkFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 0)
	res, err := NewResolver(p).Resolve(ctx, f.tenant, f.site, f.network, uuid(t, p), ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) { return NoMatch, "", nil }))
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution != ResIndeterminate || res.Reason != "UNMAPPED" {
		t.Fatalf("outcome=%s reason=%s, want INDETERMINATE/UNMAPPED", res.Resolution, res.Reason)
	}
}

// TestIntegration_VerifiedWithoutStayFailsClosed proves a VERIFIED verdict that cannot name a Stay is refused
// rather than recorded as a success nothing can act on.
func TestIntegration_VerifiedWithoutStayFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 2)
	res, err := NewResolver(p).Resolve(ctx, f.tenant, f.site, f.network, uuid(t, p), ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			if iface == f.ifaces[0] {
				return Verified, "", nil // claims a match but names no Stay
			}
			return NoMatch, "", nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution != ResIndeterminate || res.Reason != "VERIFIED_WITHOUT_STAY" {
		t.Fatalf("outcome=%s reason=%s, want INDETERMINATE/VERIFIED_WITHOUT_STAY", res.Resolution, res.Reason)
	}
}

// TestIntegration_MissingRequestIDFailsClosed proves an idempotent resolution requires a request id.
func TestIntegration_MissingRequestIDFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p, 1)
	if _, err := NewResolver(p).Resolve(context.Background(), f.tenant, f.site, f.network, "", ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) { return NoMatch, "", nil })); err != ErrNoRequestID {
		t.Fatalf("err=%v, want ErrNoRequestID", err)
	}
}

func uuid(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := p.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

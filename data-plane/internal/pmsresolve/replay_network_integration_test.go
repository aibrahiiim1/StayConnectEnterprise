//go:build integration

package pmsresolve

import (
	"context"
	"testing"
)

// P7 L13: a VERIFIED resolution stored for one guest network is never replayed for a request on another network.
func TestIntegration_AResolutionIsNotReplayedOnAnotherNetwork(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1)
	var other string
	if err := p.QueryRow(ctx, `INSERT INTO public.guest_networks(id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr)
		SELECT g, $1, $2, 'gn2', 'p'||substr(md5(g::text),1,12), 'b'||substr(md5(g::text),1,12),
		       '10.9.1.1/24'::inet, '10.9.1.1'::inet, '10.9.1.0/24'::cidr FROM gen_random_uuid() g RETURNING id::text`,
		f.tenant, f.site).Scan(&other); err != nil {
		t.Fatalf("second network: %v", err)
	}
	r := NewResolver(p)
	req := uuid(t, p)
	first, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			return Verified, stayOf(f, iface), nil
		}))
	if err != nil || first.Resolution != ResVerified {
		t.Fatalf("first: %v %+v", err, first)
	}
	// The same request id from another guest network: the stored success is not handed over.
	res, err := r.Resolve(ctx, f.tenant, f.site, other, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			return NoMatch, "", nil
		}))
	if err == nil && (res.Replayed || res.Resolution == ResVerified) {
		t.Fatalf("a success stored for another network was replayed: %+v", res)
	}
}

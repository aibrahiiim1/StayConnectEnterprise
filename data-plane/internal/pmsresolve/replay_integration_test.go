//go:build integration

package pmsresolve

// REPLAY IS A PROPERTY OF SUCCESS.
//
// A resolution request id exists so that ONE identity proof cannot become two: a double tap, or a reply the
// guest's phone never received, must converge on the one recorded resolution and the one Auth Context. That
// is a statement about a proof that SUCCEEDED.
//
// It was also being applied to refusals, and there it was wrong in a way that reached real guests. The portal
// carried one id for a whole page, so a guest who mistyped a surname and corrected it submitted the corrected
// value under the id that already carried their typo. The resolver replayed the stored NO_MATCH without
// probing anything, the portal rendered the single uniform failure sentence, and the correction was never
// compared against the mirror at all. Nothing looked broken from either end.
//
// These assert the corrected rule directly at the resolver, where it is enforced: a stored success is
// replayed and never re-probed; a stored refusal is re-evaluated and never replayed; and a refusal that the
// re-evaluation would turn into a success fails closed, because the record is append-only and access whose
// authority cannot be written down is access this system does not grant.

import (
	"context"
	"sync/atomic"
	"testing"
)

// A REFUSED REQUEST ID IS RE-EVALUATED, NOT REPLAYED.
//
// The counter is the whole assertion. "Refused again" is the same answer either way, so the only way to tell
// a genuine evaluation from a replayed record is whether the probes actually ran.
func TestIntegration_ARefusedResolutionIsReEvaluatedNotReplayed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 3)
	r := NewResolver(p)
	req := uuid(t, p)

	first, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) { return NoMatch, "", nil }))
	if err != nil || first.Resolution != ResNoMatch {
		t.Fatalf("first resolution: %v %+v, want NO_MATCH", err, first)
	}

	var probed int32
	second, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			atomic.AddInt32(&probed, 1)
			return NoMatch, "", nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&probed); got != 3 {
		t.Fatalf("probes on the second submission = %d, want the COMPLETE vector of 3. A stored refusal was "+
			"replayed instead of evaluated, which is what froze a corrected surname behind the guest's own typo", got)
	}
	if second.Replayed {
		t.Fatal("a refusal was marked as replayed; only a stored SUCCESS may be")
	}
	if second.Resolution != ResNoMatch {
		t.Fatalf("second resolution = %s, want the freshly evaluated NO_MATCH", second.Resolution)
	}
	// Re-evaluation does not duplicate the record: the id already has its row, and the append-only table keeps it.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_resolutions
		 WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`, f.tenant, f.site, req); n != 1 {
		t.Fatalf("resolutions recorded for one request id = %d, want 1", n)
	}
}

// A SPENT ID CANNOT BE PROMOTED INTO A SUCCESS.
//
// scd holds INSERT on iam_v2.auth_resolutions and deliberately not UPDATE, so the refusal already recorded
// under this id cannot be corrected. Answering VERIFIED anyway would admit a guest whose authority no row
// records. No conforming caller reaches this — one deliberate submission carries one id — so it is the
// signature of a client re-using a spent id, and it fails closed.
func TestIntegration_ASpentRefusalCannotBecomeASuccess(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 2)
	r := NewResolver(p)
	req := uuid(t, p)

	if first, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			return NoMatch, "", nil
		})); err != nil || first.Resolution != ResNoMatch {
		t.Fatalf("first resolution: %v %+v, want NO_MATCH", err, first)
	}

	second, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			if iface == f.ifaces[0] {
				return Verified, stayOf(f, iface), nil
			}
			return NoMatch, "", nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	if second.GuestVisibleSuccess() || second.Stay != "" {
		t.Fatalf("a request id that already carried a refusal was promoted to %+v; the stored refusal cannot be "+
			"corrected, so the success would be unrecordable", second)
	}
	if second.Resolution != ResIndeterminate || second.Reason != ReasonSpentOnRefusal {
		t.Fatalf("outcome=%s reason=%s, want INDETERMINATE/%s", second.Resolution, second.Reason, ReasonSpentOnRefusal)
	}
	// The original record is untouched, and no second row was written under the same id.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_resolutions
		 WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3
		   AND outcome_code='NO_MATCH' AND resolved_stay_id IS NULL`, f.tenant, f.site, req); n != 1 {
		t.Fatal("the originally recorded refusal was rewritten or duplicated")
	}
}

// AND THE SUCCESS SIDE STILL HOLDS. A stored VERIFIED resolution is replayed verbatim, with no probe re-run
// and no second row — the property that stops a retry becoming a second Auth Context. Asserted here beside
// its opposite so the two rules are read together and neither can be relaxed on its own.
func TestIntegration_ASuccessfulResolutionIsStillReplayed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 3)
	r := NewResolver(p)
	req := uuid(t, p)

	first, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			if iface == f.ifaces[2] {
				return Verified, stayOf(f, iface), nil
			}
			return NoMatch, "", nil
		}))
	if err != nil || first.Resolution != ResVerified {
		t.Fatalf("first resolution: %v %+v, want VERIFIED", err, first)
	}

	var probed int32
	second, err := r.Resolve(ctx, f.tenant, f.site, f.network, req, ProbeFunc(
		func(ctx context.Context, iface string) (CandidateOutcome, string, error) {
			atomic.AddInt32(&probed, 1)
			return NoMatch, "", nil // a different answer, which must not be reached
		}))
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&probed) != 0 {
		t.Fatal("a stored success re-probed; a successful proof must be replayed, not re-run")
	}
	if !second.Replayed || second.Resolution != ResVerified || second.Stay != first.Stay {
		t.Fatalf("retry of a successful resolution returned %+v, want the stored %+v replayed", second, first)
	}
}

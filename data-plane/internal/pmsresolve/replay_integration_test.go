//go:build integration

package pmsresolve

// Replay applies to success only: a stored success is replayed and never re-probed; a stored refusal is
// re-evaluated (a corrected typo shares the page's request id); a refusal re-evaluation would turn into a
// success fails closed, since the record is append-only.

import (
	"context"
	"sync/atomic"
	"testing"
)

// The probe counter is the assertion: "refused again" looks the same replayed or re-evaluated.
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
	// Re-evaluation does not duplicate the record.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_resolutions
		 WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`, f.tenant, f.site, req); n != 1 {
		t.Fatalf("resolutions recorded for one request id = %d, want 1", n)
	}
}

// A spent id cannot become a success: scd has no UPDATE on iam_v2.auth_resolutions, so answering VERIFIED
// would admit a guest whose authority no row records.
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
	// The original record is untouched and no second row was written.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.auth_resolutions
		 WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3
		   AND outcome_code='NO_MATCH' AND resolved_stay_id IS NULL`, f.tenant, f.site, req); n != 1 {
		t.Fatal("the originally recorded refusal was rewritten or duplicated")
	}
}

// A stored VERIFIED resolution is replayed verbatim with no probe and no second row, so a retry can't become a second Auth Context.
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

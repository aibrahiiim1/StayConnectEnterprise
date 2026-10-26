package pmsd

import "testing"

func TestSourceOwnersKeepsOneWorkerPerPhysicalSource(t *testing.T) {
	s := &supervisor{workers: map[string]*worker{}}
	ifs := []Interface{
		{ID: "b", SourceFingerprint: "same"},
		{ID: "a", SourceFingerprint: "same"},
		{ID: "c", SourceFingerprint: "other"},
		{ID: "d"},
		{ID: "e"},
	}
	ids := func(in []Interface) string {
		out := ""
		for _, i := range in {
			out += i.ID
		}
		return out
	}
	if got := ids(s.sourceOwners(ifs)); got != "acde" {
		t.Fatalf("with nothing running the lowest id owns the source: got %q, want acde", got)
	}
	s.workers["b"] = &worker{}
	if got := ids(s.sourceOwners(ifs)); got != "bcde" {
		t.Fatalf("a running worker keeps its source: got %q, want bcde", got)
	}
}

func TestPlanReconcileReleasesASourceBeforeItsNewOwnerStarts(t *testing.T) {
	s := &supervisor{assign: Assignment{TenantID: "t", SiteID: "s"}, workers: map[string]*worker{"a": {}}, desired: map[string]string{"a": "r1"}}
	mk := func(id, rev, fp string) Interface {
		return Interface{TenantID: "t", SiteID: "s", ID: id, LifecycleState: "ACTIVE", CurrentRevisionID: rev, SourceFingerprint: fp}
	}
	// A was republished from X to Y while B took X.
	stops, starts := s.planReconcile([]Interface{mk("b", "r9", "X"), mk("a", "r2", "Y")})
	if len(stops) != 1 || stops[0] != "a" {
		t.Fatalf("the old owner must be stopped first: stops=%v", stops)
	}
	got := map[string]bool{}
	for _, i := range starts {
		got[i.ID] = true
	}
	if !got["a"] || !got["b"] || len(starts) != 2 {
		t.Fatalf("both start once A has released X: %v", starts)
	}

	// An unchanged running owner keeps the source against a newcomer.
	s = &supervisor{assign: Assignment{TenantID: "t", SiteID: "s"}, workers: map[string]*worker{"z": {}}, desired: map[string]string{"z": "r1"}}
	stops, starts = s.planReconcile([]Interface{mk("a", "r1", "X"), mk("z", "r1", "X")})
	if len(stops) != 0 || len(starts) != 0 {
		t.Fatalf("a running owner is not displaced: stops=%v starts=%v", stops, starts)
	}
}

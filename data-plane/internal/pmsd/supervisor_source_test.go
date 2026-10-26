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

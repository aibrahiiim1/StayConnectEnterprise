package iamv2

import "testing"

// ONE RULE, AND EVERY ACQUISITION PATH ASKS IT THE SAME QUESTION.
//
// The rule itself is small enough to state exhaustively, which is the point: the failure it prevents is not
// a subtle one, it is two entry points disagreeing because each carried its own copy.

func TestTimeModeAcquirable(t *testing.T) {
	cases := []struct {
		mode      string
		capable   bool
		wantAllow bool
		note      string
	}{
		{"", false, true, "an omitted mode is VALIDITY_WINDOW, which needs no capability"},
		{"", true, true, "...and is unaffected by the capability being on"},
		{TimeModeValidityWindow, false, true, "the classic mode is always acquirable"},
		{TimeModeValidityWindow, true, true, "...including when aggregate is enabled"},
		{TimeModeAggregateOnlineTime, false, false, "aggregate is refused when nothing would consume the budget"},
		{TimeModeAggregateOnlineTime, true, true, "aggregate is acquirable when the runtime accounts for it"},
		{"validity_window", false, true, "case and padding are normalised, not rejected"},
		{"  AGGREGATE_ONLINE_TIME  ", true, true, "...on both values"},
		{"SOMETHING_ELSE", true, false, "an unknown mode is refused even with the capability on"},
	}
	for _, c := range cases {
		why := TimeModeAcquirable(c.mode, c.capable)
		if (why == "") != c.wantAllow {
			t.Errorf("TimeModeAcquirable(%q, %v) = %q; %s", c.mode, c.capable, why, c.note)
		}
	}
}

// The refusal reason is the SAME string on every path, so a guest cannot tell from the answer which entry
// point they happened to start at -- and an operator reading a log sees one reason, not two spellings.
func TestAggregateRefusalHasOneReason(t *testing.T) {
	if got := TimeModeAcquirable(TimeModeAggregateOnlineTime, false); got != AcquisitionReasonAggregateDisabled {
		t.Fatalf("the aggregate refusal reason is %q, not the shared constant", got)
	}
}

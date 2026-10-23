package iamv2

import "testing"

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

// Every path reports the same refusal reason, so a guest cannot tell which entry point they started at.
func TestAggregateRefusalHasOneReason(t *testing.T) {
	if got := TimeModeAcquirable(TimeModeAggregateOnlineTime, false); got != AcquisitionReasonAggregateDisabled {
		t.Fatalf("the aggregate refusal reason is %q, not the shared constant", got)
	}
}

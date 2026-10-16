package main

import "testing"

// The status answer's time-left view follows iam_v2.p6_counts_online_time (0122): the aggregate mode always, a
// validity window only with a positive allowance -- so the portal shows exactly what the accrual spends.
func TestCountsOnlineTimeMatchesTheDatabaseDefinition(t *testing.T) {
	for _, c := range []struct {
		mode   string
		budget int64
		want   bool
	}{
		{"AGGREGATE_ONLINE_TIME", 0, true},
		{"AGGREGATE_ONLINE_TIME", 3600, true},
		{"VALIDITY_WINDOW", 3600, true},
		{"VALIDITY_WINDOW", 0, false},
		{"VALIDITY_WINDOW", -1, false},
		{"", 3600, false},
	} {
		if got := countsOnlineTime(c.mode, c.budget); got != c.want {
			t.Errorf("countsOnlineTime(%q, %d) = %v, want %v", c.mode, c.budget, got, c.want)
		}
	}
}

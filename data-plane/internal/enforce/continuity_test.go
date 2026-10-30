package enforce

import (
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

func i64(n int64) *int64 { return &n }

func TestBuildContinuity_StrictModeSendsNothing(t *testing.T) {
	if BuildContinuity([]ContinuityRow{{SessionID: "s", EntitlementID: "e"}}, 0, time.Now()) != nil {
		t.Fatal("window 0 must produce no block")
	}
}

func TestBuildContinuity_ClampsAWindowAboveTheCeiling(t *testing.T) {
	c := BuildContinuity([]ContinuityRow{{SessionID: "s", EntitlementID: "e"}}, 10*time.Hour, time.Now())
	if c.WindowSeconds != shapeplan.MaxContinuityWindowSeconds {
		t.Fatalf("window %d", c.WindowSeconds)
	}
}

func TestBuildContinuity_BudgetIsQuotaMinusUsageMinusAMarginAtTheRateCeiling(t *testing.T) {
	asOf := time.Now()
	rows := []ContinuityRow{
		{SessionID: "s1", EntitlementID: "e", DataQuota: i64(1 << 30), DataConsumed: 100 << 20, DownKbps: 8000, UpKbps: 2000},
		{SessionID: "s2", EntitlementID: "e", DataQuota: i64(1 << 30), DataConsumed: 100 << 20, DownKbps: 8000, UpKbps: 2000},
	}
	c := BuildContinuity(rows, time.Hour, asOf)
	if len(c.Budgets) != 1 {
		t.Fatalf("one entitlement, one budget: %+v", c.Budgets)
	}
	margin := int64(2) * int64(8000+2000) * 125 * BudgetMarginSeconds
	want := int64(1<<30) - int64(100<<20) - margin
	if c.Budgets[0].RemainingBytes != want {
		t.Fatalf("budget %d, want %d", c.Budgets[0].RemainingBytes, want)
	}
	if shapeplan.HashContinuity(*c) != c.ContentsHash {
		t.Fatal("hash not set")
	}
}

func TestBuildContinuity_AnUnratedSessionGetsALargeMargin(t *testing.T) {
	c := BuildContinuity([]ContinuityRow{{SessionID: "s", EntitlementID: "e", DataQuota: i64(1 << 30)}}, time.Hour, time.Now())
	if c.Budgets[0].RemainingBytes != 0 {
		t.Fatalf("a 1 GiB quota with no rate ceiling cannot be vouched for 60 s at line rate: budget %d", c.Budgets[0].RemainingBytes)
	}
	if c.Sessions[0].HardEnd == nil {
		t.Fatal("a session with no budget left must be given no continuity (hard end = as-of)")
	}
}

func TestBuildContinuity_QuotaAlreadySpentGivesNoContinuity(t *testing.T) {
	asOf := time.Now()
	c := BuildContinuity([]ContinuityRow{{SessionID: "s", EntitlementID: "e", DataQuota: i64(1 << 20), DataConsumed: 1 << 20, DownKbps: 1000, UpKbps: 1000}}, time.Hour, asOf)
	if c.Sessions[0].HardEnd == nil || c.Sessions[0].HardEnd.After(asOf.UTC().Add(time.Second)) {
		t.Fatalf("hard end %v", c.Sessions[0].HardEnd)
	}
}

func TestBuildContinuity_HardEndsAreTheEarliestOfWindowMaxSessionAndTimeAllowance(t *testing.T) {
	asOf := time.Now().UTC().Truncate(time.Second)
	win := asOf.Add(5 * time.Hour)
	rows := []ContinuityRow{
		{SessionID: "a", EntitlementID: "e1", WindowEndsAt: &win, Started: asOf.Add(-1 * time.Hour), MaxSessionSec: 2 * 3600},
		// 3 sessions share a 1,000 s allowance with 100 s used: (1000-100-120)/3 = 260 s of wall clock.
		{SessionID: "b", EntitlementID: "e2", CountsTime: true, TimeQuota: i64(1000), TimeConsumed: 100, Started: asOf},
		{SessionID: "c", EntitlementID: "e2", CountsTime: true, TimeQuota: i64(1000), TimeConsumed: 100, Started: asOf},
		{SessionID: "d", EntitlementID: "e2", CountsTime: true, TimeQuota: i64(1000), TimeConsumed: 100, Started: asOf},
		{SessionID: "n", EntitlementID: "e3"},
	}
	c := BuildContinuity(rows, time.Hour, asOf)
	end := map[string]*time.Time{}
	for _, s := range c.Sessions {
		end[s.SessionID] = s.HardEnd
	}
	if end["a"] == nil || !end["a"].Equal(asOf.Add(1*time.Hour)) {
		t.Fatalf("a: max session (started -1h + 2h = +1h) must win over the 5h window: %v", end["a"])
	}
	if end["b"] == nil || !end["b"].Equal(asOf.Add(260*time.Second)) {
		t.Fatalf("b: %v", end["b"])
	}
	if end["n"] != nil {
		t.Fatalf("an unlimited session has no hard end of its own: %v", end["n"])
	}
}

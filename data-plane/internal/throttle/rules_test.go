package throttle

import (
	"context"
	"testing"
	"time"
)

// P2 L8: a rule with no positive limit is a configuration error, never a silently dropped rule.
func TestNonPositiveLimitIsRefused(t *testing.T) {
	s := &Store{key: tkey, window: time.Minute, now: time.Now}
	if _, err := s.normalizeRules([]Rule{{Kind: ScopeIP, Value: "10.0.0.1", Method: "account", Limit: 0}}); err == nil {
		t.Fatal("limit 0 must be refused")
	}
	// An absent identity is still skipped.
	got, err := s.normalizeRules([]Rule{{Kind: ScopeDevice, Value: "", Method: "account", Limit: 5}})
	if err != nil || len(got) != 0 {
		t.Fatalf("empty value: %v %d", err, len(got))
	}
}

// P2 L8: the reported reason is the rule with the longest wait. Needs THROTTLE_TEST_DSN.
func TestReasonFollowsLongestRetry(t *testing.T) {
	db := testDB(t)
	s, _ := New(db, tkey, time.Minute)
	ctx := context.Background()
	rules := []Rule{
		{Kind: ScopeDevice, Value: "aa:bb", Method: "account", Limit: 1},                         // window retry only
		{Kind: ScopeIP, Value: "10.0.0.9", Method: "account", Limit: 1, Block: 15 * time.Minute}, // long block
	}
	_, _ = s.Allow(ctx, rules...)
	d, err := s.Allow(ctx, rules...)
	if err != nil || d.Allowed {
		t.Fatalf("second attempt: %+v %v", d, err)
	}
	if d.Reason != ScopeIP || d.RetryAfter < 14*time.Minute {
		t.Fatalf("reason %s retry %v, want the IP block's 15 min", d.Reason, d.RetryAfter)
	}
}

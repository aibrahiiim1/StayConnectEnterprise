package main

import (
	"context"
	"testing"
	"time"
)

func TestTickSecondsIsBounded(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want int
	}{
		{"", 1}, {"1", 1}, {"5", 5}, {"10", 10},
		{"0", 1}, {"-3", 1}, {"11", 10}, {"120", 10}, {"abc", 1}, {"2.5", 1},
	} {
		got := tickSeconds(func(k string) string {
			if k == "ACCTD_TICK_SECONDS" {
				return c.raw
			}
			return ""
		})
		if got != c.want {
			t.Errorf("ACCTD_TICK_SECONDS=%q -> %d, want %d", c.raw, got, c.want)
		}
	}
	if time.Duration(maxTickSeconds)*time.Second*3 > planValidity {
		t.Fatalf("maxTickSeconds %d leaves fewer than three ticks inside planValidity %s", maxTickSeconds, planValidity)
	}
}

func TestPhase3NilArmIsInertForPlanLimitsAndPurge(t *testing.T) {
	var p *phase3
	p.enforceSessionLimits(context.Background(), time.Now())
	p.purgeAccountingDetail(context.Background(), time.Now())
}

func TestPurgeIsRateLimited(t *testing.T) {
	now := time.Now()
	p := &phase3{lastPurge: now.Add(-time.Minute)}
	// enf is nil: if the rate limit failed, the call would panic.
	p.purgeAccountingDetail(context.Background(), now)
	if !p.lastPurge.Equal(now.Add(-time.Minute)) {
		t.Fatal("a purge ran a minute after the last one; it must wait purgeInterval")
	}
}

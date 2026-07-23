package main

// ACCOUNTING HEALTH. The failure this guards against is not an error going unlogged — it is an accounting
// loop that keeps ticking while every observation it makes is refused, or one that quietly stops producing.
// Both look identical from outside: the process is up, the heartbeat is fresh, and nothing says a thing until
// a Folio comes up short.

import (
	"strings"
	"testing"
	"time"
)

// Every degraded condition must surface, and the reason must be a bounded phrase — not raw SQL, not a guest
// or session identifier. Health output is read by people and by monitoring systems that were never
// authorised to learn who is staying at the property.
func TestAccountingHealthReportsEveryDegradedCondition(t *testing.T) {
	now := time.Now()

	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"session inventory unreadable", reasonSessionsUnreadable},
		{"class generations unavailable", reasonNoClassGenerations},
		{"tc counters unreadable", reasonCountersUnreadable},
		{"incoherent counter source", reasonSourceIncoherent},
		{"an observation refused by the boundary", reasonObservationRefused},
	} {
		p := &phase3{tenant: "t", site: "s", lastPassOK: now, acctDegraded: tc.reason}
		h := p.accountingHealth(now)
		if h["degraded"] != true {
			t.Fatalf("%s did not surface as degraded: %v", tc.name, h)
		}
		if h["reason"] != tc.reason {
			t.Fatalf("%s reported reason %v", tc.name, h["reason"])
		}
	}
}

// SILENCE IS NOT SUCCESS. A loop that has not completed a clean pass within the freshness window is degraded
// even though nothing errored — that is precisely the state nobody notices.
func TestAccountingHealthTreatsSilenceAsDegraded(t *testing.T) {
	now := time.Now()

	fresh := &phase3{tenant: "t", site: "s", lastPassOK: now.Add(-time.Minute)}
	if h := fresh.accountingHealth(now); h["degraded"] != false || h["stale"] != false {
		t.Fatalf("a recent clean pass was reported degraded: %v", h)
	}

	quiet := &phase3{tenant: "t", site: "s", lastPassOK: now.Add(-accountingFreshness - time.Minute)}
	h := quiet.accountingHealth(now)
	if h["degraded"] != true || h["stale"] != true {
		t.Fatalf("a silent loop was reported healthy: %v", h)
	}
	if h["reason"] != reasonNoRecentPass {
		t.Fatalf("a silent loop reported reason %v", h["reason"])
	}

	// a loop that has NEVER completed a pass is also stale — "no evidence yet" is not evidence of health
	never := &phase3{tenant: "t", site: "s"}
	if h := never.accountingHealth(now); h["degraded"] != true || h["stale"] != true {
		t.Fatalf("a loop that never ran was reported healthy: %v", h)
	}
}

// The reason must never carry SQL, a session id, a guest identifier or an address. An error string pasted
// straight through is the easiest way for those to reach a monitoring system or a support ticket.
func TestAccountingHealthReasonsAreBounded(t *testing.T) {
	reasons := []string{
		reasonSessionsUnreadable, reasonNoClassGenerations, reasonCountersUnreadable,
		reasonSourceIncoherent, reasonObservationRefused, reasonNoRecentPass,
	}
	for _, r := range reasons {
		low := strings.ToLower(r)
		for _, leak := range []string{"select ", "insert ", "update ", "sqlstate", "iam_v2.", "10.", "uuid", "err="} {
			if strings.Contains(low, leak) {
				t.Fatalf("bounded reason %q contains %q", r, leak)
			}
		}
		if len(r) > 120 {
			t.Fatalf("reason is not a bounded phrase: %q", r)
		}
	}
}

// A dark arm is never degraded: it is doing exactly what it should, which is nothing.
func TestDarkAccountingIsNotDegraded(t *testing.T) {
	var dark *phase3
	h := dark.accountingHealth(time.Now())
	if h["active"] != false {
		t.Fatalf("a dark arm claimed to be active: %v", h)
	}
	if dark.degradedSummary() != "" {
		t.Fatal("a dark arm reported a degraded summary")
	}
}

// The summary the health supervisor reads must carry BOTH problems when both exist. An operator told only
// about the shaping refusal would declare the incident over once shaping recovered, while usage was still
// being lost.
func TestDegradedSummaryCarriesBothProblems(t *testing.T) {
	p := &phase3{tenant: "t", site: "s", lastPassOK: time.Now()}
	p.acctDegraded = reasonCountersUnreadable
	p.degraded = "netd refused the shaping plan: stale_generation"
	got := p.degradedSummary()
	if !strings.Contains(got, reasonCountersUnreadable) || !strings.Contains(got, "stale_generation") {
		t.Fatalf("a summary dropped one of two concurrent problems: %q", got)
	}
}

package main

// THE FAILURE POSTURE IS THE PART WORTH PINNING.
//
// Whether the detector runs is easy to get right once it has a caller at all. What is easy to get WRONG is
// what happens when it cannot run, and there are two opposite wrong answers:
//
//	exit while Phase 4 is DARK   an outage of the whole operator API because a detector for money that
//	                             cannot move could not run. Nothing is protected by that.
//	continue while transmitting  a financial worker draining an outbox without knowing whether this
//	                             database was restored, which is the exact condition migration 0023 exists
//	                             to stop.
//
// These tests pin both directions, and they pin the skip that is NOT a fault.

import (
	"context"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/posting"
)

// An appliance with no signed assignment has no site whose financial history could have been restored.
// That is the ordinary pre-assignment state, and it must not be reported as a failure.
func TestNoAssignmentSkipsTheReconcileWithoutClaimingAnything(t *testing.T) {
	for _, tc := range []struct{ tenant, site string }{
		{"", ""},
		{"11111111-1111-4111-8111-111111111111", ""},
		{"", "22222222-2222-4222-8222-222222222222"},
	} {
		s := &server{tenantID: tc.tenant, siteID: tc.site}
		// No db and no engine are needed: the guard returns before either is touched. If this ever panics,
		// the skip has stopped being the first thing the function does.
		if got := s.reconcileFinancialEpochAtStartup(context.Background()); got != "" {
			t.Errorf("tenant=%q site=%q: expected an empty outcome, got %q", tc.tenant, tc.site, got)
		}
	}
}

// DARK: a reconcile that cannot run is loud and startup continues. Money cannot move whatever it returned.
func TestWhileDarkAnUnavailableDetectorDoesNotStopStartup(t *testing.T) {
	s := &server{financialCfg: posting.Config{}} // zero value: MasterEnabled false, so Dark() is true
	if !s.financialCfg.Dark() {
		t.Fatal("the zero Config must be DARK; this test depends on it")
	}
	// Must return rather than calling os.Exit. A test process that exits here fails the whole package, so
	// reaching the assertion at all is the assertion.
	if got := s.financialEpochUnavailable("test", context.DeadlineExceeded); got != "" {
		t.Errorf("expected an empty outcome while dark, got %q", got)
	}
}

// The posture depends on Dark(), so pin what Dark() means for the flag combinations that matter. If
// TransmitOn ever stops implying money movement, this test is where that shows up.
func TestOnlyTransmissionMakesThePostureFatal(t *testing.T) {
	cases := []struct {
		name string
		cfg  posting.Config
		dark bool
	}{
		{"all off", posting.Config{}, true},
		{"master only", posting.Config{MasterEnabled: true}, true},
		{"posting without transmit", posting.Config{MasterEnabled: true, PostingEnabled: true}, true},
		{"outbox without transmit", posting.Config{MasterEnabled: true, OutboxEnabled: true}, true},
		{"transmit without master", posting.Config{TransmitEnabled: true}, true},
		// TransmitOn requires THREE flags -- master AND outbox AND transmit -- and its comment says why:
		// "so no single mis-set variable can put financial bytes on a wire". So master+transmit is STILL
		// dark, which this case had wrong until the test failed and the code was right.
		{"master and transmit, no outbox lane", posting.Config{MasterEnabled: true, TransmitEnabled: true}, true},
		{"master and outbox, no transmit", posting.Config{MasterEnabled: true, OutboxEnabled: true}, true},
		{"all three: the only non-dark combination",
			posting.Config{MasterEnabled: true, OutboxEnabled: true, TransmitEnabled: true}, false},
	}
	for _, c := range cases {
		if got := c.cfg.Dark(); got != c.dark {
			t.Errorf("%s: Dark()=%v, want %v -- the startup failure posture keys on this", c.name, got, c.dark)
		}
	}
}

// The reason a reader needs from a dark failure is that the detector did not run, not that it found nothing.
// Those are different claims and the log line has to make the difference.
func TestTheDarkFailureSaysTheDetectorDidNotRunRatherThanThatItFoundNothing(t *testing.T) {
	// Read the source of the message rather than capturing the logger: what matters is that the wording
	// exists and says the right thing, and asserting on it here keeps it from being softened later.
	src := readSourceFile(t, "financial_epoch_reconcile.go")
	for _, want := range []string{
		"did NOT run",
		"proves nothing about whether this database",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the dark-failure message no longer contains %q, so it can be read as 'nothing was "+
				"found' rather than 'nothing was checked'", want)
		}
	}
	// ...and the fatal branch must say why it refuses, not merely that it does.
	for _, want := range []string{"REFUSING TO SERVE", "without knowing whether this database was restored"} {
		if !strings.Contains(src, want) {
			t.Errorf("the fatal-branch message no longer contains %q", want)
		}
	}
}

// The ordering rule from the detector's own contract: it must run before any route is mounted. Asserted
// against main.go's source, because the alternative is starting a server in a unit test.
func TestTheReconcileIsCalledBeforeAnyRouteIsMounted(t *testing.T) {
	src := readSourceFile(t, "main.go")
	call := strings.Index(src, "s.reconcileFinancialEpochAtStartup(")
	if call < 0 {
		t.Fatal("main.go no longer calls reconcileFinancialEpochAtStartup -- migration 0023's detector has " +
			"lost its only caller again, which is the defect this delivery fixed")
	}
	mount := strings.Index(src, "mountResource(")
	if mount < 0 {
		t.Fatal("main.go no longer calls mountResource; this test needs updating")
	}
	if call > mount {
		t.Error("reconcileFinancialEpochAtStartup is called AFTER the first mountResource. Its contract is " +
			"that it runs before any financial worker starts, and a mounted route is one.")
	}
}

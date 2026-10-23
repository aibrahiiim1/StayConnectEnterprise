package main

// These tests pin both wrong answers to a detector that cannot run: exiting while Phase 4 is dark (an outage
// protecting nothing) and continuing while transmitting (a worker draining an outbox on possibly restored data).

import (
	"context"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/posting"
)

func TestNoAssignmentSkipsTheReconcileWithoutClaimingAnything(t *testing.T) {
	for _, tc := range []struct{ tenant, site string }{
		{"", ""},
		{"11111111-1111-4111-8111-111111111111", ""},
		{"", "22222222-2222-4222-8222-222222222222"},
	} {
		s := &server{tenantID: tc.tenant, siteID: tc.site}
		// No db or engine: a panic means the skip is no longer the first thing the function does.
		if got := s.reconcileFinancialEpochAtStartup(context.Background()); got != "" {
			t.Errorf("tenant=%q site=%q: expected an empty outcome, got %q", tc.tenant, tc.site, got)
		}
	}
}

func TestWhileDarkAnUnavailableDetectorDoesNotStopStartup(t *testing.T) {
	s := &server{financialCfg: posting.Config{}}
	if !s.financialCfg.Dark() {
		t.Fatal("the zero Config must be DARK; this test depends on it")
	}
	// Must return rather than os.Exit, which would fail the whole package.
	if got := s.financialEpochUnavailable("test", context.DeadlineExceeded); got != "" {
		t.Errorf("expected an empty outcome while dark, got %q", got)
	}
}

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
		// TransmitOn needs master, outbox and transmit so no single mis-set variable can put bytes on a wire.
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

func TestTheDarkFailureSaysTheDetectorDidNotRunRatherThanThatItFoundNothing(t *testing.T) {
	// Assert on the source wording so it cannot be softened to read as "nothing found".
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
	for _, want := range []string{"REFUSING TO SERVE", "without knowing whether this database was restored"} {
		if !strings.Contains(src, want) {
			t.Errorf("the fatal-branch message no longer contains %q", want)
		}
	}
}

// The reconcile must run before any route is mounted; asserted on main.go's source rather than starting a server.
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

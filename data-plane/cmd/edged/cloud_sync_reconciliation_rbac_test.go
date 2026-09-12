package main

// AUTHORIZATION for the three keys this delivery adds, through the ACTUAL router and session middleware.
//
// The tempting shape is one "cloud sync" key and one "PMS" key. Both would be wrong for the same reason the
// guest sign-in keys were split: the powers behind them differ by orders of magnitude.
//
//   * cloud-sync-settings   changes how long DELIVERED records are kept. A policy.
//   * cloud-sync-recovery   releases thousands of abandoned records back onto the wire, at a far end that
//                           serves the whole fleet. An action with consequences somewhere else.
//   * pms-reconciliation    WRITE hands a recorded departure back to the ingestion engine, which may then
//                           check a stay out through the ordinary checkout policy and revoke a guest's
//                           access. READ is just a list, and the desk needs the list.
//
// The reception desk therefore reads reconciliation and can act on none of it, and touches the cloud queue
// not at all.

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func cloudSyncRBACRouter(s *server) http.Handler {
	reached := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		mountResource(r, s, "cloud-sync-settings", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Put("/", reached)
			return rr
		})
		mountResource(r, s, "cloud-sync-recovery", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Post("/", reached)
			return rr
		})
		mountResource(r, s, "pms-reconciliation", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Post("/{eventID}/re-evaluate", reached)
			return rr
		})
	})
	return r
}

func TestCloudSyncAndReconciliation_PermissionModel(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := cloudSyncRBACRouter(s)

	const reach = http.StatusTeapot
	const deny = http.StatusForbidden

	cases := []struct {
		role                                 string
		readSettings, writeSettings          int
		readRecovery, doRecovery             int
		readReconciliation, doReconciliation int
	}{
		{"site_admin", reach, reach, reach, reach, reach, reach},
		// The IT manager owns the appliance's infrastructure and the PMS integration.
		{"hotel_it_manager", reach, reach, reach, reach, reach, reach},
		// THE DESK READS THE CASES AND ACTS ON NONE OF THEM. Re-evaluating can end a stay and revoke a
		// guest's access; that is not a reception decision. The cloud queue is not a desk concern at all.
		{"front_office_operator", deny, deny, deny, deny, reach, deny},
		{"guest_relations_operator", deny, deny, deny, deny, reach, deny},
		// A viewer sees all three as evidence — including the recovery log, which records what somebody did
		// about the queue — and acts on none of them.
		{"site_viewer", reach, deny, reach, deny, reach, deny},
		// Roles with no relationship to either subject reach nothing.
		{"voucher_operator", deny, deny, deny, deny, deny, deny},
		{"payments_operator", deny, deny, deny, deny, deny, deny},
	}

	for _, c := range cases {
		cookie := loginAs(t, s, []string{c.role})
		checks := []struct {
			label, method, path string
			want                int
		}{
			{"read retention setting", http.MethodGet, "/cloud-sync-settings/", c.readSettings},
			{"change retention setting", http.MethodPut, "/cloud-sync-settings/", c.writeSettings},
			{"read recovery history", http.MethodGet, "/cloud-sync-recovery/", c.readRecovery},
			{"run a recovery", http.MethodPost, "/cloud-sync-recovery/", c.doRecovery},
			{"read reconciliation cases", http.MethodGet, "/pms-reconciliation/", c.readReconciliation},
			{"re-evaluate a departure", http.MethodPost, "/pms-reconciliation/abc/re-evaluate", c.doReconciliation},
		}
		for _, k := range checks {
			if got := protectionDo(t, h, k.method, k.path, cookie); got != k.want {
				t.Errorf("%s: %s = %d, want %d", c.role, k.label, got, k.want)
			}
		}
	}
}

// READING THE QUEUE DOES NOT CARRY PERMISSION TO EMPTY IT BACK ONTO THE WIRE.
//
// Stated separately because the two keys are held by an overlapping set of roles, which is exactly the
// situation in which somebody later decides one implies the other.
func TestCloudSyncRecovery_ReadingDoesNotCarryRunning(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := cloudSyncRBACRouter(s)

	cookie := loginAs(t, s, []string{"site_viewer"})
	if got := protectionDo(t, h, http.MethodGet, "/cloud-sync-recovery/", cookie); got != http.StatusTeapot {
		t.Fatalf("setup: a viewer should read the recovery history, got %d", got)
	}
	if got := protectionDo(t, h, http.MethodPost, "/cloud-sync-recovery/", cookie); got != http.StatusForbidden {
		t.Fatalf("reading the recovery history carried permission to run one: %d", got)
	}
}

// The one that would disconnect a guest: a desk role must never reach the re-evaluate route, whose outcome
// runs the checkout policy.
func TestReconciliation_TheDeskCannotReEvaluate(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := cloudSyncRBACRouter(s)

	for _, role := range []string{"front_office_operator", "guest_relations_operator", "site_viewer"} {
		cookie := loginAs(t, s, []string{role})
		if got := protectionDo(t, h, http.MethodGet, "/pms-reconciliation/", cookie); got != http.StatusTeapot {
			t.Errorf("%s: should read the cases, got %d", role, got)
		}
		if got := protectionDo(t, h, http.MethodPost, "/pms-reconciliation/abc/re-evaluate", cookie); got != http.StatusForbidden {
			t.Errorf("%s: reached the re-evaluate route (%d); its outcome can revoke a guest's access", role, got)
		}
	}
}

// actionableState is the single gate between a case and the button. It is asserted here rather than only in
// the browser because the server refuses the route on the same predicate, and a second copy of "which states
// are safe" is how the screen and the server come to disagree.
func TestActionableState_OnlyResolvable(t *testing.T) {
	safe := []string{"RESOLVABLE"}
	refused := []string{
		"SUPERSEDED_ROOM_EMPTY", // nobody is in the room; nothing to close
		"ROOM_SHARED",           // sharing is ordinary and makes a room-keyed departure undecidable
		"LATER_OCCUPANT",        // applying it would check out a resident guest
		"ROSTER_CONTRADICTS",    // the PMS's own fresh roster says they are still in house
		"NEEDS_PMS_EVIDENCE",
		"", // an unknown state must never be actionable
	}
	for _, s := range safe {
		if !actionableState(s) {
			t.Errorf("%s should be actionable", s)
		}
	}
	for _, s := range refused {
		if actionableState(s) {
			t.Errorf("%s must not be actionable", s)
		}
	}
}

// The screen's stated standard and the database's default must be the same number. They are written in two
// places by necessity — a CHECK constraint cannot import a Go constant — so this is the thing that keeps
// them equal.
func TestRetentionDefault_MatchesWhatTheScreenClaims(t *testing.T) {
	if defaultRetentionDays != 30 {
		t.Errorf("the approved default is 30 days; this says %d", defaultRetentionDays)
	}
	if minRetentionDays != 1 || maxRetentionDays != 365 {
		t.Errorf("bounds drifted from the migration's CHECK: %d..%d", minRetentionDays, maxRetentionDays)
	}
}

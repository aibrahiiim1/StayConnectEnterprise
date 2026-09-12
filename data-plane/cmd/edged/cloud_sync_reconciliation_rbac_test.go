package main

// AUTHORIZATION for the three keys this delivery adds, through the ACTUAL router and session middleware.
//
// The tempting shape is one "cloud sync" key and one "PMS" key. Both would be wrong for the same reason the
// guest sign-in keys were split: the powers behind them differ by orders of magnitude.
//
//   * cloud-sync-settings   changes how long DELIVERED records are kept. A policy.
//   * cloud-sync-recovery   releases thousands of abandoned records back onto the wire, at a far end that
//                           serves the whole fleet. An action with consequences somewhere else.
//   * pms-reconciliation    READ ONLY, for every role. There is no local action: a departure that went to
//                           review is resolved by the PMS sending one that can be applied, because
//                           stay_events is one-way and a checkout boundary must be an APPLIED GO event.
//                           A write permission would promise a power the product does not have.
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
		role                        string
		readSettings, writeSettings int
		readRecovery, doRecovery    int
		readReconciliation          int
	}{
		{"site_admin", reach, reach, reach, reach, reach},
		// The IT manager owns the appliance's infrastructure and the PMS integration.
		{"hotel_it_manager", reach, reach, reach, reach, reach},
		// The desk reads the cases. The cloud queue is not a desk concern at all.
		{"front_office_operator", deny, deny, deny, deny, reach},
		{"guest_relations_operator", deny, deny, deny, deny, reach},
		// A viewer sees all three as evidence — including the recovery log, which records what somebody did
		// about the queue — and acts on the one action it is offered, never.
		{"site_viewer", reach, deny, reach, deny, reach},
		// Roles with no relationship to either subject reach nothing.
		{"voucher_operator", deny, deny, deny, deny, deny},
		{"payments_operator", deny, deny, deny, deny, deny},
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

// THE ACTION DOES NOT EXIST, FOR ANYBODY — including site_admin.
//
// This is the assertion that keeps the product honest about what it can do. A departure that went to review
// cannot be replayed: iam_v2.stay_events is strictly one-way (the p3_stay_event_appendonly trigger refuses
// any change to a terminal row), and separately a checkout boundary must be an APPLIED GO event. A route
// offering to try would fail at the database every time, and an operator would reasonably read that failure
// as the appliance being broken rather than as the design saying no.
//
// It walks the REAL route tree rather than a fixture, so re-adding a write route fails here.
func TestReconciliation_ThereIsNoLocalAction(t *testing.T) {
	s := &server{}
	var mutating []string
	err := chi.Walk(s.pmsReconciliationRoutes().(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
				mutating = append(mutating, method+" "+route)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("walking the reconciliation routes: %v", err)
	}
	if len(mutating) != 0 {
		t.Errorf("pms-reconciliation exposes %v; the PMS resolves these cases, not this screen", mutating)
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

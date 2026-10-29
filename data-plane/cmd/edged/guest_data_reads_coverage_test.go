package main

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Audit P9 M4: the guest-data read audit records a route only when its pattern is in guestDataReads. This walks
// every GET route of the resources that hold guest data and fails for one that is neither recorded nor
// deliberately classified below, so a new guest-data route cannot be added without a decision.

// guestListsBrowsedUnrecorded is EMPTY since audit decision AD-7(a): every list page view is recorded as guest_data.listed (in
// guestDataReads). The map stays so a list that is deliberately NOT recorded can be classified here, with a reason, rather than
// slipping through.
var guestListsBrowsedUnrecorded = map[string]bool{}

// notGuestDataRoutes are GET routes under these resources that show no guest's personal data.
var notGuestDataRoutes = map[string]bool{
	"/edge/v1/guest-accounts/portal":          true, // whether the portal offers client accounts
	"/edge/v1/pms-stays/travel-agents":        true, // agent names, not guests
	"/edge/v1/pms-reconciliation/summary":     true, // counts
	"/edge/v1/pms-roster-reconciliation/runs": true, // run bookkeeping
	"/edge/v1/pms-event-decisions/settings":   true,
	"/edge/v1/vouchers/batches":               true,
	"/edge/v1/vouchers/grantable":             true,
	"/edge/v1/vouchers/summary":               true,
}

func TestEveryGuestDataRouteIsRecordedOrClassified(t *testing.T) {
	s := &server{}
	resources := map[string]func() http.Handler{
		"/edge/v1/guest-accounts":            s.guestAccountsRoutes,
		"/edge/v1/sessions":                  s.sessionsRoutes,
		"/edge/v1/client-devices":            s.clientDevicesAdminRoutes,
		"/edge/v1/pms-stays":                 s.pmsStaysRoutes,
		"/edge/v1/pms-events":                s.pmsEventsRoutes,
		"/edge/v1/pms-resolutions":           s.pmsResolutionsRoutes,
		"/edge/v1/pms-event-decisions":       s.pmsEventDecisionsRoutes,
		"/edge/v1/pms-reconciliation":        s.pmsReconciliationRoutes,
		"/edge/v1/pms-roster-reconciliation": s.rosterReconciliationRoutes,
		"/edge/v1/guest-signin-attempts":     s.signInAttemptsRoutes,
		"/edge/v1/guest-signin-restrictions": s.signInRestrictionsRoutes,
		"/edge/v1/usage":                     s.usageRoutes,
		"/edge/v1/guest-data-rights":         s.guestDataRightsRoutes,
		"/edge/v1/vouchers":                  s.vouchersRoutes,
		"/edge/v1/post-stay-profiles":        s.postStayProfilesRoutes,
		"/edge/v1/stay-transfers":            s.stayTransfersRoutes,
		"/edge/v1/code-requests":             s.codeRequestsRoutes,
	}
	var unclassified []string
	seen := map[string]bool{}
	for prefix, build := range resources {
		rt, ok := build().(chi.Routes)
		if !ok {
			t.Fatalf("%s: routes are not walkable", prefix)
		}
		_ = chi.Walk(rt, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			if method != http.MethodGet {
				return nil
			}
			// RoutePattern reports a list mounted at "/" without the trailing slash.
			p := strings.TrimSuffix(prefix+route, "/")
			seen[p] = true
			if _, recorded := guestDataReads[p]; !recorded && !guestListsBrowsedUnrecorded[p] && !notGuestDataRoutes[p] {
				unclassified = append(unclassified, p)
			}
			return nil
		})
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("GET routes under guest-data resources that are neither recorded in guestDataReads nor classified:\n  %s",
			strings.Join(unclassified, "\n  "))
	}
	// A classification for a route that no longer exists would hide a renamed one.
	for _, m := range []map[string]bool{guestListsBrowsedUnrecorded, notGuestDataRoutes} {
		for p := range m {
			if !seen[p] {
				t.Errorf("%s is classified but no longer exists", p)
			}
		}
	}
}

package main

// PER-ROUTE REQUEST TIMEOUTS (audit N-3).
//
// Every request was bounded at 15 s by one middleware. A client-network apply creates bridges, waits for Kea
// (up to 20 s on its own), restarts services and runs health checks; a WAN/LAN apply runs netplan, waits for
// links and probes reachability. Both can take longer than 15 s on a real property, and the cancelled request
// context reached netd -- so the operator saw an error while netd went on with (or was cut off in the middle
// of) the change.
//
// The network applies, confirms and rollbacks now get a long bound of their own; everything else keeps 15 s.
// netd also detaches those operations from the request (cmd/netd/main.go), so a closed browser tab can never
// interrupt a change half-way.

import (
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

const (
	defaultRequestTimeout = 15 * time.Second
	networkApplyTimeout   = 180 * time.Second
)

// longNetworkRoutes are the requests that wait for netd to change the appliance's networking.
var longNetworkRoutes = regexp.MustCompile(`^/edge/v1/network/(apply|validate|system/(apply|confirm|rollback|validate)|revisions/[^/]+/(confirm|rollback))$`)

// routeTimeout bounds each request: the network applies at long, everything else at short.
func routeTimeout(short, long time.Duration) func(http.Handler) http.Handler {
	shortMW, longMW := middleware.Timeout(short), middleware.Timeout(long)
	return func(next http.Handler) http.Handler {
		s, l := shortMW(next), longMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && longNetworkRoutes.MatchString(r.URL.Path) {
				l.ServeHTTP(w, r)
				return
			}
			s.ServeHTTP(w, r)
		})
	}
}

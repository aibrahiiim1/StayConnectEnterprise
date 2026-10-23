package main

// Per-route request timeouts: network applies, confirms and rollbacks wait for netd (Kea alone can take 20 s) and get a long bound; everything else keeps 15 s.
// netd detaches those operations from the request (cmd/netd/main.go), so a closed tab cannot interrupt a change half-way.

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

// ledgerRouteTimeout bounds requests that commit to the erasure ledger: each write goes to the appliance AND secondary storage (disk, SFTP, bucket),
// which over a slow link exceeds the 15 s default.
const ledgerRouteTimeout = 4 * time.Minute

// ledgerRoutes are the requests that commit to the erasure ledger (they all go through ledgerQuery).
var ledgerRoutes = regexp.MustCompile(`^/edge/v1/guest-data-rights/(erase|assisted/[^/]+/(erase|close)|register/[^/]+/answer)$`)

// routeTimeout bounds each request: the network applies at long, the ledger commits at ledgerRouteTimeout, everything else at short.
func routeTimeout(short, long time.Duration) func(http.Handler) http.Handler {
	shortMW, longMW, ledgerMW := middleware.Timeout(short), middleware.Timeout(long), middleware.Timeout(ledgerRouteTimeout)
	return func(next http.Handler) http.Handler {
		s, l, g := shortMW(next), longMW(next), ledgerMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && longNetworkRoutes.MatchString(r.URL.Path) {
				l.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodPost && ledgerRoutes.MatchString(r.URL.Path) {
				g.ServeHTTP(w, r)
				return
			}
			s.ServeHTTP(w, r)
		})
	}
}

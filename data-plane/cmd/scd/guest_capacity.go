package main

import (
	"net/http"
	"strconv"
)

// maxGuestInFlight bounds guest requests scd works on at once. scd has a small database pool and two CPUs; beyond this,
// requests would only queue inside scd until the 10 s handler timeout. A protective internal limit, not a hotel setting.
const maxGuestInFlight = 64

// guestBusyRetryAfter is the Retry-After (seconds) sent with a capacity refusal, so portald can say "busy, try again".
const guestBusyRetryAfter = 2

// guestCapacity refuses guest requests beyond maxGuestInFlight at once with 503 and Retry-After, early, instead of
// letting them time out. Admin routes, the health check and metrics are never refused here.
func guestCapacity(limit int) func(http.Handler) http.Handler {
	slots := make(chan struct{}, limit)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/health" || r.URL.Path == "/metrics" || classifyRoute(r.URL.Path) != guestRoute {
				next.ServeHTTP(w, r)
				return
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				next.ServeHTTP(w, r)
			default:
				w.Header().Set("Retry-After", strconv.Itoa(guestBusyRetryAfter))
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "BUSY", "retry_after": guestBusyRetryAfter})
			}
		})
	}
}

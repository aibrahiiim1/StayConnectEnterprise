package main

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// slotKey carries the request's capacity slot, so a handler that is only WAITING (for enforcement to be confirmed) can hand it back.
type slotKey struct{}

type guestSlot struct {
	once    sync.Once
	release func()
}

func (g *guestSlot) free() { g.once.Do(g.release) }

// releaseGuestSlot gives the in-flight slot of the request in ctx back before a long wait that holds no database connection and does no
// work. The sign-in waits up to eight seconds for the kernel to confirm the guest; counted against the 64 slots that wait starved every other
// guest request at 5 sign-ins a second (capacity acceptance, 2026-10-30). A no-op outside a guest request or when already released.
func releaseGuestSlot(ctx context.Context) {
	if g, ok := ctx.Value(slotKey{}).(*guestSlot); ok && g != nil {
		g.free()
	}
}

// maxGuestInFlight bounds guest requests scd works on at once. scd has a small database pool and two CPUs; beyond this,
// requests would only queue inside scd until the 10 s handler timeout. A protective internal limit, not a hotel setting.
const maxGuestInFlight = 64

// guestBusyRetryAfter is the Retry-After (seconds) sent with a capacity refusal, so portald can say "busy, try again".
const guestBusyRetryAfter = 2

// guestQueueWait is how long a guest request beyond the in-flight cap waits for a slot before it is refused with "busy"
// (AD-26, AD-35). A burst of 300-500 clients connecting at once is hundreds of short requests arriving together: refusing
// the ones beyond 64 at the instant they arrive turned a 150 ms backlog into hundreds of "busy" answers and retries.
// Queueing for up to two seconds drains such a burst; only a backlog that long is told to come back. A variable so tests
// can shorten it.
var guestQueueWait = 2 * time.Second

// guestCapacity serves at most limit guest requests at once. A request beyond that waits up to guestQueueWait for a slot,
// then is refused with 503 and Retry-After, early, instead of timing out. Admin routes, the health check and metrics are
// never refused or queued here.
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
			default:
				timer := time.NewTimer(guestQueueWait)
				defer timer.Stop()
				select {
				case slots <- struct{}{}:
				case <-timer.C:
					w.Header().Set("Retry-After", strconv.Itoa(guestBusyRetryAfter))
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "BUSY", "retry_after": guestBusyRetryAfter})
					return
				case <-r.Context().Done():
					return
				}
			}
			g := &guestSlot{release: func() { <-slots }}
			defer g.free()
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), slotKey{}, g)))
		})
	}
}

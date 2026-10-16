package main

// THE PORTAL'S REQUEST DEADLINE, AND WHAT A CLIENT SEES WHEN IT IS REACHED (audit P-20).
//
// The router used chi's Timeout(15 s). A package acquisition runs quote (scd client, 5 s) + confirm (5 s) +
// activation (12 s: scd waits up to 8 s for netd to enforce the session), so on a slow appliance the deadline
// fired AFTER the grant had committed -- the client's access existed, and their browser received a blank 504.
//
// The deadline now covers the longest legitimate request with margin, and a request that still runs out without
// having answered gets a page in the client's language that says what to do: check whether the device is online
// before trying again (the Status page answers that), rather than paying or redeeming twice.

import (
	"context"
	"net/http"
	"time"
)

// portalRequestTimeout bounds one portal request: the acquisition's 22 s worst case, plus margin.
const portalRequestTimeout = 30 * time.Second

// answeredWriter records whether the handler has started its answer.
type answeredWriter struct {
	http.ResponseWriter
	answered bool
}

func (a *answeredWriter) WriteHeader(code int) {
	a.answered = true
	a.ResponseWriter.WriteHeader(code)
}

func (a *answeredWriter) Write(b []byte) (int, error) {
	a.answered = true
	return a.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (a *answeredWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }

func (h *handler) portalTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			aw := &answeredWriter{ResponseWriter: w}
			next.ServeHTTP(aw, r.WithContext(ctx))
			if ctx.Err() != context.DeadlineExceeded || aw.answered {
				return
			}
			// The request's own context is spent; the page is drawn on a fresh one (the design lookup is bounded).
			r2 := r.WithContext(context.Background())
			if wantsHTML(r) || (r.Method == http.MethodPost && r.Header.Get("X-OneGate-Nav") == "" &&
				r.Header.Get("Content-Type") == "application/x-www-form-urlencoded") {
				h.renderGuestNotice(w, r2, http.StatusGatewayTimeout, "err.timeout.title", "err.timeout", "/status", "online.status")
				return
			}
			writeJSONPortal(w, http.StatusGatewayTimeout, map[string]any{"error": "timeout"})
		})
	}
}

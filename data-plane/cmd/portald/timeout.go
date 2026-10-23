package main

// The deadline covers the longest legitimate request (an acquisition: quote 5 s + confirm 5 s + activation 12 s), because
// firing after a grant committed gave the client a blank 504. A request still unanswered gets a page telling the client to
// check the Status page before paying or redeeming twice.

import (
	"context"
	"net/http"
	"time"
)

// portalRequestTimeout bounds one portal request: the acquisition's 22 s worst case, plus margin.
const portalRequestTimeout = 30 * time.Second

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

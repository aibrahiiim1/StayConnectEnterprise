package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// requestLog is Central's request log (GDPR audit 2026-10-19). chi's middleware.Logger printed the full URI
// and the client address, so console searches and the people they named reached the journal. This prints the
// matched route pattern instead: what was served, never about whom.
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		defer func() {
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}
			slog.Info("http request", "method", r.Method, "route", route, "status", status,
				"bytes", ww.BytesWritten(), "ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()))
		}()
		next.ServeHTTP(ww, r)
	})
}

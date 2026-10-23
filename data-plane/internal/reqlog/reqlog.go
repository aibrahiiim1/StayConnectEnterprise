// Package reqlog logs the matched route pattern (e.g. "/edge/v1/usage/devices/{mac}"), never the path,
// query or client address: chi's middleware.Logger would put guest names, emails, phones, MACs and browsed
// hosts into the journal.
package reqlog

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		defer func() {
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			slog.Info("http request", "method", r.Method, "route", Route(r), "status", status,
				"bytes", ww.BytesWritten(), "ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()))
		}()
		next.ServeHTTP(ww, r)
	})
}

// Route is the pattern chi matched, or "unmatched" when no route did.
func Route(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}

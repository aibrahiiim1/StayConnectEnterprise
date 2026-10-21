// Package reqlog is the request log of the appliance's HTTP services (GDPR audit 2026-10-19, A1/A2).
//
// chi's middleware.Logger prints the full request URI and the client address. In Hotel Admin's API that put
// guest names, emails, phone numbers and MAC addresses typed into searches, and MACs in paths, into the
// journal; in the captive portal it recorded the host and path of every web page a guest's browser asked
// for before sign-in. This logger prints the matched ROUTE PATTERN instead (for example
// "/edge/v1/usage/devices/{mac}"), never the path, the query or the client address, so a log line says what
// was served without saying about whom.
package reqlog

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Middleware logs one line per request after it is served.
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

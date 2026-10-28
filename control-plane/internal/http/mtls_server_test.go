package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Audit Phase 1 M10: the :9443 server bounds slow bodies, writes, idle connections and headers, recovers a
// panicking handler and puts a deadline on each appliance request.
func TestMTLSServerLimits(t *testing.T) {
	var deadline time.Time
	var hasDeadline bool
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/appliance/panic" {
			panic("boom")
		}
		deadline, hasDeadline = r.Context().Deadline()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := newMTLSServer(":0", api)
	if srv.ReadTimeout != 30*time.Second || srv.WriteTimeout != 60*time.Second || srv.IdleTimeout != 120*time.Second ||
		srv.ReadHeaderTimeout == 0 || srv.MaxHeaderBytes != 64<<10 {
		t.Fatalf("server limits not set: %+v", srv)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/appliance/hello", nil).WithContext(context.Background()))
	if rec.Code != http.StatusNoContent || !hasDeadline || time.Until(deadline) > 15*time.Second {
		t.Fatalf("appliance request must carry a 15 s deadline: code %d deadline %v %v", rec.Code, hasDeadline, deadline)
	}
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/appliance/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a panicking handler must answer 500, got %d", rec.Code)
	}
}

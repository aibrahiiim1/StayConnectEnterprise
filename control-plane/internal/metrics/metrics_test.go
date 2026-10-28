package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Audit Phase 1 CC8: the metrics wrapper keeps http.Flusher visible and still records the status.
func TestMiddlewareKeepsFlusherAndCountsStatus(t *testing.T) {
	reg := New("test")
	r := chi.NewRouter()
	r.Use(reg.Middleware)
	var flusher bool
	r.Get("/x", func(w http.ResponseWriter, _ *http.Request) {
		_, flusher = w.(http.Flusher)
		w.WriteHeader(http.StatusTeapot)
	})
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))
	if !flusher {
		t.Fatal("handlers behind the metrics middleware must still see http.Flusher")
	}
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{`route="/x",status="418"`, `route="/ok",status="200"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %s:\n%s", want, body)
		}
	}
}

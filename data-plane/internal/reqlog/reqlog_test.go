package reqlog

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The log line names the route, never the path, the query or the client address.
func TestMiddlewareLogsTheRouteNotThePerson(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/edge/v1/usage/devices/{mac}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	r.Get("/search", func(http.ResponseWriter, *http.Request) {})

	for _, target := range []string{"/edge/v1/usage/devices/aa:bb:cc:dd:ee:ff", "/search?q=john.smith@example.test", "/no/such/page.example.com"} {
		req := httptest.NewRequest(http.MethodGet, "http://captive.example.org"+target, nil)
		req.RemoteAddr = "10.10.0.42:5555"
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	out := buf.String()
	for _, leak := range []string{"aa:bb:cc", "john.smith", "example.test", "10.10.0.42", "captive.example.org", "page.example.com"} {
		if strings.Contains(out, leak) {
			t.Errorf("the request log holds %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"route=/edge/v1/usage/devices/{mac}", "status=418", "route=/search", "route=unmatched", "status=404"} {
		if !strings.Contains(out, want) {
			t.Errorf("the request log lacks %q:\n%s", want, out)
		}
	}
}

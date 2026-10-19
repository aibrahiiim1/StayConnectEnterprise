package http

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRequestLogNamesTheRouteNotThePerson(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	r := chi.NewRouter()
	r.Use(requestLog)
	r.Get("/cloud/v1/customers/{id}/users", func(http.ResponseWriter, *http.Request) {})
	req := httptest.NewRequest(http.MethodGet, "/cloud/v1/customers/c1/users?q=jane.doe@example.test", nil)
	req.RemoteAddr = "198.51.100.77:4000"
	r.ServeHTTP(httptest.NewRecorder(), req)
	out := buf.String()
	if strings.Contains(out, "jane.doe") || strings.Contains(out, "198.51.100.77") || !strings.Contains(out, "route=/cloud/v1/customers/{id}/users") {
		t.Fatalf("request log: %s", out)
	}
}

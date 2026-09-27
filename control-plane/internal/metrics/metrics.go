// Package metrics defines ctrlapi's Prometheus metric surface: build info, uptime and the HTTP layer.
// Route labels use chi's route pattern (not the raw URL) to keep cardinality bounded. The handler is served
// to loopback only (see internal/http.loopbackOnly).
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Registry struct {
	reg *prometheus.Registry

	BuildInfo prometheus.Gauge
	Uptime    prometheus.GaugeFunc

	HTTPRequests *prometheus.CounterVec   // labels: method, route, status
	HTTPDuration *prometheus.HistogramVec // labels: method, route
}

func New(version string) *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	r := &Registry{reg: reg}

	r.BuildInfo = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ctrlapi_build_info", Help: "1 with version label",
		ConstLabels: prometheus.Labels{"version": version},
	})
	r.BuildInfo.Set(1)

	started := time.Now()
	r.Uptime = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "ctrlapi_uptime_seconds", Help: "Seconds since ctrlapi started.",
	}, func() float64 { return time.Since(started).Seconds() })

	r.HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ctrlapi_http_requests_total",
		Help: "HTTP requests handled, by method, chi route pattern, and status.",
	}, []string{"method", "route", "status"})

	r.HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ctrlapi_http_request_duration_seconds",
		Help:    "HTTP handler latency, in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	reg.MustRegister(r.BuildInfo, r.Uptime, r.HTTPRequests, r.HTTPDuration)
	return r
}

func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// Middleware records HTTPRequests + HTTPDuration. Must be mounted AFTER
// chi routes are populated so chi.RouteContext().RoutePattern() returns
// the matched pattern (e.g. "/cloud/v1/appliances/{id}") instead of empty.
func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(ww, req)
		dur := time.Since(start).Seconds()

		route := chi.RouteContext(req.Context()).RoutePattern()
		if route == "" {
			route = "unmatched" // 404s, OPTIONS preflight, etc.
		}
		r.HTTPRequests.WithLabelValues(req.Method, route, strconv.Itoa(ww.status)).Inc()
		r.HTTPDuration.WithLabelValues(req.Method, route).Observe(dur)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

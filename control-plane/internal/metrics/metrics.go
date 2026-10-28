// Package metrics defines ctrlapi's Prometheus metrics; route labels use chi's pattern to bound cardinality.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Registry struct {
	reg *prometheus.Registry

	BuildInfo prometheus.Gauge
	Uptime    prometheus.GaugeFunc

	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec
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

// Middleware must be mounted so chi's RoutePattern() is populated when the request finishes.
func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// chi's wrapper keeps http.Flusher and http.Hijacker visible to handlers (audit Phase 1 CC8).
		ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, req)
		dur := time.Since(start).Seconds()

		route := chi.RouteContext(req.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		r.HTTPRequests.WithLabelValues(req.Method, route, strconv.Itoa(status)).Inc()
		r.HTTPDuration.WithLabelValues(req.Method, route).Observe(dur)
	})
}

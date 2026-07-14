// Package metrics defines ctrlapi's Prometheus metric surface.
//
// Unlike scd, ctrlapi serves many tenants — tenant_id is a per-request
// label on tenant-scoped metrics. HTTP-level metrics use chi's route
// pattern (not raw URL) to keep cardinality bounded.
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

	// HTTP layer.
	HTTPRequests *prometheus.CounterVec   // labels: method, route, status
	HTTPDuration *prometheus.HistogramVec // labels: method, route

	// Appliance lifecycle.
	HeartbeatsReceived *prometheus.CounterVec // labels: tenant_id
	AppliancesByStatus *prometheus.GaugeVec   // labels: tenant_id, status
	AppliancesOffline  prometheus.Counter     // total flips to offline
	TokensSwept        prometheus.Counter
	ConfigPushed       *prometheus.CounterVec // labels: subsystem, action

	// NATS RPC view from this side.
	NATSRPCRequests *prometheus.CounterVec // labels: method, result

	// Phase 12 — payments.
	PaymentCheckoutTotal *prometheus.CounterVec // labels: result
	PaymentWebhookTotal  *prometheus.CounterVec // labels: result
	PaymentAmountCents   *prometheus.CounterVec // labels: tenant_id, currency
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

	r.HeartbeatsReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ctrlapi_heartbeats_received_total",
		Help: "scd heartbeats consumed, by tenant.",
	}, []string{"tenant_id"})

	r.AppliancesByStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ctrlapi_appliances_status_count",
		Help: "Appliances per (tenant, status). Refreshed by the heartbeat sweeper.",
	}, []string{"tenant_id", "status"})

	r.AppliancesOffline = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ctrlapi_appliances_flipped_offline_total",
		Help: "Times the heartbeat staleness sweeper flipped a row to offline.",
	})

	r.TokensSwept = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ctrlapi_bootstrap_tokens_swept_total",
		Help: "Expired unconsumed bootstrap tokens deleted by the sweeper.",
	})

	r.ConfigPushed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ctrlapi_config_pushed_total",
		Help: "Config events published to NATS, by subsystem and action.",
	}, []string{"subsystem", "action"})

	r.NATSRPCRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ctrlapi_nats_rpc_requests_total",
		Help: "ApplianceTransport calls over NATS, by method and result.",
	}, []string{"method", "result"})

	r.PaymentCheckoutTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ctrlapi_payment_checkout_total",
		Help: "Checkout-session create attempts, by result.",
	}, []string{"result"})

	r.PaymentWebhookTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ctrlapi_payment_webhook_total",
		Help: "Stripe webhook deliveries consumed, by result.",
	}, []string{"result"})

	r.PaymentAmountCents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ctrlapi_payment_amount_cents_total",
		Help: "Cumulative cents successfully charged, by tenant and currency.",
	}, []string{"tenant_id", "currency"})

	reg.MustRegister(
		r.BuildInfo, r.Uptime,
		r.HTTPRequests, r.HTTPDuration,
		r.HeartbeatsReceived, r.AppliancesByStatus, r.AppliancesOffline,
		r.TokensSwept, r.ConfigPushed, r.NATSRPCRequests,
		r.PaymentCheckoutTotal, r.PaymentWebhookTotal, r.PaymentAmountCents,
	)
	// Pre-touch checkout/webhook result combos so dashboards have
	// materialised series even before first traffic.
	for _, res := range []string{"ok", "bad_body", "not_configured", "bad_template",
		"insert_failed", "stripe_error", "internal"} {
		r.PaymentCheckoutTotal.WithLabelValues(res).Add(0)
	}
	for _, res := range []string{"ok", "signature_fail", "idempotent_skip",
		"ignored_type", "unpaid", "voucher_fail", "no_account", "read_failed",
		"parse_failed", "dedupe_failed"} {
		r.PaymentWebhookTotal.WithLabelValues(res).Add(0)
	}
	return r
}

func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// Middleware records HTTPRequests + HTTPDuration. Must be mounted AFTER
// chi routes are populated so chi.RouteContext().RoutePattern() returns
// the matched pattern (e.g. "/v1/sessions/{id}") instead of empty.
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

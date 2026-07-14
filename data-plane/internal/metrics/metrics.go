// Package metrics defines scd's Prometheus metric surface.
//
// All metrics live in a single registry exposed at /metrics on scd's
// Unix socket. scd is single-tenant + single-site + single-appliance, so
// tenant_id / site_id / appliance_id are constant labels attached at
// registration time — there's nothing scrape-side relabeling can't add,
// but baking them in makes the labels visible without external config.
//
// Cardinality budget per series ≈ O(1) for gauges + O(N) for counters
// where N is the small fixed set of methods/reasons/results we track.
// Don't add user-supplied strings as labels (room numbers, IPs, names).
package metrics

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds the per-process Prometheus registry plus the metric
// handles. Build via New(); call Handler() to mount the HTTP handler.
type Registry struct {
	reg *prometheus.Registry

	BuildInfo prometheus.Gauge
	Uptime    prometheus.GaugeFunc

	SessionsActive    prometheus.Gauge
	SessionsStarted   *prometheus.CounterVec // labels: method
	SessionsClosed    *prometheus.CounterVec // labels: reason
	SessionBytesTotal *prometheus.CounterVec // labels: direction (up|down)

	OTPIssued  *prometheus.CounterVec // labels: channel (email|sms)
	OTPVerify  *prometheus.CounterVec // labels: channel, result (ok|bad|expired|locked)

	PMSValidate         *prometheus.CounterVec   // labels: provider, result
	PMSValidateDuration *prometheus.HistogramVec // labels: provider
	PMSStatus           *prometheus.GaugeVec     // labels: provider, kind; value = enum
	PMSCacheSize        *prometheus.GaugeVec     // labels: provider, kind

	NFTOps         *prometheus.CounterVec // labels: op (add|del), source (local|peer|reaper)
	ReaperClosed   *prometheus.CounterVec // labels: reason
	NATSReconnects prometheus.Counter

	// Phase 8 — notification provider observability.
	NotifySendTotal    *prometheus.CounterVec   // labels: channel, provider, result
	NotifySendDuration *prometheus.HistogramVec // labels: channel, provider

	// Phase 9 — social login observability.
	SocialLoginTotal    *prometheus.CounterVec   // labels: provider, result
	SocialLoginDuration *prometheus.HistogramVec // labels: provider
}

// New builds a fresh registry preloaded with Go runtime + process collectors
// and the application metrics. constLabels typically carries
// {tenant_id, site_id, appliance_id} so every series renders with them.
func New(version string, constLabels prometheus.Labels) *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	r := &Registry{reg: reg}

	r.BuildInfo = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "scd_build_info", Help: "1 with version label",
		ConstLabels: mergeLabels(constLabels, prometheus.Labels{"version": version}),
	})
	r.BuildInfo.Set(1)

	started := time.Now()
	r.Uptime = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "scd_uptime_seconds", Help: "Seconds since scd started.",
		ConstLabels: constLabels,
	}, func() float64 { return time.Since(started).Seconds() })

	r.SessionsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "scd_sessions_active",
		Help: "Sessions currently in state=active per the local DB view.",
		ConstLabels: constLabels,
	})

	r.SessionsStarted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_sessions_started_total",
		Help: "Sessions started, by auth method.",
		ConstLabels: constLabels,
	}, []string{"method"})

	r.SessionsClosed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_sessions_closed_total",
		Help: "Sessions closed, by reason. Matches sessions.end_reason.",
		ConstLabels: constLabels,
	}, []string{"reason"})

	r.SessionBytesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_session_bytes_total",
		Help: "Cumulative bytes attributed to closed sessions, by direction.",
		ConstLabels: constLabels,
	}, []string{"direction"})

	r.OTPIssued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_otp_issued_total",
		Help: "OTP codes issued, by channel.",
		ConstLabels: constLabels,
	}, []string{"channel"})

	r.OTPVerify = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_otp_verify_total",
		Help: "OTP verification attempts, by channel and result.",
		ConstLabels: constLabels,
	}, []string{"channel", "result"})

	r.PMSValidate = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_pms_validate_total",
		Help: "PMS guest-validation calls, by provider and result.",
		ConstLabels: constLabels,
	}, []string{"provider", "result"})

	r.PMSValidateDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "scd_pms_validate_duration_seconds",
		Help: "Latency of PMS ValidateGuest calls, by provider.",
		// REST providers can take >1s on first request; FIAS is local-cache
		// fast. Same buckets as notification_send covers both.
		Buckets:     []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		ConstLabels: constLabels,
	}, []string{"provider"})

	r.PMSStatus = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "scd_pms_provider_status",
		Help: "Provider link state (0=down, 1=degraded, 2=connecting, 3=connected, 4=idle).",
		ConstLabels: constLabels,
	}, []string{"provider", "kind"})

	r.PMSCacheSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "scd_pms_cache_size",
		Help: "Reservations currently held in the provider's local cache.",
		ConstLabels: constLabels,
	}, []string{"provider", "kind"})

	r.NFTOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_nft_ops_total",
		Help: "nft auth_ipv4 set mutations, by op and source.",
		ConstLabels: constLabels,
	}, []string{"op", "source"})

	r.ReaperClosed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_reaper_closed_total",
		Help: "Sessions closed by the background reaper, by reason.",
		ConstLabels: constLabels,
	}, []string{"reason"})

	r.NATSReconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "scd_nats_reconnects_total",
		Help: "Times the NATS client reconnected after a drop.",
		ConstLabels: constLabels,
	})

	r.NotifySendTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_notification_send_total",
		Help: "Outbound email/SMS sends, by channel, provider, and result.",
		ConstLabels: constLabels,
	}, []string{"channel", "provider", "result"})

	r.NotifySendDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "scd_notification_send_duration_seconds",
		Help: "Latency of notification provider Send calls, by channel/provider.",
		// 25ms .. 10s — covers cheap dev stubs through slow upstream APIs.
		Buckets: []float64{0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		ConstLabels: constLabels,
	}, []string{"channel", "provider"})

	r.SocialLoginTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scd_social_login_total",
		Help: "Social-OAuth callbacks completed, by provider and result.",
		ConstLabels: constLabels,
	}, []string{"provider", "result"})

	r.SocialLoginDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "scd_social_login_duration_seconds",
		Help: "Latency of the social Exchange (token swap + userinfo round-trip).",
		// Token + userinfo round-trip; same shape as notify_send.
		Buckets: []float64{0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		ConstLabels: constLabels,
	}, []string{"provider"})

	reg.MustRegister(
		r.BuildInfo, r.Uptime,
		r.SessionsActive, r.SessionsStarted, r.SessionsClosed, r.SessionBytesTotal,
		r.OTPIssued, r.OTPVerify,
		r.PMSValidate, r.PMSValidateDuration, r.PMSStatus, r.PMSCacheSize,
		r.NFTOps, r.ReaperClosed, r.NATSReconnects,
		r.NotifySendTotal, r.NotifySendDuration,
		r.SocialLoginTotal, r.SocialLoginDuration,
	)

	// Pre-touch every known label combo for the counters we care about
	// so the series exists in /metrics output even before the first
	// Inc(). Counters with no samples are omitted by default — that's
	// fine in production but trips up dashboards / alerts that expect
	// the series to be present.
	for _, m := range []string{"voucher", "otp", "pms", "social"} {
		r.SessionsStarted.WithLabelValues(m).Add(0)
	}
	for _, reason := range []string{"admin", "quota_time", "quota_bytes", "idle", "dhcp_expired", "policy"} {
		r.SessionsClosed.WithLabelValues(reason).Add(0)
		r.ReaperClosed.WithLabelValues(reason).Add(0)
	}
	for _, dir := range []string{"up", "down"} {
		r.SessionBytesTotal.WithLabelValues(dir).Add(0)
	}
	for _, ch := range []string{"email", "sms"} {
		r.OTPIssued.WithLabelValues(ch).Add(0)
	}
	for _, op := range []string{"add", "del"} {
		for _, src := range []string{"local", "peer"} {
			r.NFTOps.WithLabelValues(op, src).Add(0)
		}
	}
	for _, ch := range []string{"email", "sms"} {
		for _, prov := range []string{"stub", "sendgrid", "twilio", "ses"} {
			for _, res := range []string{"ok", "failed"} {
				r.NotifySendTotal.WithLabelValues(ch, prov, res).Add(0)
			}
		}
	}
	for _, prov := range []string{"google", "apple", "facebook", "microsoft", "stub"} {
		for _, res := range []string{"ok", "failed", "email_unverified", "bad_state"} {
			r.SocialLoginTotal.WithLabelValues(prov, res).Add(0)
		}
	}
	return r
}

// Handler returns the /metrics HTTP handler.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

// PMSStatusValue maps a pms.Health.Status string to the gauge enum.
// Kept here so metric translation isn't sprinkled across handlers.
func PMSStatusValue(status string) float64 {
	switch status {
	case "connected":
		return 3
	case "connecting":
		return 2
	case "degraded":
		return 1
	case "idle":
		return 4
	default: // "down" or unknown
		return 0
	}
}

// mergeLabels combines two label maps; keys in extra win on collision.
func mergeLabels(base, extra prometheus.Labels) prometheus.Labels {
	if len(base) == 0 {
		return extra
	}
	out := make(prometheus.Labels, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// active-sessions sampling: a tiny helper so we don't proliferate the
// gauge across multiple goroutines fighting to write it.
var (
	activeMu sync.Mutex
)

// SetActive atomically updates the active-sessions gauge. Safe for
// concurrent callers (heartbeat / reaper / authorize paths).
func (r *Registry) SetActive(n int) {
	activeMu.Lock()
	defer activeMu.Unlock()
	r.SessionsActive.Set(float64(n))
}

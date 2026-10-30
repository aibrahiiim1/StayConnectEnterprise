package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PoolStats is a snapshot of a database connection pool, kept free of any driver type so this package does not import one.
type PoolStats struct {
	Acquired, Idle, Max int32
	// Acquires counts every acquisition; EmptyAcquires those that had to wait because no idle connection was ready;
	// CanceledAcquires those that gave up (the caller's deadline passed) before they got one.
	Acquires, EmptyAcquires, CanceledAcquires int64
	// AcquireWait is the total time EmptyAcquires waited.
	AcquireWait time.Duration
}

// WatchPool exposes the pool's occupancy and its waiting at /metrics. A pool that runs out of connections shows nowhere else: the
// requests queue inside the process with the database idle (capacity acceptance, 2026-10-30).
func (r *Registry) WatchPool(name string, read func() PoolStats, constLabels prometheus.Labels) {
	l := prometheus.Labels{"pool": name}
	for k, v := range constLabels {
		l[k] = v
	}
	g := func(n, h string, f func(PoolStats) float64) {
		r.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: n, Help: h, ConstLabels: l}, func() float64 { return f(read()) }))
	}
	c := func(n, h string, f func(PoolStats) float64) {
		r.reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: n, Help: h, ConstLabels: l}, func() float64 { return f(read()) }))
	}
	g("scd_db_pool_acquired_connections", "Connections checked out of the pool right now.", func(s PoolStats) float64 { return float64(s.Acquired) })
	g("scd_db_pool_idle_connections", "Idle connections in the pool right now.", func(s PoolStats) float64 { return float64(s.Idle) })
	g("scd_db_pool_max_connections", "The pool's connection limit.", func(s PoolStats) float64 { return float64(s.Max) })
	c("scd_db_pool_acquires_total", "Connection acquisitions.", func(s PoolStats) float64 { return float64(s.Acquires) })
	c("scd_db_pool_acquires_waited_total", "Acquisitions that had to wait for a connection.", func(s PoolStats) float64 { return float64(s.EmptyAcquires) })
	c("scd_db_pool_acquires_canceled_total", "Acquisitions abandoned because the caller's deadline passed.", func(s PoolStats) float64 { return float64(s.CanceledAcquires) })
	c("scd_db_pool_acquire_wait_seconds_total", "Total time acquisitions waited for a connection.", func(s PoolStats) float64 { return s.AcquireWait.Seconds() })
}

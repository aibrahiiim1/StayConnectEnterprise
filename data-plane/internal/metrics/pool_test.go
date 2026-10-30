package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestPoolStatsAreExposedAndFollowThePool(t *testing.T) {
	r := New("test", prometheus.Labels{"tenant_id": "t"})
	cur := PoolStats{Acquired: 15, Idle: 1, Max: 16, Acquires: 100, EmptyAcquires: 40, CanceledAcquires: 3, AcquireWait: 1500 * time.Millisecond}
	r.WatchPool("scd", func() PoolStats { return cur }, prometheus.Labels{"tenant_id": "t"})
	body := func() string {
		rec := httptest.NewRecorder()
		r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		return rec.Body.String()
	}
	b := body()
	for _, want := range []string{
		`scd_db_pool_acquired_connections{pool="scd",tenant_id="t"} 15`,
		`scd_db_pool_idle_connections{pool="scd",tenant_id="t"} 1`,
		`scd_db_pool_max_connections{pool="scd",tenant_id="t"} 16`,
		`scd_db_pool_acquires_waited_total{pool="scd",tenant_id="t"} 40`,
		`scd_db_pool_acquires_canceled_total{pool="scd",tenant_id="t"} 3`,
		`scd_db_pool_acquire_wait_seconds_total{pool="scd",tenant_id="t"} 1.5`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	cur.Acquired = 2
	if !strings.Contains(body(), `scd_db_pool_acquired_connections{pool="scd",tenant_id="t"} 2`) {
		t.Error("the gauge did not follow the pool")
	}
}

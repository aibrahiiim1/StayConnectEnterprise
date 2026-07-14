package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// ---- /v1/admin/pms/{name}/test -- one-shot connectivity probe ---------------

type pmsTestResp struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

func (s *server) pmsAdminTest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	prov, ok := s.currentPMSReg().Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, pmsTestResp{Error: "provider not registered"})
		return
	}
	t, ok := prov.(pms.Tester)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, pmsTestResp{Error: "provider doesn't support TestConnection"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := t.TestConnection(ctx); err != nil {
		writeJSON(w, http.StatusBadGateway, pmsTestResp{
			OK: false, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, pmsTestResp{
		OK: true, LatencyMS: time.Since(start).Milliseconds(),
	})
}

// ---- /v1/admin/pms/{name}/cache -- inspect the in-memory lookup table -------

func (s *server) pmsAdminCache(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	prov, ok := s.currentPMSReg().Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not registered"})
		return
	}
	c, ok := prov.(pms.Cacher)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "provider doesn't support cache snapshot"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows := c.CacheSnapshot(limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": name,
		"kind":     prov.Kind(),
		"count":    len(rows),
		"rows":     rows,
	})
}

// ---- /v1/admin/pms/{name}/health -- live snapshot ---------------------------

func (s *server) pmsAdminHealth(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	prov, ok := s.currentPMSReg().Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not registered"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider": name,
		"kind":     prov.Kind(),
		"health":   prov.Health(),
	})
}

// ---- background loop: flush in-memory Health to pms_providers ---------------

// pmsHealthFlushLoop periodically copies each registered provider's Health
// into the matching pms_providers row so the admin UI can see status without
// going through the appliance.
func (s *server) pmsHealthFlushLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pmsHealthFlushOnce(ctx)
		}
	}
}

func (s *server) pmsHealthFlushOnce(ctx context.Context) {
	s.pmsMu.RLock()
	built := s.pmsBuilt
	s.pmsMu.RUnlock()
	for _, p := range built {
		h := p.Health()
		var lastRecord, lastErrAt any
		if !h.LastRecordAt.IsZero() {
			lastRecord = h.LastRecordAt
		}
		if !h.LastErrorAt.IsZero() {
			lastErrAt = h.LastErrorAt
		}
		var lastErr any
		if h.LastError != "" {
			lastErr = h.LastError
		}
		_, _ = s.db.Exec(ctx, `
            UPDATE pms_providers
               SET status         = $3,
                   last_record_at = $4,
                   last_error     = $5,
                   last_error_at  = $6,
                   updated_at     = now()
             WHERE tenant_id = $1 AND name = $2
        `, s.tenID, p.Name(), h.Status, lastRecord, lastErr, lastErrAt)

		// Mirror the same snapshot into Prometheus gauges. Same cadence
		// as the DB flush (30s) keeps both views in sync.
		s.met.PMSStatus.WithLabelValues(p.Name(), p.Kind()).Set(metrics.PMSStatusValue(h.Status))
		s.met.PMSCacheSize.WithLabelValues(p.Name(), p.Kind()).Set(float64(h.CacheSize))
	}

	// Active-sessions gauge — sampled here on the same 30s tick (cheap
	// COUNT, partial index makes it constant-time).
	var n int
	if err := s.db.QueryRow(ctx, `
        SELECT count(*) FROM sessions
         WHERE tenant_id = $1 AND site_id = $2 AND state = 'active'
    `, s.tenID, s.siteID).Scan(&n); err == nil {
		s.met.SetActive(n)
	}
}

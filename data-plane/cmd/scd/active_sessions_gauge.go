package main

import (
	"context"
	"time"
)

// activeSessionsGaugeLoop samples the number of active sessions for the metrics gauge. It is what remained of the legacy PMS
// provider health flush (audit AD-39), which also copied provider health into public.pms_providers; the legacy provider layer
// is gone and pmsd owns every PMS connection.
func (s *server) activeSessionsGaugeLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sampleActiveSessions(ctx)
		}
	}
}

func (s *server) sampleActiveSessions(ctx context.Context) {
	var n int
	if err := s.db.QueryRow(ctx, `
        SELECT count(*) FROM iam_v2.sessions
         WHERE tenant_id = $1 AND site_id = $2 AND state = 'active'
    `, s.tenID, s.siteID).Scan(&n); err == nil {
		s.met.SetActive(n)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	bundleLogLines      = 60
	bundleErrorScan     = 2000
	bundleErrorsKept    = 50
	bundleFormat        = "onegate-diagnostics/1"
	bundleRequestFailed = "request failed"
)

type diagnosticsParts struct {
	At       time.Time
	Version  string
	SiteID   string
	Licence  map[string]any
	Services map[string]any
	Recovery []recoveryEvent
	Monitor  map[string]any
	Logs     map[string][]string
	Errors   []string
}

func diagnosticsBundle(p diagnosticsParts) ([]byte, error) {
	events := make([]map[string]any, 0, len(p.Recovery))
	for _, e := range p.Recovery {
		events = append(events, map[string]any{
			"service": e.Service, "event": e.Event, "cause": e.Cause, "action": e.Action,
			"backoff_level": e.BackoffLevel, "result": e.Result, "duration_ms": e.DurationMS, "created_at": e.CreatedAt,
		})
	}
	logs := p.Logs
	if logs == nil {
		logs = map[string][]string{}
	}
	errs := p.Errors
	if errs == nil {
		errs = []string{}
	}
	raw, err := json.Marshal(map[string]any{
		"format":          bundleFormat,
		"generated_at":    p.At.UTC(),
		"appliance":       map[string]any{"version": p.Version, "site_id": p.SiteID, "licence": p.Licence},
		"services":        p.Services,
		"recovery_events": events,
		"monitor":         p.Monitor,
		"recent_errors":   errs,
		"logs":            logs,
	})
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(redactMonitor(v), "", "  ")
}

func recentRequestFailures(lines []string, keep int) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, bundleRequestFailed) {
			out = append(out, l)
		}
	}
	if len(out) > keep {
		out = out[len(out)-keep:]
	}
	return out
}

func bundleFileName(at time.Time) string {
	return "onegate-diagnostics-" + at.UTC().Format("20060102-150405") + "Z.json"
}

func (s *server) healthBundle(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	at := time.Now()
	p := diagnosticsParts{At: at, Version: version, SiteID: s.siteID, Logs: map[string][]string{}}
	if sum, ok := s.readLicenceSummary(ctx); ok {
		p.Licence = map[string]any{"activation": sum.Activation, "state": sum.State, "valid_until": sum.ValidUntil,
			"grace_ends_at": sum.GraceEndsAt, "days_left": sum.DaysLeft, "central": sum.Central}
	}
	p.Services = s.servicesAnswer(ctx)
	p.Recovery = s.recoveryEvents(ctx, "", 100)
	p.Monitor = monitorReport()
	for _, sp := range s.services() {
		p.Logs[sp.Name] = journalLines(ctx, sp.Unit, bundleLogLines)
		if sp.Name == "edged" {
			p.Errors = recentRequestFailures(journalLines(ctx, sp.Unit, bundleErrorScan), bundleErrorsKept)
		}
	}
	body, err := diagnosticsBundle(p)
	if err != nil {
		internalErr(w, r, http.StatusInternalServerError, "bundle_failed", err)
		return
	}
	s.audit(r, "health.diagnostics_downloaded", "appliance", s.siteID, nil)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+bundleFileName(at)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

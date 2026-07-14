package main

import (
	"context"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// healthRoutes backs the Hotel Admin Health & Diagnostics page. Read endpoints
// require the "health" resource read permission; operator actions require write;
// the destructive Restart additionally requires password step-up + a reason and
// is audited. No secrets, credentials, keys or guest PII are ever returned.
func (s *server) healthRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/services", s.healthServices)
	r.Get("/services/{name}", s.healthService)
	r.Post("/services/{name}/recheck", s.healthRecheck)
	r.Post("/services/{name}/restart", s.healthRestart)
	r.Get("/services/{name}/logs", s.healthLogs)
	r.Get("/recovery-events", s.healthRecoveryEvents)
	return r
}

// unitFor maps a health name to its systemd unit, enforcing the whitelist so an
// operator action can never touch an arbitrary unit.
func (s *server) unitFor(name string) (string, bool) {
	for _, sp := range s.services() {
		if sp.Name == name {
			return sp.Unit, true
		}
	}
	return "", false
}

func (s *server) healthServices(w http.ResponseWriter, r *http.Request) {
	all := s.allHealth(r.Context())
	overall, counts := overallHealth(all)
	var boot map[string]any
	{
		var converged, alertOpen bool
		var pending []string
		var bootAt, convergedAt, deadline *time.Time
		if s.db.QueryRow(r.Context(), `SELECT converged, alert_open, pending_services, boot_at, converged_at, deadline_at
		    FROM appliance_boot_convergence WHERE id`).Scan(&converged, &alertOpen, &pending, &bootAt, &convergedAt, &deadline) == nil {
			boot = map[string]any{"converged": converged, "alert_open": alertOpen, "pending": pending,
				"boot_at": bootAt, "converged_at": convergedAt, "deadline_at": deadline}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"overall": overall, "counts": counts, "services": all, "boot": boot,
		"generated_at": time.Now().UTC(),
	})
}

func (s *server) healthService(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if _, ok := s.unitFor(name); !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "unknown service")
		return
	}
	h := s.loadHealth(r.Context(), name)
	events := s.recoveryEvents(r.Context(), name, 25)
	writeJSON(w, http.StatusOK, map[string]any{"service": h, "recovery_events": events})
}

func (s *server) healthRecheck(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	for _, sp := range s.services() {
		if sp.Name == name {
			s.pollOne(r.Context(), sp)
			s.recordRecovery(r.Context(), name, "manual_recheck", "", "operator forced a health re-check", 0, "", 0, actorOf(r))
			s.audit(r, "health.recheck", "service", name, nil)
			writeJSON(w, http.StatusOK, map[string]any{"service": s.loadHealth(r.Context(), name)})
			return
		}
	}
	jsonErr(w, http.StatusNotFound, "not_found", "unknown service")
}

func (s *server) healthRestart(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	unit, ok := s.unitFor(name)
	if !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "unknown service")
		return
	}
	var in struct {
		Password string `json:"password"`
		Reason   string `json:"reason"`
	}
	_ = decodeJSON(r, &in)
	if strings.TrimSpace(in.Reason) == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a reason is required")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	s.audit(r, "health.restart_requested", "service", name, map[string]any{"reason": in.Reason})
	// Reset any start-limit wedge first, then restart. systemctl is gated by a
	// polkit rule to only the whitelisted stayconnect/kea/unbound units.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", unit).Run()
	out, err := exec.CommandContext(ctx, "systemctl", "restart", unit).CombinedOutput()
	if err != nil {
		s.recordRecovery(r.Context(), name, "manual_restart", in.Reason, "systemctl restart", 0, "failed: "+errShort(err), 0, actorOf(r))
		jsonErr(w, http.StatusInternalServerError, "restart_failed", sanitizeDetail(string(out)))
		return
	}
	s.recordRecovery(r.Context(), name, "manual_restart", in.Reason, "systemctl restart", 0, "issued", 0, actorOf(r))
	writeJSON(w, http.StatusOK, map[string]any{"status": "restart_issued", "service": name})
}

var (
	reIP     = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+`)
	reMAC    = regexp.MustCompile(`(?i)\b([0-9a-f]{2}:){5}[0-9a-f]{2}\b`)
	reSecret = regexp.MustCompile(`(?i)(password|token|secret|authorization|api[_-]?key)\s*[=:]\s*\S+`)
)

// sanitizeLog strips IPs, emails, MACs and secret-looking assignments from a log
// line so recent logs can be shown in the UI without leaking guest PII/secrets.
func sanitizeLog(line string) string {
	line = reSecret.ReplaceAllString(line, "$1=[redacted]")
	line = reEmail.ReplaceAllString(line, "[email]")
	line = reMAC.ReplaceAllString(line, "[mac]")
	line = reIP.ReplaceAllString(line, "[ip]")
	if len(line) > 500 {
		line = line[:500]
	}
	return line
}

func (s *server) healthLogs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	unit, ok := s.unitFor(name)
	if !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "unknown service")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", "60", "--no-pager", "-o", "short-iso").CombinedOutput()
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines = append(lines, sanitizeLog(l))
	}
	s.audit(r, "health.logs_viewed", "service", name, nil)
	writeJSON(w, http.StatusOK, map[string]any{"service": name, "lines": lines})
}

func (s *server) healthRecoveryEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"events": s.recoveryEvents(r.Context(), "", 100)})
}

type recoveryEvent struct {
	ID           int64     `json:"id"`
	Service      string    `json:"service"`
	Event        string    `json:"event"`
	Cause        string    `json:"cause"`
	Action       string    `json:"action"`
	BackoffLevel int       `json:"backoff_level"`
	Result       string    `json:"result"`
	DurationMS   int64     `json:"duration_ms"`
	Actor        string    `json:"actor"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *server) recoveryEvents(ctx context.Context, service string, limit int) []recoveryEvent {
	q := `SELECT id, service, event, COALESCE(cause,''), COALESCE(action,''), COALESCE(backoff_level,0),
	    COALESCE(result,''), COALESCE(duration_ms,0), actor, created_at FROM appliance_recovery_events`
	args := []any{}
	if service != "" {
		q += ` WHERE service=$1`
		args = append(args, service)
	}
	q += ` ORDER BY id DESC LIMIT ` + itoa(limit)
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []recoveryEvent
	for rows.Next() {
		var e recoveryEvent
		if rows.Scan(&e.ID, &e.Service, &e.Event, &e.Cause, &e.Action, &e.BackoffLevel, &e.Result, &e.DurationMS, &e.Actor, &e.CreatedAt) == nil {
			out = append(out, e)
		}
	}
	return out
}

func actorOf(r *http.Request) string {
	if sess := sessFrom(r.Context()); sess != nil {
		return sess.OperatorID
	}
	return "operator"
}

func itoa(n int) string {
	if n <= 0 {
		n = 100
	}
	return strconv.Itoa(n)
}

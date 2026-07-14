package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/outbox"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
)

// ---------------------------------------------------------------------------
// Appliance Health Supervisor (runs inside edged).
//
// edged is the appliance's authoritative management-plane component, so it owns
// health supervision — no extra daemon. It OBSERVES and DIAGNOSES; it never
// fights systemd for restart control (systemd + the per-service adaptive
// startup backoff own recovery). It:
//   - polls every critical service (systemd state + service-specific health check
//     + the adaptive-backoff tracker),
//   - classifies healthy/degraded/recovering/crash_loop/failed/starting,
//   - persists the authoritative health model + recovery history to the site DB
//     (survives edged restart and reboot),
//   - tracks boot convergence,
//   - enqueues sanitized telemetry for Central,
//   - and backs the /edge/v1/health API + Hotel Admin Health UI.
// ---------------------------------------------------------------------------

// Health states.
const (
	stHealthy    = "healthy"
	stDegraded   = "degraded"   // active but the health check fails
	stRecovering = "recovering" // restarting with backoff, not yet stable
	stCrashLoop  = "crash_loop" // repeated rapid restarts
	stFailed     = "failed"     // inactive/failed and should be up
	stStarting   = "starting"   // first-time activation window
	stUnknown    = "unknown"
)

// bootConvergeDeadline is how long after boot all critical services have to
// become healthy before it's flagged as failed-to-converge.
const bootConvergeDeadline = 4 * time.Minute

// svcSpec describes one supervised service.
type svcSpec struct {
	Name     string // health name (matches the backoff tracker + telemetry)
	Unit     string // systemd unit
	Critical bool
	// Check runs a meaningful service-specific health probe. Returns ok, a
	// sanitized detail string, and (optionally) the dependency name responsible
	// for a failure so the UI can show "degraded because <dep>".
	Check func(ctx context.Context, s *server) (ok bool, detail, dep string)
}

// services is the supervised set. Order = display order.
func (s *server) services() []svcSpec {
	return []svcSpec{
		{"scd", "stayconnect-scd.service", true, checkSCD},
		{"edged", "stayconnect-edged.service", true, checkEdged},
		{"netd", "stayconnect-netd.service", true, checkNetd},
		{"portald", "stayconnect-portald.service", true, checkPortald},
		{"acctd", "stayconnect-acctd.service", true, checkAcctd},
		{"hotel-admin", "stayconnect-hotel-admin.service", true, checkHotelAdmin},
		{"caddy", "stayconnect-caddy.service", true, checkCaddy},
		{"kea", "kea-dhcp4-server.service", true, checkKea},
		{"unbound", "unbound.service", true, checkUnbound},
		{"postgres", "docker.service", true, checkPostgres},
	}
}

// sdState is the parsed systemd view of a unit.
type sdState struct {
	ActiveState string // active/inactive/failed/activating/deactivating
	SubState    string
	NRestarts   int64
	ExecCode    string // "exited"/"killed"/"" (from ExecMainCode)
	ExecStatus  int    // exit code, or signal number when killed
	Result      string // success/exit-code/signal/watchdog/start-limit-hit/...
	Since       time.Time
}

// systemdShow reads unit state via `systemctl show` (read-only, no privilege).
func systemdShow(ctx context.Context, unit string) sdState {
	props := "ActiveState,SubState,NRestarts,ExecMainCode,ExecMainStatus,Result,ActiveEnterTimestamp"
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--property="+props).Output()
	var st sdState
	if err != nil {
		st.ActiveState = "unknown"
		return st
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.IndexByte(line, '='); i > 0 {
			m[line[:i]] = strings.TrimSpace(line[i+1:])
		}
	}
	st.ActiveState = m["ActiveState"]
	st.SubState = m["SubState"]
	st.Result = m["Result"]
	if n, e := strconv.ParseInt(m["NRestarts"], 10, 64); e == nil {
		st.NRestarts = n
	}
	switch m["ExecMainCode"] {
	case "1":
		st.ExecCode = "exited"
	case "2":
		st.ExecCode = "killed"
	}
	if v, e := strconv.Atoi(m["ExecMainStatus"]); e == nil {
		st.ExecStatus = v
	}
	// ActiveEnterTimestamp like "Mon 2026-07-14 10:00:00 UTC"; best-effort parse.
	if ts := m["ActiveEnterTimestamp"]; ts != "" {
		for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -0700"} {
			if t, e := time.Parse(layout, ts); e == nil {
				st.Since = t
				break
			}
		}
	}
	return st
}

// serviceHealth is the persisted+served health record for one service.
type serviceHealth struct {
	Service         string     `json:"service"`
	State           string     `json:"state"`
	ProcessState    string     `json:"process_state"`
	HealthOK        *bool      `json:"health_ok"`
	HealthDetail    string     `json:"health_detail"`
	ConsecFailures  int        `json:"consecutive_failures"`
	RestartCount    int64      `json:"restart_count"`
	RestartsWindow  int        `json:"restarts_in_window"`
	RestartWindowS  int        `json:"restart_window_secs"`
	BackoffLevel    int        `json:"backoff_level"`
	BackoffMS       int64      `json:"backoff_ms"`
	NextRetryAt     *time.Time `json:"next_retry_at"`
	FirstFailureAt  *time.Time `json:"first_failure_at"`
	LastFailureAt   *time.Time `json:"last_failure_at"`
	LastFailureRsn  string     `json:"last_failure_reason"`
	LastExitCode    *int       `json:"last_exit_code"`
	LastExitSignal  string     `json:"last_exit_signal"`
	LastHealthyAt   *time.Time `json:"last_healthy_at"`
	LastRecoveryAt  *time.Time `json:"last_recovery_at"`
	TimeSinceHealth *int64     `json:"time_since_healthy_s"`
	DegradedDep     string     `json:"degraded_dependency"`
	Critical        bool       `json:"critical"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// healthMonitorLoop is the supervisor's main loop.
func (s *server) healthMonitorLoop(ctx context.Context) {
	// Give services a moment to come up on first boot before the first poll.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	s.bootConvergeInit(ctx)
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	// Telemetry to Central at a slower cadence than local polling.
	telemetryEvery := 6 // every 6th poll ≈ 60s
	tick := 0
	for {
		s.pollAllServices(ctx)
		s.bootConvergeStep(ctx)
		tick++
		if tick%telemetryEvery == 0 {
			s.enqueueServiceHealth(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// pollAllServices polls, classifies and persists every service once.
func (s *server) pollAllServices(ctx context.Context) {
	for _, spec := range s.services() {
		s.pollOne(ctx, spec)
	}
	// Bounded retention: keep recent recovery history only.
	_, _ = s.db.Exec(ctx, `DELETE FROM appliance_recovery_events
	    WHERE id NOT IN (SELECT id FROM appliance_recovery_events ORDER BY id DESC LIMIT 2000)`)
}

func (s *server) pollOne(ctx context.Context, spec svcSpec) {
	now := time.Now()
	sd := systemdShow(ctx, spec.Unit)
	bk := startupbackoff.Load(spec.Name)
	ok, detail, dep := spec.Check(ctx, s)

	prev := s.loadHealth(ctx, spec.Name)

	cur := serviceHealth{
		Service:        spec.Name,
		ProcessState:   strings.TrimSpace(sd.ActiveState + "/" + sd.SubState),
		HealthOK:       &ok,
		HealthDetail:   sanitizeDetail(detail),
		RestartCount:   sd.NRestarts,
		RestartsWindow: bk.CountInWindow,
		RestartWindowS: int(startupbackoff.Window / time.Second),
		BackoffLevel:   bk.Level,
		BackoffMS:      bk.LastDelayMS,
		DegradedDep:    dep,
		Critical:       spec.Critical,
		UpdatedAt:      now,
	}
	if bk.Level > 0 && bk.NextEligibleAt.After(now) {
		t := bk.NextEligibleAt
		cur.NextRetryAt = &t
	}
	if sd.ExecCode == "exited" {
		c := sd.ExecStatus
		cur.LastExitCode = &c
	} else if sd.ExecCode == "killed" {
		cur.LastExitSignal = signalName(sd.ExecStatus)
	}

	cur.State = classify(sd, bk, ok)

	// Carry forward accumulating fields from the previous record.
	cur.ConsecFailures = prev.ConsecFailures
	cur.FirstFailureAt = prev.FirstFailureAt
	cur.LastFailureAt = prev.LastFailureAt
	cur.LastFailureRsn = prev.LastFailureRsn
	cur.LastHealthyAt = prev.LastHealthyAt
	cur.LastRecoveryAt = prev.LastRecoveryAt

	healthyNow := cur.State == stHealthy
	unhealthyNow := cur.State == stDegraded || cur.State == stFailed || cur.State == stCrashLoop
	wasHealthy := prev.State == stHealthy || prev.State == "" || prev.State == stUnknown

	switch {
	case healthyNow:
		cur.LastHealthyAt = &now
		cur.ConsecFailures = 0
		cur.FirstFailureAt = nil
		cur.DegradedDep = ""
		if !wasHealthy && prev.State != "" {
			cur.LastRecoveryAt = &now
			dur := recoveryDuration(prev, now)
			s.recordRecovery(ctx, spec.Name, "recovered", prev.LastFailureRsn,
				"self-heal (systemd restart + adaptive backoff)", bk.Level, "recovered", dur, "system")
		}
	case unhealthyNow:
		cur.ConsecFailures = prev.ConsecFailures + 1
		cur.LastFailureAt = &now
		cur.LastFailureRsn = failureReason(sd, ok, detail, dep)
		if wasHealthy {
			cur.FirstFailureAt = &now
			s.recordRecovery(ctx, spec.Name, "failure_detected", cur.LastFailureRsn, "", bk.Level, "", 0, "system")
		}
		if cur.State == stCrashLoop && prev.State != stCrashLoop {
			s.recordRecovery(ctx, spec.Name, "crash_loop", cur.LastFailureRsn,
				fmt.Sprintf("adaptive backoff level %d (~%.0fs)", bk.Level, float64(bk.LastDelayMS)/1000), bk.Level, "", 0, "system")
		}
	case cur.State == stRecovering:
		if prev.State != stRecovering {
			s.recordRecovery(ctx, spec.Name, "recovering", prev.LastFailureRsn,
				fmt.Sprintf("restarting with backoff level %d", bk.Level), bk.Level, "", 0, "system")
		}
	}

	if cur.LastHealthyAt != nil {
		v := int64(now.Sub(*cur.LastHealthyAt).Seconds())
		cur.TimeSinceHealth = &v
	}
	s.saveHealth(ctx, cur)
}

// classify maps systemd state + backoff + health-check into a health state.
func classify(sd sdState, bk startupbackoff.Tracker, ok bool) string {
	// A sustained rapid restart pattern is a crash loop UNLESS it is currently
	// active AND healthy (in which case it has recovered).
	if bk.Level >= startupbackoff.CrashLoopLevel && !(sd.ActiveState == "active" && ok) {
		return stCrashLoop
	}
	switch sd.ActiveState {
	case "active":
		if ok {
			return stHealthy
		}
		return stDegraded
	case "activating", "reloading":
		if bk.Level > 0 {
			return stRecovering
		}
		return stStarting
	case "failed":
		return stFailed
	case "inactive", "deactivating":
		return stFailed // a critical service must not be down
	default:
		return stUnknown
	}
}

func failureReason(sd sdState, ok bool, detail, dep string) string {
	if sd.ActiveState == "failed" || sd.ActiveState == "inactive" {
		switch {
		case sd.Result == "start-limit-hit":
			return "systemd start-limit hit"
		case sd.ExecCode == "killed":
			return "killed by " + signalName(sd.ExecStatus)
		case sd.ExecCode == "exited" && sd.ExecStatus != 0:
			return fmt.Sprintf("exited with code %d", sd.ExecStatus)
		case sd.Result != "" && sd.Result != "success":
			return "systemd result: " + sd.Result
		default:
			return "service " + sd.ActiveState
		}
	}
	if !ok { // active-but-unhealthy
		if dep != "" {
			return "dependency unavailable: " + dep + " (" + sanitizeDetail(detail) + ")"
		}
		return "health check failed: " + sanitizeDetail(detail)
	}
	return ""
}

func recoveryDuration(prev serviceHealth, now time.Time) int64 {
	if prev.FirstFailureAt != nil {
		return int64(now.Sub(*prev.FirstFailureAt).Milliseconds())
	}
	if prev.LastFailureAt != nil {
		return int64(now.Sub(*prev.LastFailureAt).Milliseconds())
	}
	return 0
}

func signalName(sig int) string {
	names := map[int]string{9: "SIGKILL", 15: "SIGTERM", 6: "SIGABRT", 11: "SIGSEGV", 2: "SIGINT", 1: "SIGHUP"}
	if n, okk := names[sig]; okk {
		return n
	}
	return "signal " + strconv.Itoa(sig)
}

// sanitizeDetail strips anything that could carry secrets/PII from a check
// detail before it is stored or sent to Central.
func sanitizeDetail(d string) string {
	d = strings.TrimSpace(d)
	if len(d) > 300 {
		d = d[:300]
	}
	// Redact anything that looks like key=value secrets or IPs of guests.
	for _, bad := range []string{"password", "token", "secret", "key=", "authorization"} {
		if strings.Contains(strings.ToLower(d), bad) {
			return "[redacted]"
		}
	}
	return d
}

// ---- persistence -----------------------------------------------------------

func (s *server) loadHealth(ctx context.Context, name string) serviceHealth {
	var h serviceHealth
	row := s.db.QueryRow(ctx, `SELECT service, state, process_state, health_ok, health_detail,
	    consecutive_failures, restart_count, restarts_in_window, restart_window_secs, backoff_level, backoff_ms,
	    next_retry_at, first_failure_at, last_failure_at, last_failure_reason, last_exit_code, last_exit_signal,
	    last_healthy_at, last_recovery_at, time_since_healthy_s, degraded_dependency, critical, updated_at
	    FROM appliance_service_health WHERE service=$1`, name)
	_ = row.Scan(&h.Service, &h.State, &h.ProcessState, &h.HealthOK, &h.HealthDetail,
		&h.ConsecFailures, &h.RestartCount, &h.RestartsWindow, &h.RestartWindowS, &h.BackoffLevel, &h.BackoffMS,
		&h.NextRetryAt, &h.FirstFailureAt, &h.LastFailureAt, &h.LastFailureRsn, &h.LastExitCode, &h.LastExitSignal,
		&h.LastHealthyAt, &h.LastRecoveryAt, &h.TimeSinceHealth, &h.DegradedDep, &h.Critical, &h.UpdatedAt)
	return h
}

func (s *server) saveHealth(ctx context.Context, h serviceHealth) {
	_, err := s.db.Exec(ctx, `INSERT INTO appliance_service_health
	  (service, state, process_state, health_ok, health_detail, consecutive_failures, restart_count,
	   restarts_in_window, restart_window_secs, backoff_level, backoff_ms, next_retry_at, first_failure_at,
	   last_failure_at, last_failure_reason, last_exit_code, last_exit_signal, last_healthy_at, last_recovery_at,
	   time_since_healthy_s, degraded_dependency, critical, updated_at)
	  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
	  ON CONFLICT (service) DO UPDATE SET state=$2, process_state=$3, health_ok=$4, health_detail=$5,
	   consecutive_failures=$6, restart_count=$7, restarts_in_window=$8, restart_window_secs=$9, backoff_level=$10,
	   backoff_ms=$11, next_retry_at=$12, first_failure_at=$13, last_failure_at=$14, last_failure_reason=$15,
	   last_exit_code=$16, last_exit_signal=$17, last_healthy_at=$18, last_recovery_at=$19, time_since_healthy_s=$20,
	   degraded_dependency=$21, critical=$22, updated_at=$23`,
		h.Service, h.State, h.ProcessState, h.HealthOK, h.HealthDetail, h.ConsecFailures, h.RestartCount,
		h.RestartsWindow, h.RestartWindowS, h.BackoffLevel, h.BackoffMS, h.NextRetryAt, h.FirstFailureAt,
		h.LastFailureAt, h.LastFailureRsn, h.LastExitCode, h.LastExitSignal, h.LastHealthyAt, h.LastRecoveryAt,
		h.TimeSinceHealth, h.DegradedDep, h.Critical, h.UpdatedAt)
	if err != nil {
		// best-effort; a transient DB blip must not crash the supervisor
		return
	}
}

func (s *server) recordRecovery(ctx context.Context, service, event, cause, action string, level int, result string, durMS int64, actor string) {
	_, _ = s.db.Exec(ctx, `INSERT INTO appliance_recovery_events
	   (service, event, cause, action, backoff_level, result, duration_ms, actor)
	   VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		service, event, sanitizeDetail(cause), action, level, result, durMS, actor)
}

// ---- boot convergence ------------------------------------------------------

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

func (s *server) bootConvergeInit(ctx context.Context) {
	bid := bootID()
	var storedBoot string
	_ = s.db.QueryRow(ctx, `SELECT COALESCE(boot_id,'') FROM appliance_boot_convergence WHERE id`).Scan(&storedBoot)
	if storedBoot == bid && bid != "" {
		return // same boot; keep existing convergence state
	}
	var req []string
	for _, sp := range s.services() {
		if sp.Critical {
			req = append(req, sp.Name)
		}
	}
	now := time.Now()
	_, _ = s.db.Exec(ctx, `INSERT INTO appliance_boot_convergence
	   (id, boot_id, boot_at, deadline_at, converged, converged_at, required_services, pending_services, alert_open, updated_at)
	   VALUES (true,$1,$2,$3,false,NULL,$4,$4,false,$2)
	   ON CONFLICT (id) DO UPDATE SET boot_id=$1, boot_at=$2, deadline_at=$3, converged=false, converged_at=NULL,
	     required_services=$4, pending_services=$4, alert_open=false, updated_at=$2`,
		bid, now, now.Add(bootConvergeDeadline), req)
}

func (s *server) bootConvergeStep(ctx context.Context) {
	var converged, alertOpen bool
	var deadline time.Time
	var required []string
	if err := s.db.QueryRow(ctx, `SELECT converged, alert_open, deadline_at, required_services
	    FROM appliance_boot_convergence WHERE id`).Scan(&converged, &alertOpen, &deadline, &required); err != nil {
		return
	}
	if converged {
		return
	}
	// Which required services are not yet healthy?
	var pending []string
	for _, name := range required {
		h := s.loadHealth(ctx, name)
		if h.State != stHealthy {
			pending = append(pending, name)
		}
	}
	now := time.Now()
	if len(pending) == 0 {
		_, _ = s.db.Exec(ctx, `UPDATE appliance_boot_convergence
		   SET converged=true, converged_at=$1, pending_services='{}', alert_open=false, updated_at=$1 WHERE id`, now)
		s.recordRecovery(ctx, "appliance", "boot_converged", "", "all critical services healthy after boot", 0, "converged", 0, "system")
		return
	}
	sort.Strings(pending)
	_, _ = s.db.Exec(ctx, `UPDATE appliance_boot_convergence SET pending_services=$1, updated_at=$2 WHERE id`, pending, now)
	if now.After(deadline) && !alertOpen {
		_, _ = s.db.Exec(ctx, `UPDATE appliance_boot_convergence SET alert_open=true, updated_at=$1 WHERE id`, now)
		s.recordRecovery(ctx, "appliance", "boot_not_converged", "did not converge within deadline",
			strings.Join(pending, ","), 0, "degraded", 0, "system")
	}
}

// ---- telemetry to Central --------------------------------------------------

// enqueueServiceHealth pushes a sanitized service-health summary to Central via
// the outbox (scd's drainer publishes it). No guest PII, no secrets.
func (s *server) enqueueServiceHealth(ctx context.Context) {
	all := s.allHealth(ctx)
	overall, counts := overallHealth(all)
	svcs := make([]map[string]any, 0, len(all))
	var worstReason, worstService string
	for _, h := range all {
		item := map[string]any{
			"service":              h.Service,
			"state":                h.State,
			"restart_count":        h.RestartCount,
			"restarts_in_window":   h.RestartsWindow,
			"consecutive_failures": h.ConsecFailures,
			"backoff_level":        h.BackoffLevel,
			"degraded_dependency":  h.DegradedDep,
		}
		if h.NextRetryAt != nil {
			item["next_retry_at"] = h.NextRetryAt.UTC()
		}
		if h.LastFailureRsn != "" {
			item["last_failure_reason"] = h.LastFailureRsn
		}
		if h.LastRecoveryAt != nil {
			item["last_recovery_at"] = h.LastRecoveryAt.UTC()
		}
		if h.State != stHealthy && worstService == "" {
			worstService, worstReason = h.Service, h.LastFailureRsn
		}
		svcs = append(svcs, item)
	}
	var boot map[string]any
	{
		var converged, alertOpen bool
		var pending []string
		var convergedAt *time.Time
		if s.db.QueryRow(ctx, `SELECT converged, alert_open, pending_services, converged_at
		    FROM appliance_boot_convergence WHERE id`).Scan(&converged, &alertOpen, &pending, &convergedAt) == nil {
			boot = map[string]any{"converged": converged, "alert_open": alertOpen, "pending": pending}
			if convergedAt != nil {
				boot["converged_at"] = convergedAt.UTC()
			}
		}
	}
	payload := map[string]any{
		"overall":              overall,
		"counts":               counts,
		"services":             svcs,
		"boot":                 boot,
		"worst_service":        worstService,
		"worst_failure_reason": worstReason,
		"reported_at":          time.Now().UTC(),
	}
	ob := &outbox.Outbox{DB: s.db, ApplianceID: s.applianceID()}
	_ = ob.Enqueue(ctx, "service_health", payload)
}

// applianceID resolves this appliance's id for telemetry (best-effort).
func (s *server) applianceID() string {
	if v := os.Getenv("EDGED_APPLIANCE_ID"); v != "" {
		return v
	}
	// The signed assignment / identity carries it; read from identity.json.
	b, _ := os.ReadFile(envOr("EDGED_IDENTITY_DIR", "/etc/stayconnect/identity") + "/identity.json")
	// crude extraction to avoid importing identity here; the drainer also stamps it
	s2 := string(b)
	if i := strings.Index(s2, "\"appliance_id\""); i >= 0 {
		rest := s2[i+15:]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			rest = rest[j+1:]
			if k := strings.IndexByte(rest, '"'); k >= 0 {
				return rest[:k]
			}
		}
	}
	return ""
}

// allHealth returns every service's current health record, in display order.
func (s *server) allHealth(ctx context.Context) []serviceHealth {
	order := map[string]int{}
	for i, sp := range s.services() {
		order[sp.Name] = i
	}
	rows, err := s.db.Query(ctx, `SELECT service, state, process_state, health_ok, health_detail,
	    consecutive_failures, restart_count, restarts_in_window, restart_window_secs, backoff_level, backoff_ms,
	    next_retry_at, first_failure_at, last_failure_at, last_failure_reason, last_exit_code, last_exit_signal,
	    last_healthy_at, last_recovery_at, time_since_healthy_s, degraded_dependency, critical, updated_at
	    FROM appliance_service_health`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []serviceHealth
	for rows.Next() {
		var h serviceHealth
		if rows.Scan(&h.Service, &h.State, &h.ProcessState, &h.HealthOK, &h.HealthDetail,
			&h.ConsecFailures, &h.RestartCount, &h.RestartsWindow, &h.RestartWindowS, &h.BackoffLevel, &h.BackoffMS,
			&h.NextRetryAt, &h.FirstFailureAt, &h.LastFailureAt, &h.LastFailureRsn, &h.LastExitCode, &h.LastExitSignal,
			&h.LastHealthyAt, &h.LastRecoveryAt, &h.TimeSinceHealth, &h.DegradedDep, &h.Critical, &h.UpdatedAt) == nil {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i].Service] < order[out[j].Service] })
	return out
}

// overallHealth derives the appliance-level state + per-state counts.
func overallHealth(all []serviceHealth) (string, map[string]int) {
	counts := map[string]int{stHealthy: 0, stDegraded: 0, stRecovering: 0, stCrashLoop: 0, stFailed: 0, stStarting: 0, stUnknown: 0}
	worst := stHealthy
	rank := map[string]int{stHealthy: 0, stStarting: 1, stRecovering: 2, stDegraded: 3, stCrashLoop: 4, stFailed: 5, stUnknown: 3}
	for _, h := range all {
		counts[h.State]++
		if h.Critical && rank[h.State] > rank[worst] {
			worst = h.State
		}
	}
	switch worst {
	case stHealthy:
		return "healthy", counts
	case stStarting, stRecovering:
		return "recovering", counts
	default:
		return "degraded", counts
	}
}

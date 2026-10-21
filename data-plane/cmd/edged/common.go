package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ----- responses ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]string{"error": errCode, "message": msg})
}

type listMeta struct {
	HasMore bool `json:"has_more"`
}

func writeList[T any](w http.ResponseWriter, rows []T) {
	if rows == nil {
		rows = []T{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows, "meta": listMeta{}})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	// UseNumber, or every free-form map[string]any body silently loses integers.
	//
	// Grant tiers, duration policies and eligibility rules are carried as
	// map[string]any. Without this, encoding/json turns their numbers into
	// float64, and the domain validators accept only json.Number/int/int64 --
	// deliberately, because they reject anything with a "." or an exponent so a
	// bandwidth cap can never arrive as 2047.9999999. The two halves disagreed,
	// so EVERY numeric override sent over HTTP was rejected as
	// "invalid_grant_tier": publishing a package with a bandwidth or quota
	// override was impossible through the API.
	//
	// It stayed hidden because the unit tests build json.Number values in Go and
	// call the admin layer directly, never crossing this decoder -- so the tests
	// exercised a type the HTTP surface could not actually produce.
	dec.UseNumber()
	return dec.Decode(dst)
}

func dbCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// ----- local audit ---------------------------------------------------------------

// audit appends to the site-local audit_log. Best-effort: never fails the
// calling request.
func (s *server) audit(r *http.Request, action, targetType, targetID string, payload map[string]any) {
	if err := s.auditErr(r, action, targetType, targetID, payload); err != nil {
		slog.Warn("audit write failed", "action", action, "err", err)
	}
}

// auditErr is audit for an action that must not happen unrecorded: the caller refuses when it fails.
func (s *server) auditErr(r *http.Request, action, targetType, targetID string, payload map[string]any) error {
	sess := sessFrom(r.Context())
	actorID := ""
	if sess != nil {
		actorID = sess.OperatorID
	}
	raw, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// tenant_id may be empty before enrollment (factory-clean box). It is a
	// nullable uuid, so map "" -> NULL; otherwise the insert fails and pre-
	// enrollment attempts (e.g. setup.enroll_*) would go unaudited.
	_, err := s.db.Exec(ctx, `
        INSERT INTO audit_log (tenant_id, actor_type, actor_id, action, target_type, target_id, ip, user_agent, payload)
        VALUES (NULLIF($1,'')::uuid, 'operator', NULLIF($2,''), $3, NULLIF($4,''), NULLIF($5,''),
                CASE WHEN $6 = '' THEN NULL ELSE $6::inet END, NULLIF($7,''), $8::jsonb)
    `, s.tenantID, actorID, action, targetType, targetID, clientIP(r), r.UserAgent(), string(raw))
	return err
}

func clientIP(r *http.Request) string {
	// edged sits behind Caddy on loopback, so prefer the forwarded client IP. X-Real-IP first: the Admin Console
	// vhost SETS it from the connection Caddy accepted (header_up X-Real-IP {remote_host}), so a browser cannot
	// choose it -- and the sign-in throttle counts per source address with this value (operator_signin.go).
	if rip := strings.TrimSpace(r.Header.Get("X-Real-IP")); rip != "" {
		return rip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ----- scd unix-socket client ------------------------------------------------------

// scdClient talks to scd's admin surface over its unix socket — the same
// channel portald uses. edged never touches nftables or tc directly.
type scdClient struct {
	http *http.Client
}

func newSCDClient(socket string) *scdClient {
	return &scdClient{http: &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}}
}

// call proxies a request to scd and returns (status, body).
// longSCDPaths are the privileged operations that legitimately take MINUTES rather than milliseconds: a
// database dump and its verification. The shared 15s client deadline is right for every other call and was
// killing these mid-flight -- the appliance reported "signal: killed" as though the dump were corrupt.
var longSCDPaths = map[string]time.Duration{
	"/v1/backup/run":    20 * time.Minute,
	"/v1/backup/verify": 25 * time.Minute,
	// The status re-reads every stored backup against its checksum, and removing earlier backups does the same first.
	"/v1/backup/status":         3 * time.Minute,
	"/v1/backup/earlier/remove": 5 * time.Minute,
	// The erasure ledger: a commit writes two copies and reads both back; recovery and a first copy can take longer.
	"/v1/ledger/prepare":           60 * time.Second,
	"/v1/ledger/commit":            2 * time.Minute,
	"/v1/ledger/abort":             90 * time.Second,
	"/v1/ledger/status":            40 * time.Second,
	"/v1/ledger/destination":       4 * time.Minute,
	"/v1/ledger/destination/probe": 45 * time.Second,
	"/v1/ledger/recover":           6 * time.Minute,
}

func (c *scdClient) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if d, ok := longSCDPaths[strings.SplitN(path, "?", 2)[0]]; ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), d)
		defer cancel()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, raw, err
}

// proxy relays scd's response verbatim.
func (c *scdClient) proxy(w http.ResponseWriter, r *http.Request, method, path string, body any) {
	st, raw, err := c.call(r.Context(), method, path, body)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

// ----- license endpoints (proxied to scd, which owns the license store) -----------

func (s *server) licenseInstall(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "empty body")
		return
	}
	st, resp, err := s.scd.call(r.Context(), http.MethodPost, "/v1/license/install", json.RawMessage(raw))
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st == http.StatusOK {
		s.audit(r, "license.installed", "license", "", nil)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// ----- health -----------------------------------------------------------------------

// health is the unauthenticated liveness answer, and the dashboard's summary for a signed-in operator.
//
// WHO SEES WHAT (audit C-21). Anyone who can reach the management address gets liveness only -- service, status,
// db, scd -- which is what an uptime check needs. The site id, the version and the licence state go only to a
// signed-in operator (the dashboard and the top-bar status) and to a caller on the appliance itself that
// connects to edged directly (the acceptance and reboot scripts curl 127.0.0.1:8090): through Caddy and the
// console every request carries X-Forwarded-For / X-Real-IP, so "loopback with no forwarding header" means a
// process on this machine, which can read all of this anyway.
func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	full := s.healthCallerMaySeeDetail(r)
	out := map[string]any{"service": "edged"}
	if full {
		out["version"] = version
		out["site_id"] = s.siteID
	}
	dbOK := s.db.Ping(ctx) == nil
	out["db"] = dbOK

	scdOK := false
	// The licence summary comes from the SAME scd computation as /central/status (section 8 vocabulary:
	// none|active|expiring|grace|expired|suspended|revoked|wrong_hardware), so the dashboard and the Appliance
	// & licence page can never disagree about it.
	var licState, activation any
	if sum, ok := s.readLicenceSummary(ctx); ok {
		scdOK = true
		licState, activation = sum.State, sum.Activation
	} else if st2, _, err2 := s.scd.call(ctx, http.MethodGet, "/v1/health", nil); err2 == nil && st2 == 200 {
		scdOK = true
	}
	out["scd"] = scdOK
	if full {
		out["license_state"] = licState
		out["license_installed"] = licState != nil && licState != "none"
		out["activation"] = activation
	}

	code := http.StatusOK
	if !dbOK {
		code = http.StatusServiceUnavailable
	}
	out["status"] = map[bool]string{true: "ok", false: "degraded"}[dbOK && scdOK]
	writeJSON(w, code, out)
}

func (s *server) healthCallerMaySeeDetail(r *http.Request) bool {
	if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" && s.sessions != nil && s.sessions.peek(ck.Value) != nil {
		return true
	}
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "X-Forwarded-Host", "True-Client-IP", "Forwarded"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ----- seed-admin -------------------------------------------------------------------

// runSeedAdmin bootstraps (or resets) the site_admin account directly in the
// site DB. Used during hotel onboarding before any operator exists.
func runSeedAdmin(args []string) error {
	fs := flag.NewFlagSet("seed-admin", flag.ExitOnError)
	email := fs.String("email", "", "site admin email / username")
	password := fs.String("password", "", "password (min 10 chars unless --allow-weak)")
	name := fs.String("name", "Site Admin", "display name")
	allowWeak := fs.Bool("allow-weak", false, "permit a password shorter than 10 chars — deliberate per-appliance provisioning only, NOT for production; the operator must change it at first sign-in")
	_ = fs.Parse(args)
	if *email == "" || *password == "" {
		return fmt.Errorf("--email and --password are required")
	}
	if len(*password) < 10 && !*allowWeak {
		return fmt.Errorf("--password must be >= 10 chars (pass --allow-weak to override; not for production)")
	}
	// A weak seeded password is usable exactly once: the first sign-in can only replace it (0119).
	mustChange := *allowWeak || len(*password) < 10
	if len(*password) < 10 {
		slog.Warn("seeding a WEAK site-admin password via --allow-weak — it must be changed at first sign-in")
	}
	c := loadCfg()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, c.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	hash, err := hashPassword(*password)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `
        INSERT INTO operators (email, display_name, password_hash, status, must_change_password)
        VALUES ($1, $2, $3, 'active', $4)
        ON CONFLICT (email) DO UPDATE
          SET password_hash = EXCLUDED.password_hash, display_name = EXCLUDED.display_name,
              status = 'active', must_change_password = EXCLUDED.must_change_password, updated_at = now()
        RETURNING id
    `, *email, *name, hash, mustChange).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO operator_roles (operator_id, tenant_id, role)
        SELECT $1, NULL, 'site_admin'
         WHERE NOT EXISTS (SELECT 1 FROM operator_roles WHERE operator_id = $1 AND role = 'site_admin')
    `, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// Break-glass recovery: a reset password must be usable now, so a running sign-in lockout on this account ends
	// with it (the same as an administrator's reset). Best-effort -- before migration 0119 there is no throttle.
	if _, err := pool.Exec(ctx, `SELECT public.operator_signin_throttle_clear('account', $1)`,
		throttleKey(scopeAccount, *email)); err != nil {
		slog.Warn("could not clear a sign-in lockout for the seeded account", "err", err)
	}
	slog.Info("seeded site admin", "id", id, "must_change_password", mustChange) // the id, not the email: the journal is outside retention
	return nil
}

// writeAwaitingAssignment answers a tenant- or site-scoped surface on an appliance that has not yet received
// its signed assignment.
//
// It returns 200 with an EMPTY result and an explicit reason, not an error. The distinction matters to the
// operator: a factory-clean appliance before enrollment/claim/assignment is working exactly as designed, and
// the previous behaviour -- a uuid comparison against ” failing, surfaced as "the appliance did not answer"
// -- described a broken machine. The screens read `data` and render an empty state; `awaiting_assignment`
// lets them say WHY it is empty instead of implying a fault.
func writeAwaitingAssignment(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data":                []any{},
		"meta":                map[string]any{"has_more": false},
		"awaiting_assignment": true,
		"reason": "This appliance has not been assigned to a site yet, so there is no " + what +
			" to show. Complete enrollment, claim and signed assignment first.",
	})
}

// ----- reset-mfa ----------------------------------------------------------------------------------------------

// runResetMFA removes one operator's authenticator from the appliance's own command line (0132, GDPR plan C10):
// the break-glass for the last site administrator, when nobody is left to reset them in the console. Whoever can
// run it is root on the appliance already. The operator sets up a new authenticator at their next sign-in; a
// running edged ends their sessions at its next restart, or an administrator ends them by any password reset.
func runResetMFA(args []string) error {
	fs := flag.NewFlagSet("reset-mfa", flag.ExitOnError)
	email := fs.String("email", "", "the operator's email / username")
	reason := fs.String("reason", "", "why (4-500 characters; recorded in the audit log)")
	_ = fs.Parse(args)
	if strings.TrimSpace(*email) == "" || !validReason(strings.TrimSpace(*reason)) {
		return fmt.Errorf("--email and --reason (4-500 characters) are required")
	}
	c := loadCfg()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, c.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	var id string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM operators WHERE lower(email) = lower($1)`, strings.TrimSpace(*email)).Scan(&id); err != nil {
		return fmt.Errorf("no operator %q: %w", *email, err)
	}
	var removed bool
	if err := pool.QueryRow(ctx, `SELECT public.operator_mfa_remove($1)`, id).Scan(&removed); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `SELECT public.operator_signin_throttle_clear('mfa', $1)`, throttleKey(scopeMFA, id)); err != nil {
		slog.Warn("could not clear the code counter", "err", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor_type, actor_id, action, target_type, target_id, payload)
		VALUES (NULLIF($1,'')::uuid, 'system', 'edged reset-mfa', 'operator.mfa_reset', 'operator', $2,
		        jsonb_build_object('reason', $3::text, 'had_authenticator', $4::boolean, 'via', 'command line'))`,
		c.TenantID, id, strings.TrimSpace(*reason), removed); err != nil {
		slog.Warn("reset-mfa: the audit entry could not be written", "err", err)
	}
	slog.Info("two-step sign-in reset; a new authenticator is set up at the next sign-in", "id", id, "had_authenticator", removed)
	return nil
}

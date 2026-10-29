package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
	// UseNumber is required: grant tiers, duration policies and eligibility rules are map[string]any, and the
	// domain validators accept only json.Number/int/int64 (rejecting "." or exponents so a bandwidth cap can
	// never be 2047.9999999). With float64 every numeric override was rejected as "invalid_grant_tier".
	dec.UseNumber()
	return dec.Decode(dst)
}

func dbCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// audit appends to the site-local audit_log; best-effort, it never fails the request.
func (s *server) audit(r *http.Request, action, targetType, targetID string, payload map[string]any) {
	if err := s.auditErr(r, action, targetType, targetID, payload); err != nil {
		slog.Warn("audit write failed", "action", action, "err", err)
	}
}

// auditErr is audit for an action that must not happen unrecorded: the caller refuses when it fails.
func (s *server) auditErr(r *http.Request, action, targetType, targetID string, payload map[string]any) error {
	if s.db == nil { // a handler unit test without a database; production always has a pool
		return errors.New("no database")
	}
	sess := sessFrom(r.Context())
	actorID := ""
	if sess != nil {
		actorID = sess.OperatorID
	}
	raw, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// tenant_id is empty before enrollment; map "" to NULL or pre-enrollment attempts would go unaudited.
	_, err := s.db.Exec(ctx, `
        INSERT INTO audit_log (tenant_id, actor_type, actor_id, action, target_type, target_id, ip, user_agent, payload)
        VALUES (NULLIF($1,'')::uuid, 'operator', NULLIF($2,''), $3, NULLIF($4,''), NULLIF($5,''),
                CASE WHEN $6 = '' THEN NULL ELSE $6::inet END, NULLIF($7,''), $8::jsonb)
    `, s.tenantID, actorID, action, targetType, targetID, clientIP(r), r.UserAgent(), string(raw))
	return err
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Forwarding headers are believed only from a loopback peer, the appliance's own proxy chain (audit P9 L5);
	// any other peer is the client itself.
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return host
	}
	// edged sits behind Caddy. X-Real-IP comes first: the Admin Console vhost sets it from the accepted connection,
	// so a browser cannot choose it, and the sign-in throttle counts per source address with it.
	if rip := strings.TrimSpace(r.Header.Get("X-Real-IP")); rip != "" {
		return rip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	return host
}

// scdClient talks to scd's admin surface over its unix socket; edged never touches nftables or tc directly.
type scdClient struct {
	http *http.Client
}

func newSCDClient(socket string) *scdClient {
	// No http.Client.Timeout: it caps the whole call and would turn every long path below into a 15 s one. The
	// deadline is applied per call in call().
	return &scdClient{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}}
}

// scdDefaultTimeout applies to every scd call not in longSCDPaths.
var scdDefaultTimeout = 15 * time.Second

// longSCDPaths take minutes (a dump and its verification); the shared 15 s deadline killed them mid-flight and
// the appliance reported "signal: killed" as though the dump were corrupt.
var longSCDPaths = map[string]time.Duration{
	"/v1/backup/run":    20 * time.Minute,
	"/v1/backup/verify": 25 * time.Minute,
	// Status and earlier-backup removal re-read every stored backup against its checksum.
	"/v1/backup/status":         3 * time.Minute,
	"/v1/backup/earlier/remove": 5 * time.Minute,
	// A ledger commit writes two copies and reads both back; recovery and a first copy take longer.
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
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, scdDefaultTimeout)
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
		internalErr(w, r, http.StatusBadGateway, "scd_unreachable", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

func (s *server) licenseInstall(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "empty body")
		return
	}
	st, resp, err := s.scd.call(r.Context(), http.MethodPost, "/v1/license/install", json.RawMessage(raw))
	if err != nil {
		internalErr(w, r, http.StatusBadGateway, "scd_unreachable", err)
		return
	}
	if st == http.StatusOK {
		s.audit(r, "license.installed", "license", "", nil)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// licenseClockReset is the audited operator action "the clock was wrong" (AD-17): scd reads Central's authenticated
// time and resets the licence clock high-water mark to it. A reason is required and recorded with the old and new mark.
func (s *server) licenseClockReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || strings.TrimSpace(in.Reason) == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "A reason is required.")
		return
	}
	st, resp, err := s.scd.call(r.Context(), http.MethodPost, "/v1/license/clock-reset", map[string]any{})
	if err != nil {
		s.audit(r, "license.clock_reset_failed", "license", "", map[string]any{"reason": in.Reason, "error": err.Error()})
		internalErr(w, r, http.StatusBadGateway, "scd_unreachable", err)
		return
	}
	detail := map[string]any{"reason": in.Reason, "status": st}
	var out map[string]any
	if json.Unmarshal(resp, &out) == nil {
		detail["previous_high_water"], detail["high_water"] = out["previous_high_water"], out["high_water"]
	}
	if st == http.StatusOK {
		s.audit(r, "license.clock_reset", "license", "", detail)
	} else {
		s.audit(r, "license.clock_reset_refused", "license", "", detail)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// health is the unauthenticated liveness answer. Anyone reaching the management address gets liveness only; site
// id, version and licence state go only to a signed-in operator or a process on the appliance itself. Through
// Caddy every request carries X-Forwarded-For / X-Real-IP, so loopback with no forwarding header means a local
// process, which can read all of this anyway.
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
	// Same scd computation as /central/status, so the dashboard and the licence page cannot disagree.
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

// runSeedAdmin bootstraps (or resets) the site_admin account directly in the site DB during onboarding.
func runSeedAdmin(args []string) error {
	fs := flag.NewFlagSet("seed-admin", flag.ExitOnError)
	email := fs.String("email", "", "site admin email / username")
	password := fs.String("password", "", "password (min 10 chars unless --allow-weak)")
	name := fs.String("name", "Site Admin", "display name")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from the first line of standard input instead of --password, so it never appears in the process list or shell history")
	allowWeak := fs.Bool("allow-weak", false, "permit a password shorter than 10 chars — deliberate per-appliance provisioning only, NOT for production; the operator must change it at first sign-in")
	_ = fs.Parse(args)
	pw, err := seedAdminPassword(*password, *passwordStdin, os.Stdin)
	if err != nil {
		return err
	}
	*password = pw
	if *email == "" || *password == "" {
		return fmt.Errorf("--email and a password (--password or --password-stdin) are required")
	}
	if len(*password) < 10 && !*allowWeak {
		return fmt.Errorf("--password must be >= 10 chars (pass --allow-weak to override; not for production)")
	}
	// A weak seeded password is usable only to replace it at first sign-in.
	mustChange := *allowWeak || len(*password) < 10
	if len(*password) < 10 {
		slog.Warn("seeding a WEAK site-admin password via --allow-weak — it must be changed at first sign-in")
	}
	c := loadCfg()
	if err := requireDBURL(c); err != nil {
		return err
	}
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
	// Break-glass: a reset password must work now, so any sign-in lockout on the account ends with it. Best-effort.
	if _, err := pool.Exec(ctx, `SELECT public.operator_signin_throttle_clear('account', $1)`,
		throttleKey(scopeAccount, *email)); err != nil {
		slog.Warn("could not clear a sign-in lockout for the seeded account", "err", err)
	}
	slog.Info("seeded site admin", "id", id, "must_change_password", mustChange) // the id, not the email: the journal is outside retention
	return nil
}

// seedAdminPassword picks the seed-admin password from exactly one source. --password-stdin exists for the
// installer: a password given as an argument is visible to every local user in the process list while the
// command runs, and lands in shell history. Only the first line is read and only its line ending is removed,
// so a password may contain spaces.
func seedAdminPassword(flagValue string, fromStdin bool, stdin io.Reader) (string, error) {
	if !fromStdin {
		return flagValue, nil
	}
	if flagValue != "" {
		return "", fmt.Errorf("--password and --password-stdin are mutually exclusive")
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("reading the password from standard input: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", fmt.Errorf("--password-stdin: standard input carried no password")
	}
	return line, nil
}

// writeAwaitingAssignment returns 200 with an empty result and a reason, not an error: an appliance awaiting
// assignment is working as designed, and the screens use `awaiting_assignment` to say why they are empty.
func writeAwaitingAssignment(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data":                []any{},
		"meta":                map[string]any{"has_more": false},
		"awaiting_assignment": true,
		"reason": "This appliance has not been assigned to a site yet, so there is no " + what +
			" to show. Complete enrollment, claim and signed assignment first.",
	})
}

// runResetMFA removes one operator's authenticator from the command line: the break-glass for the last site
// administrator. Whoever can run it is already root. The operator then signs in with the password alone.
func runResetMFA(args []string) error {
	fs := flag.NewFlagSet("reset-mfa", flag.ExitOnError)
	email := fs.String("email", "", "the operator's email / username")
	reason := fs.String("reason", "", "why (4-500 characters; recorded in the audit log)")
	_ = fs.Parse(args)
	if strings.TrimSpace(*email) == "" || !validReason(strings.TrimSpace(*reason)) {
		return fmt.Errorf("--email and --reason (4-500 characters) are required")
	}
	c := loadCfg()
	if err := requireDBURL(c); err != nil {
		return err
	}
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
	slog.Info("two-step sign-in reset; the operator signs in with the password alone until they turn it on again", "id", id, "had_authenticator", removed)
	return nil
}

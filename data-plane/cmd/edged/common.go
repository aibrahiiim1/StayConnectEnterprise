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
	return dec.Decode(dst)
}

func dbCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// ----- local audit ---------------------------------------------------------------

// audit appends to the site-local audit_log. Best-effort: never fails the
// calling request.
func (s *server) audit(r *http.Request, action, targetType, targetID string, payload map[string]any) {
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
	if err != nil {
		slog.Warn("audit write failed", "action", action, "err", err)
	}
}

func clientIP(r *http.Request) string {
	// edged sits behind Caddy on loopback, so prefer the forwarded client IP.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if rip := r.Header.Get("X-Real-IP"); rip != "" {
		return rip
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
func (c *scdClient) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
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

func (s *server) licenseStatus(w http.ResponseWriter, r *http.Request) {
	s.scd.proxy(w, r, http.MethodGet, "/v1/license/status", nil)
}

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

func (s *server) licenseRefresh(w http.ResponseWriter, r *http.Request) {
	s.scd.proxy(w, r, http.MethodPost, "/v1/license/refresh", nil)
}

// ----- health -----------------------------------------------------------------------

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	out := map[string]any{"service": "edged", "version": version, "site_id": s.siteID}
	dbOK := s.db.Ping(ctx) == nil
	out["db"] = dbOK

	scdOK := false
	var licState any
	// licenseInstalled is true ONLY for a real signed license (installed with a
	// non-empty license_id). The permissive "unlicensed-dev" licstate reports
	// state="Active" with no license_id and must NOT read as licensed/activated —
	// otherwise the dashboard shows a green "Active" on a factory-clean box while
	// the License page correctly says "Pending activation".
	licenseInstalled := false
	if st, raw, err := s.scd.call(ctx, http.MethodGet, "/v1/license/status", nil); err == nil && st == 200 {
		scdOK = true
		var lic map[string]any
		if json.Unmarshal(raw, &lic) == nil {
			licState = lic["state"]
			installed, _ := lic["installed"].(bool)
			licID, _ := lic["license_id"].(string)
			licenseInstalled = installed && licID != ""
		}
	} else if st2, _, err2 := s.scd.call(ctx, http.MethodGet, "/v1/health", nil); err2 == nil && st2 == 200 {
		scdOK = true
	}
	out["scd"] = scdOK
	out["license_state"] = licState
	out["license_installed"] = licenseInstalled

	if st, raw, err := s.scd.call(ctx, http.MethodGet, "/v1/admin/outbox/stats", nil); err == nil && st == 200 {
		var ob map[string]any
		if json.Unmarshal(raw, &ob) == nil {
			out["sync_outbox"] = ob
		}
	}
	code := http.StatusOK
	if !dbOK {
		code = http.StatusServiceUnavailable
	}
	out["status"] = map[bool]string{true: "ok", false: "degraded"}[dbOK && scdOK]
	writeJSON(w, code, out)
}

// ----- seed-admin -------------------------------------------------------------------

// runSeedAdmin bootstraps (or resets) the site_admin account directly in the
// site DB. Used during hotel onboarding before any operator exists.
func runSeedAdmin(args []string) error {
	fs := flag.NewFlagSet("seed-admin", flag.ExitOnError)
	email := fs.String("email", "", "site admin email / username")
	password := fs.String("password", "", "password (min 10 chars unless --allow-weak)")
	name := fs.String("name", "Site Admin", "display name")
	allowWeak := fs.Bool("allow-weak", false, "permit a password shorter than 10 chars — deliberate per-appliance provisioning only, NOT for production")
	_ = fs.Parse(args)
	if *email == "" || *password == "" {
		return fmt.Errorf("--email and --password are required")
	}
	if len(*password) < 10 && !*allowWeak {
		return fmt.Errorf("--password must be >= 10 chars (pass --allow-weak to override; not for production)")
	}
	if len(*password) < 10 {
		slog.Warn("seeding a WEAK site-admin password via --allow-weak — management-network use only", "email", *email)
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
        INSERT INTO operators (email, display_name, password_hash, status)
        VALUES ($1, $2, $3, 'active')
        ON CONFLICT (email) DO UPDATE
          SET password_hash = EXCLUDED.password_hash, display_name = EXCLUDED.display_name,
              status = 'active', updated_at = now()
        RETURNING id
    `, *email, *name, hash).Scan(&id); err != nil {
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
	slog.Info("seeded site admin", "email", *email, "id", id)
	return nil
}

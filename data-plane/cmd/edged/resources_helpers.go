package main

// Shared helpers for the edged resource handlers: license-derived limits,
// provisioning gate, scd reload fan-out and small SQL error classifiers.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// parseLimit reads ?limit= with a default and a hard cap.
func parseLimit(r *http.Request, def, max int) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// intLimit reads an integer limit from tenant_effective_limits. The second
// return is false when the key is absent (absent = no limit enforced).
func (s *server) intLimit(ctx context.Context, key string) (int64, bool, error) {
	var v *int64
	err := s.db.QueryRow(ctx, `
        SELECT int_value FROM tenant_effective_limits
         WHERE tenant_id = $1 AND key = $2 AND value_type = 'int'
    `, s.tenantID, key).Scan(&v)
	if isNoRows(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

// enforceLimit checks a license limit before creating `delta` new rows.
// Missing key, -1 and 0 all mean "unlimited". Writes 403 limit_exceeded and
// returns false when the limit would be breached.
func (s *server) enforceLimit(ctx context.Context, w http.ResponseWriter, key string, delta int64, countQuery string, countArgs ...any) bool {
	lim, ok, err := s.intLimit(ctx, key)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "limits lookup failed")
		return false
	}
	if !ok || lim == -1 || lim == 0 {
		return true
	}
	var count int64
	if err := s.db.QueryRow(ctx, countQuery, countArgs...).Scan(&count); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "limits count failed")
		return false
	}
	if count+delta > lim {
		jsonErr(w, http.StatusForbidden, "limit_exceeded", "license limit reached for "+key)
		return false
	}
	return true
}

// licenseAllowsProvisioning asks scd for the license state. Only a clearly
// restrictive state blocks provisioning; scd being unreachable or a missing
// state field is treated as ALLOW (the appliance must stay operable during
// local hiccups — enforcement of a truly bad license happens in scd itself).
func (s *server) licenseAllowsProvisioning(ctx context.Context) bool {
	st, raw, err := s.scd.call(ctx, http.MethodGet, "/v1/license/status", nil)
	if err != nil || st != http.StatusOK {
		return true
	}
	var lic struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &lic) != nil || lic.State == "" {
		return true
	}
	switch lic.State {
	case "Restricted", "Expired", "Suspended", "Revoked":
		return false
	}
	return true
}

// requireProvisioning writes 403 license_restricted and returns false when
// the license state forbids creating new guest-facing resources.
func (s *server) requireProvisioning(w http.ResponseWriter, r *http.Request) bool {
	if s.licenseAllowsProvisioning(r.Context()) {
		return true
	}
	jsonErr(w, http.StatusForbidden, "license_restricted", "license state forbids provisioning new resources")
	return false
}

// scdReloadWarn pokes an scd reload endpoint after a config mutation.
// Best-effort: failures are logged, never surfaced to the caller — scd will
// pick the change up from the DB on its next restart regardless.
func (s *server) scdReloadWarn(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, _, err := s.scd.call(ctx, http.MethodPost, path, nil)
	if err != nil || st >= 300 {
		slog.Warn("scd reload failed", "path", path, "status", st, "err", err)
	}
}

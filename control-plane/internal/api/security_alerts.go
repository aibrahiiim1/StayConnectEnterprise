package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// alertRepeatWindow is how long an open alert absorbs repeats of the same event. Registration is unauthenticated,
// so without it anyone reaching Central could bury real alerts under new rows.
const alertRepeatWindow = 24 * time.Hour

type alertDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// raiseAlertOnce records a security alert for (applianceID, kind), folding repeats within alertRepeatWindow into the
// open one. It reports whether a new alert was raised, which is also when the caller audits.
func raiseAlertOnce(ctx context.Context, db alertDB, applianceID, serial, kind string, detail map[string]any, ip string) (bool, error) {
	raw, _ := json.Marshal(detail)
	var id string
	err := db.QueryRow(ctx, `
        UPDATE appliance_security_alerts
           SET detail = COALESCE(detail, '{}'::jsonb)
                        || jsonb_build_object('repeats', COALESCE((detail->>'repeats')::int, 1) + 1,
                                              'last_seen_at', now(), 'last_source_ip', $4::text)
         WHERE id = (SELECT id FROM appliance_security_alerts
                      WHERE appliance_id IS NOT DISTINCT FROM NULLIF($1,'')::uuid AND kind = $2 AND NOT resolved
                        AND created_at > now() - make_interval(secs => $3)
                      ORDER BY created_at DESC LIMIT 1)
        RETURNING id::text`, applianceID, kind, alertRepeatWindow.Seconds(), ip).Scan(&id)
	if err == nil {
		return false, nil
	}
	if err != pgx.ErrNoRows {
		return false, err
	}
	err = db.QueryRow(ctx, `
        INSERT INTO appliance_security_alerts (appliance_id, serial, kind, detail, source_ip)
        VALUES (NULLIF($1,'')::uuid, $2, $3, $4::jsonb, $5) RETURNING id::text`,
		applianceID, serial, kind, string(raw), ip).Scan(&id)
	return err == nil, err
}

// publicError is what an operator may see of an error: validation sentences pass through, but database errors never
// do (they name tables, constraints and values); those are logged with the trace id and replaced.
func publicError(r *http.Request, err error) string {
	if err == nil {
		return ""
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, pgx.ErrTxClosed) ||
		errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		slog.Warn("request failed on a database error", "path", r.URL.Path, "trace_id", middleware.GetReqID(r.Context()), "err", err)
		return "Central hit a database error (see its log)"
	}
	return err.Error()
}

// failInternal answers 500 with msg and logs the cause, never sending it to the client.
func failInternal(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.Warn(msg, "path", r.URL.Path, "trace_id", middleware.GetReqID(r.Context()), "err", err)
	Fail(w, r, http.StatusInternalServerError, CodeInternal, msg)
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

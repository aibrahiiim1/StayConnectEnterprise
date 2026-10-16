package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// alertRepeatWindow is how long an open security alert absorbs repeats of the same event instead of a new row
// being raised. Registration is unauthenticated: without it, anyone able to reach Central could raise an alert
// every two seconds (the registration limit) for as long as they liked, burying the real ones.
const alertRepeatWindow = 24 * time.Hour

// alertDB is what raiseAlertOnce needs: a pool or a transaction.
type alertDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// raiseAlertOnce records a security alert for (applianceID, kind). While an unresolved alert of the same kind
// for the same appliance was raised within alertRepeatWindow, the event is folded into it -- its detail gains
// repeats, last_seen_at and last_source_ip -- and no new row appears. It reports whether a NEW alert was
// raised, which is also when the caller audits (so a flood is one alert and one audit row, not thousands).
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

// isUniqueViolation reports a Postgres unique-constraint violation (23505).
func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

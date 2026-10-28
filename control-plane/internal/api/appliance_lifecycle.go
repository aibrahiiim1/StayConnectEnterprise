package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func recordLifecycle(ctx context.Context, db Querier, applianceID, from, to, actor, srcIP, reason string) {
	_, _ = db.Exec(ctx, `
        INSERT INTO appliance_lifecycle_events (appliance_id, from_state, to_state, actor, source_ip, reason)
        VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''))`,
		applianceID, from, to, actor, srcIP, reason)
}

func emailOf(s *auth.Session) string {
	if s == nil {
		return "system"
	}
	return s.Email
}

// clientIP: see package clientip for which headers are trusted.
func clientIP(r *http.Request) string { return clientip.From(r) }

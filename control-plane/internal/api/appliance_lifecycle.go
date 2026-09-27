package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx (used for lifecycle
// events written inside or outside a transaction).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// recordLifecycle appends an immutable lifecycle event.
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

// clientIPFromReq is the request's client address (see package clientip for which headers are trusted).
func clientIPFromReq(r *http.Request) string { return clientip.From(r) }

// clientIP is the same, kept under the name the appliance-facing handlers use.
func clientIP(r *http.Request) string { return clientip.From(r) }

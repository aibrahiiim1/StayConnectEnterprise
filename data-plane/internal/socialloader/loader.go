// Package socialloader resolves a tenant's social OAuth providers from
// the DB and registers the right implementation in a social.Registry.
//
// Falls back to the in-process Stub for any provider that has no enabled
// row — keeps dev/test environments working without real OAuth credentials.
package socialloader

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/social"
)

// Load reads enabled rows for tenantID, constructs the matching impl per
// provider, and returns a populated registry. Callers typically pre-stage
// the registry with stub fallbacks before calling Load — anything Load
// finds in the DB overrides those entries.
func Load(ctx context.Context, db *pgxpool.Pool, tenantID string, fallback *social.Registry) (*social.Registry, error) {
	if fallback == nil {
		fallback = social.NewRegistry()
	}
	rows, err := db.Query(ctx, `
        SELECT provider, COALESCE(client_id,''), COALESCE(client_secret,''),
               COALESCE(scopes,'')
          FROM social_oauth_providers
         WHERE tenant_id = $1 AND enabled = true
    `, tenantID)
	if err != nil {
		return fallback, fmt.Errorf("socialloader: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var provider, clientID, clientSecret, scopes string
		if err := rows.Scan(&provider, &clientID, &clientSecret, &scopes); err != nil {
			slog.Warn("socialloader: scan failed", "err", err)
			continue
		}
		switch provider {
		case "google":
			g, err := social.NewGoogle(clientID, clientSecret, scopes)
			if err != nil {
				slog.Warn("socialloader: google construct failed; keeping fallback", "err", err)
				continue
			}
			fallback.Register(g)
			slog.Info("socialloader: registered", "provider", "google", "kind", "real")
		default:
			// apple/facebook/microsoft can plug in here when their impls land.
			slog.Warn("socialloader: provider not yet implemented; keeping fallback", "provider", provider)
		}
	}
	return fallback, rows.Err()
}

// Package socialloader resolves a tenant's social OAuth providers from
// the DB and registers the right implementation in a social.Registry.
//
// Falls back to the in-process Stub for any provider that has no enabled
// row — keeps dev/test environments working without real OAuth credentials.
package socialloader

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/social"
)

// Extra is the non-secret, provider-specific configuration kept in
// social_oauth_providers.extra (jsonb). Only the keys a provider reads are
// defined; anything else in the column is ignored.
type Extra struct {
	Tenant string `json:"tenant,omitempty"`  // microsoft: common|organizations|consumers|<tenant id>|<domain>
	TeamID string `json:"team_id,omitempty"` // apple: 10-character Team ID
	KeyID  string `json:"key_id,omitempty"`  // apple: 10-character Key ID of the .p8 key
}

// Build constructs the real provider for one configuration row. The secret is
// passed in and never appears in the returned error.
func Build(provider, clientID, clientSecret, scopes string, extra Extra) (social.Provider, error) {
	switch provider {
	case "google":
		return social.NewGoogle(clientID, clientSecret, scopes)
	case "microsoft":
		return social.NewMicrosoft(clientID, clientSecret, scopes, extra.Tenant)
	case "apple":
		return social.NewApple(clientID, clientSecret, extra.TeamID, extra.KeyID, scopes)
	case "facebook":
		return social.NewFacebook(clientID, clientSecret, scopes)
	}
	return nil, social.ErrUnknownProvider
}

// Load reads enabled rows for tenantID, constructs the matching impl per
// provider, and returns a populated registry. Callers typically pre-stage
// the registry with stub fallbacks before calling Load — anything Load
// finds in the DB overrides those entries. A row that cannot be built
// (missing credentials, an unreadable Apple key) is logged by provider name
// and error only — never the secret — and the fallback stays in place.
func Load(ctx context.Context, db *pgxpool.Pool, tenantID string, fallback *social.Registry) (*social.Registry, error) {
	if fallback == nil {
		fallback = social.NewRegistry()
	}
	rows, err := db.Query(ctx, `
        SELECT provider, COALESCE(client_id,''), COALESCE(client_secret,''),
               COALESCE(scopes,''), COALESCE(extra,'{}'::jsonb)::text
          FROM social_oauth_providers
         WHERE tenant_id = $1 AND enabled = true
    `, tenantID)
	if err != nil {
		return fallback, fmt.Errorf("socialloader: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var provider, clientID, clientSecret, scopes, extraJSON string
		if err := rows.Scan(&provider, &clientID, &clientSecret, &scopes, &extraJSON); err != nil {
			slog.Warn("socialloader: scan failed", "err", err)
			continue
		}
		var extra Extra
		if err := json.Unmarshal([]byte(extraJSON), &extra); err != nil {
			slog.Warn("socialloader: extra is not valid JSON; keeping fallback", "provider", provider)
			continue
		}
		p, err := Build(provider, clientID, clientSecret, scopes, extra)
		if err != nil {
			slog.Warn("socialloader: construct failed; keeping fallback", "provider", provider, "err", err)
			continue
		}
		fallback.Register(p)
		slog.Info("socialloader: registered", "provider", provider, "kind", "real")
	}
	return fallback, rows.Err()
}

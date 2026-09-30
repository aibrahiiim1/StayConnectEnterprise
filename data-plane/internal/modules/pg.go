package modules

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgLocal reads iam_v2.site_module_get, the single source of the site's module switches.
type PgLocal struct{ DB *pgxpool.Pool }

// SiteModules returns module id → enabled for the switchable modules.
func (p PgLocal) SiteModules(ctx context.Context, tenantID, siteID string) (map[string]bool, error) {
	rows, err := p.DB.Query(ctx, `SELECT module_id, enabled FROM iam_v2.site_module_get($1::uuid, $2::uuid)`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var en bool
		if err := rows.Scan(&id, &en); err != nil {
			return nil, err
		}
		out[id] = en
	}
	return out, rows.Err()
}

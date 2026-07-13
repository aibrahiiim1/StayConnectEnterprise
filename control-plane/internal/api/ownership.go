package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// legacyGuardTables are archived, read-only guest-data tables reached by the
// tenant/site FK cascade. Their statement-level `legacy_ro` guard fires even on
// a zero-row cascade DELETE, which would abort a legitimate empty-resource
// delete. disableLegacyGuards turns the guard off for the life of an ownership
// delete TRANSACTION (DDL is transactional — a rollback reverts it); the caller
// MUST re-enable before commit so the tables stay read-only for everyone else.
var legacyGuardTables = []string{"guests", "sessions", "vouchers"}

func disableLegacyGuards(ctx context.Context, tx pgx.Tx) ([]string, error) {
	var toggled []string
	for _, t := range legacyGuardTables {
		var has bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=$1::regclass AND tgname='legacy_ro' AND NOT tgisinternal)`,
			t).Scan(&has); err != nil {
			return toggled, err
		}
		if !has {
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER TABLE %s DISABLE TRIGGER legacy_ro", t)); err != nil {
			return toggled, err
		}
		toggled = append(toggled, t)
	}
	return toggled, nil
}

func enableLegacyGuards(ctx context.Context, tx pgx.Tx, toggled []string) error {
	for _, t := range toggled {
		if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ENABLE TRIGGER legacy_ro", t)); err != nil {
			return err
		}
	}
	return nil
}

// blocker is one class of dependent record that prevents a non-cascading delete.
// The UI renders these into "delete or archive these first" with links built
// from Resource + IDs.
type blocker struct {
	Type     string   `json:"type"`     // machine key: site|appliance|license|subscription|token|command
	Label    string   `json:"label"`    // human label, e.g. "Sites"
	Count    int      `json:"count"`    // how many exist
	Resource string   `json:"resource"` // UI route hint, e.g. "sites", "appliances", "licenses"
	IDs      []string `json:"ids,omitempty"`
}

// deleteConfirm is the body required for every permanent (non-cascading) delete:
// a typed confirmation (matched against the resource name) and a reason recorded
// in the immutable audit trail.
type deleteConfirm struct {
	Confirm string `json:"confirm"` // must match the resource name/serial exactly
	Reason  string `json:"reason"`
}

// countIDs runs a `SELECT id::text ...` query and returns the matched IDs (capped
// so the response stays small — the count is exact, the id list is a sample).
func (b *Base) countIDs(ctx context.Context, query string, args ...any) ([]string, int, error) {
	rows, err := b.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var ids []string
	n := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		n++
		if len(ids) < 25 {
			ids = append(ids, id)
		}
	}
	return ids, n, rows.Err()
}

// tenantBlockers returns the dependent records that must be removed before a
// Customer can be permanently deleted, in the enforced deletion order.
func (b *Base) tenantBlockers(ctx context.Context, tenantID string) ([]blocker, error) {
	var out []blocker
	// Appliances first (they must be deactivated + deleted before their site).
	if ids, n, err := b.countIDs(ctx, `SELECT id::text FROM appliances WHERE tenant_id=$1`, tenantID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "appliance", Label: "Appliances", Count: n, Resource: "appliances", IDs: ids})
	}
	// Sites.
	if ids, n, err := b.countIDs(ctx, `SELECT id::text FROM sites WHERE tenant_id=$1`, tenantID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "site", Label: "Sites", Count: n, Resource: "sites", IDs: ids})
	}
	// Any retained license (active or historical) — licenses are commercial
	// records; a Customer holding licenses must be archived, not deleted.
	if ids, n, err := b.countIDs(ctx, `SELECT id::text FROM licenses WHERE tenant_id=$1`, tenantID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "license", Label: "Licenses", Count: n, Resource: "licenses", IDs: ids})
	}
	// Active (non-canceled) subscription.
	if ids, n, err := b.countIDs(ctx, `SELECT id::text FROM tenant_subscriptions WHERE tenant_id=$1 AND status <> 'canceled' AND ended_at IS NULL`, tenantID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "subscription", Label: "Active Subscription", Count: n, Resource: "subscription", IDs: ids})
	}
	// Live enrollment tokens.
	if ids, n, err := b.countIDs(ctx, `SELECT id::text FROM appliance_bootstrap_tokens WHERE tenant_id=$1 AND consumed_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, tenantID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "token", Label: "Enrollment Tokens", Count: n, Resource: "appliances", IDs: ids})
	}
	return out, nil
}

// siteBlockers returns the dependent records that must be removed before a Site
// can be permanently deleted.
func (b *Base) siteBlockers(ctx context.Context, siteID string) ([]blocker, error) {
	var out []blocker
	if ids, n, err := b.countIDs(ctx, `SELECT id::text FROM appliances WHERE site_id=$1`, siteID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "appliance", Label: "Appliances", Count: n, Resource: "appliances", IDs: ids})
	}
	// Only ACTIVE/suspended licenses block; revoked/superseded/expired rows are
	// cleaned up as part of the delete (their history is preserved in audit).
	if ids, n, err := b.countIDs(ctx, `SELECT id::text FROM licenses WHERE site_id=$1 AND status IN ('active','suspended')`, siteID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "license", Label: "Active Licenses", Count: n, Resource: "licenses", IDs: ids})
	}
	if ids, n, err := b.countIDs(ctx, `SELECT id::text FROM appliance_bootstrap_tokens WHERE site_id=$1 AND consumed_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, siteID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "token", Label: "Enrollment Tokens", Count: n, Resource: "appliances", IDs: ids})
	}
	// Pending lifecycle/command operations against any appliance at the site.
	if ids, n, err := b.countIDs(ctx, `
        SELECT c.id::text FROM appliance_commands c
          JOIN appliances a ON a.id = c.appliance_id
         WHERE a.site_id=$1 AND c.status IN ('pending','issued','delivered','acknowledged')`, siteID); err != nil {
		return nil, err
	} else if n > 0 {
		out = append(out, blocker{Type: "command", Label: "Pending Operations", Count: n, Resource: "appliances", IDs: ids})
	}
	return out, nil
}

// failBlocked writes the standard 409 conflict body listing exactly what must be
// removed first. `what` is "Customer" or "Site".
func failBlocked(w http.ResponseWriter, r *http.Request, what string, blockers []blocker) {
	Fail(w, r, http.StatusConflict, CodeConflict,
		what+" cannot be deleted because it still contains dependent records. Delete or archive them first.",
		map[string]any{"blocking": blockers})
}

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// tenantOwnedTables are the guest/tenant-owned tables in the appliance SITE
// database whose every row belongs to exactly one Customer (tenant_id). On a
// cross-tenant transition each row NOT owned by the current tenant is securely
// purged. Ordered child→parent so the intra-DB foreign keys (all the tenant_id /
// site_id FKs -> tenants/sites) are satisfied within a single transaction.
//
// DELIBERATELY EXCLUDED (classified NOT tenant-owned, so preserved):
//   - appliance/system/network/sync tables: appliances, appliance_*, backup_records,
//     dhcp_*, edge_*, network_*, schema_migrations, sync_checkpoints,
//     network_interfaces, system_network_audit — device/bootstrap-owned; keep the
//     box reachable and recoverable.
//   - guest_networks: network infrastructure. REPOINTED to the current owner (not
//     deleted) so the live VLAN / DHCP / captive portal keep running.
//   - audit_log: security/audit history class — retained (has no FK to the mirror);
//     the transition record itself is written here.
var tenantOwnedTables = []string{
	// children / leaves first (FK dependents)
	"sessions",
	"auth_otps",
	"pms_attempts",
	"accounting_records",
	"social_oauth_states",
	"stripe_events",
	"payments",
	"vouchers",
	"guests",
	// parents
	"voucher_batches",
	"walled_garden_rules",
	"notification_providers",
	"pms_providers",
	"social_oauth_providers",
	"stripe_accounts",
	"operator_roles",
	"operators",
	"tenant_effective_limits",
	"ticket_templates",
}

// hasForeignTenantData reports whether the site DB holds ANY tenant-owned row that
// does not belong to the current tenant (compared by immutable UUID) — the signal
// of a cross-tenant reassignment, a deleted-and-recreated Customer with a new
// UUID, decommission/reuse, or ownership transfer.
func (s *server) hasForeignTenantData(ctx context.Context) (bool, error) {
	q := "SELECT EXISTS(SELECT 1 FROM tenants WHERE id <> $1)"
	for _, t := range append([]string{"sites"}, tenantOwnedTables...) {
		q += " OR EXISTS(SELECT 1 FROM " + t + " WHERE tenant_id IS NOT NULL AND tenant_id <> $1)"
	}
	var foreign bool
	err := s.db.QueryRow(ctx, q, s.tenID).Scan(&foreign)
	return foreign, err
}

// reconcileTenantOwnership enforces single-tenant ownership of the appliance's
// local guest data. If any tenant-owned row does not belong to the CURRENT tenant
// (cross-tenant reassignment / deleted+recreated Customer / decommission-reuse /
// ownership transfer — compared by UUID, never by name or slug), all such rows and
// their cached secrets are securely purged in ONE transaction BEFORE guest
// authorization resumes; the live guest networks are repointed to the new owner;
// pending outbox events (queued under the previous identity, referencing purged
// data) are dropped; an audited transition record is written; and stale runtime
// guest authorization is flushed. Same-tenant deactivate/reactivate finds no
// foreign data and preserves everything.
//
// Idempotent: a clean or same-tenant boot returns immediately, and a retry after a
// partial failure re-runs the whole purge from a consistent state. On ANY error it
// returns non-nil and the caller MUST leave the appliance fail-closed (no guest
// authorization) — the transition is never treated as complete on a partial
// cleanup.
func (s *server) reconcileTenantOwnership(ctx context.Context) error {
	if s.db == nil || s.tenID == "" {
		return nil // not yet assigned to a tenant; nothing to reconcile
	}
	foreign, err := s.hasForeignTenantData(ctx)
	if err != nil {
		return fmt.Errorf("detect foreign-tenant data: %w", err)
	}
	if !foreign {
		return nil // same-tenant steady state — preserve all tenant data
	}
	slog.Warn("tenant-transition: foreign-tenant data present — securely purging previous tenant before guest auth resumes",
		"current_tenant", s.tenID, "current_site", s.siteID)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin purge tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Repoint the live guest networks to the current owner FIRST so the CHR VLAN /
	// DHCP / captive portal keep running and no stale owner reference remains.
	if _, err := tx.Exec(ctx,
		`UPDATE guest_networks SET tenant_id=$1, site_id=$2
		  WHERE tenant_id IS DISTINCT FROM $1 OR site_id IS DISTINCT FROM $2`,
		s.tenID, s.siteID); err != nil {
		return fmt.Errorf("repoint guest_networks: %w", err)
	}

	purged := map[string]int64{}
	for _, tbl := range tenantOwnedTables {
		ct, err := tx.Exec(ctx, "DELETE FROM "+tbl+" WHERE tenant_id IS NOT NULL AND tenant_id <> $1", s.tenID)
		if err != nil {
			return fmt.Errorf("purge %s: %w", tbl, err)
		}
		if n := ct.RowsAffected(); n > 0 {
			purged[tbl] = n
		}
	}
	// Site + tenant mirror rows: drop every owner that is not the current one.
	if ct, err := tx.Exec(ctx, `DELETE FROM sites WHERE tenant_id IS NOT NULL AND tenant_id <> $1`, s.tenID); err != nil {
		return fmt.Errorf("purge sites mirror: %w", err)
	} else if n := ct.RowsAffected(); n > 0 {
		purged["sites"] = n
	}
	if ct, err := tx.Exec(ctx, `DELETE FROM tenants WHERE id <> $1`, s.tenID); err != nil {
		return fmt.Errorf("purge tenants mirror: %w", err)
	} else if n := ct.RowsAffected(); n > 0 {
		purged["tenants"] = n
	}
	// Pending outbox events were queued under the previous owner/identity and
	// reference now-purged data; drop them. Fresh state re-syncs under the new
	// identity. (sync_outbox is not tenant-keyed, so a full clear is correct on a
	// confirmed cross-tenant transition.)
	if ct, err := tx.Exec(ctx, `DELETE FROM sync_outbox`); err != nil {
		return fmt.Errorf("purge sync_outbox: %w", err)
	} else if n := ct.RowsAffected(); n > 0 {
		purged["sync_outbox"] = n
	}

	// Controlled failure injection for acceptance testing: fail AFTER the deletes
	// but BEFORE commit, so the deferred Rollback proves no partial purge is ever
	// committed and the caller holds the appliance fail-closed. Off unless the env
	// var is explicitly set; never set in production.
	if os.Getenv("SCD_TENANT_PURGE_FAIL_INJECT") == "true" {
		return fmt.Errorf("injected cleanup failure (acceptance test) — transaction will roll back, no partial purge")
	}

	// Audited transition record — kept in the preserved audit_log (immutable
	// security/audit metadata + purged counts; no reusable secrets or guest PII).
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log
	    (tenant_id, actor_type, actor_id, action, target_type, target_id, payload)
	    VALUES ($1, 'system', NULL, 'appliance.tenant_transition_purge', 'appliance', $2, $3)`,
		s.tenID, s.applID, map[string]any{
			"new_tenant_id": s.tenID,
			"new_site_id":   s.siteID,
			"reason":        "cross-tenant transition: securely purged previous tenant-owned local data & secrets",
			"purged":        purged,
		}); err != nil {
		return fmt.Errorf("write transition audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit purge: %w", err)
	}
	slog.Warn("tenant-transition: secure purge complete", "current_tenant", s.tenID, "purged", purged)

	// Clear stale RUNTIME guest authorization: the kernel nftables set persists
	// across an scd restart, so a device authorized under the previous tenant could
	// otherwise stay admitted. A fresh tenant has no live guests yet, so a full
	// flush is safe and guarantees no previous-tenant authorization survives.
	s.flushGuestAuthRuntime(ctx)
	return nil
}

// flushGuestAuthRuntime clears the nftables guest-authorization set so no device
// authorized under a previous tenant remains admitted. Best-effort.
func (s *server) flushGuestAuthRuntime(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(c, "nft", "flush", "set", "inet", "stayconnect", "auth_ipv4").CombinedOutput(); err != nil {
		slog.Warn("tenant-transition: nft auth flush failed (best-effort)", "err", err, "out", string(out))
	}
}

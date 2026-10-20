package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// tenantOwnedTables are the guest/tenant-owned tables in the appliance SITE
// database whose every row belongs to exactly one Customer (tenant_id). On a
// cross-tenant transition each row NOT owned by the current tenant is securely
// purged. Ordered child→parent so the intra-DB foreign keys (all the tenant_id /
// site_id FKs -> tenants/sites) are satisfied within a single transaction.
//
// DELIBERATELY EXCLUDED (classified NOT tenant-owned, so preserved):
//   - appliance/system/network tables: appliances, appliance_*, backup_records,
//     dhcp_*, edge_*, network_*, schema_migrations, network_interfaces,
//     system_network_audit — device/bootstrap-owned; keep the box reachable and
//     recoverable.
//   - guest_networks: network infrastructure. REPOINTED to the current owner (not
//     deleted) so the live VLAN / DHCP / captive portal keep running.
//   - audit_log: security/audit history class — retained (has no FK to the mirror);
//     the transition record itself is written here.
var tenantOwnedTables = []string{
	// children / leaves first (FK dependents)
	"auth_otps",
	"pms_attempts",
	"accounting_records",
	"social_oauth_states",
	"stripe_events",
	// sealed provider secrets (0107, 0116): keyed by tenant, unreadable under another tenant's AAD, and purged
	// with the providers they belong to rather than left as ciphertext nobody can use.
	"iam_v2.notification_provider_secret_generations",
	"iam_v2.social_provider_secret_generations",
	// parents
	"walled_garden_rules",
	"notification_providers",
	"pms_providers",
	"social_oauth_providers",
	"operator_roles",
	"operators",
	"tenant_effective_limits",
}

// guestRecordTables are the iam_v2 tables whose rows are a customer's GUEST RECORDS (reservations, accounts, identities,
// devices, sign-in attempts, sessions). The purge below does not reach them: they are append-only, guarded, tied to
// running enforcement, and a departed customer's financial rows in them are kept by law. Their presence under another
// tenant is therefore detected and refused (GDPR final audit): serving guests next to the previous customer's guest
// records, or purging everything around them, would both be wrong. See DATA_OWNERSHIP.md, "Moving an appliance to
// another customer".
var guestRecordTables = []string{
	"iam_v2.stays", "iam_v2.guest_access_accounts", "iam_v2.guest_principals", "iam_v2.devices",
	"iam_v2.sign_in_attempts", "iam_v2.sessions",
}

// foreignGuestRecords reports whether the site database holds guest records of a tenant other than the current one.
func (s *server) foreignGuestRecords(ctx context.Context) (bool, error) {
	q := "SELECT false"
	for _, t := range guestRecordTables {
		q += " OR EXISTS(SELECT 1 FROM " + t + " WHERE tenant_id <> $1)"
	}
	var foreign bool
	err := s.db.QueryRow(ctx, q, s.tenID).Scan(&foreign)
	return foreign, err
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
// an audited transition record is written; and stale runtime
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
	// ANOTHER CUSTOMER'S GUEST RECORDS ARE NEVER SERVED NEXT TO, AND THIS VERSION CAN NOT PURGE THEM. Held fail-closed with
	// the reason, whether or not the other tenant tables hold anything: the appliance must be re-provisioned for its
	// new customer (the guest records go with the installation), not reassigned.
	if foreignGuests, err := s.foreignGuestRecords(ctx); err != nil {
		return fmt.Errorf("detect another customer's guest records: %w", err)
	} else if foreignGuests {
		return fmt.Errorf("this appliance holds another customer's guest records, which a reassignment does not remove: " +
			"it stays closed to guests until it is re-provisioned for its new customer (see DATA_OWNERSHIP.md, \"Moving an appliance to another customer\")")
	}
	if !foreign {
		return nil // same-tenant steady state — preserve all tenant data
	}
	slog.Warn("tenant-transition: foreign-tenant data present — securely purging previous tenant before guest auth resumes",
		"current_tenant", s.tenID, "current_site", s.siteID)

	// C35 — ARCHIVE BEFORE PURGE.
	//
	// This runs before the purge transaction opens, and a failure here aborts the whole reconciliation: the
	// caller holds the appliance fail-closed, which is the correct direction. Keeping a departed customer's
	// data one boot longer is recoverable; deleting it with no archive is not.
	//
	// The archive is also gated in the database (p4_assert_compliance_archived), so a future purge path
	// that forgot to call this would still be refused rather than silently succeeding.
	departing, err := s.departingTenants(ctx)
	if err != nil {
		return fmt.Errorf("identify departing tenants: %w", err)
	}
	for _, dt := range departing {
		if err := s.archiveTenantBeforePurge(ctx, dt); err != nil {
			return fmt.Errorf("compliance archive for departing tenant %s: %w", dt, err)
		}
		slog.Warn("tenant-transition: compliance archive written before purge",
			"departing_tenant", dt,
			"receipt", "NOT countersigned — no external archival authority exists in this product")
	}

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

	// The database's own gate (C35). It refuses the purge unless an EXTERNAL authority has acknowledged
	// custody of the archive -- not merely that this appliance wrote one, which would be the party doing the
	// deleting certifying its own copy.
	//
	// No archival receipt authority exists in this product, so in practice this ALWAYS refuses and the
	// cross-customer purge cannot complete. That is the correct failure mode and the caller holds the
	// appliance fail-closed, exactly as it does for any other incomplete transition. What matters here is
	// that the operator is told WHICH thing is missing: "cleanup failed" would send someone looking for a
	// database fault that does not exist.
	for _, dt := range departing {
		if _, err := tx.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, dt); err != nil {
			if strings.Contains(err.Error(), "COMPLIANCE_RECEIPT_UNVERIFIED") {
				return fmt.Errorf("compliance receipt for departing tenant %s is not verified: the archive "+
					"exists and its digest is recorded, but no external archival authority has acknowledged "+
					"custody of it. Cross-customer purge is blocked until one does; the appliance stays "+
					"fail-closed and no guest is authorized: %w", dt, err)
			}
			return fmt.Errorf("compliance gate for %s: %w", dt, err)
		}
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

	// THE ERASURE LEDGER AND ITS SECONDARY STORAGE BELONG TO THE CUSTOMER WHO IS LEAVING. The new customer starts a new ledger and
	// sets up a storage of their own; the old setting (and its credentials) is forgotten, the old customer keeps their copy.
	var hasReset bool
	if err := tx.QueryRow(ctx, `SELECT to_regprocedure('public.ledger_reset()') IS NOT NULL`).Scan(&hasReset); err != nil {
		return fmt.Errorf("check for the erasure ledger: %w", err)
	}
	if hasReset {
		if _, err := tx.Exec(ctx, `SELECT public.ledger_reset()`); err != nil {
			return fmt.Errorf("start a new erasure ledger: %w", err)
		}
		purged["erasure_ledger_reset"] = 1
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
	if purged["erasure_ledger_reset"] > 0 {
		s.ledgerForgetPrevious()
	}

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

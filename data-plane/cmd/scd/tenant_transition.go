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

// Guest/tenant-owned tables in the site database, each row belonging to exactly one Customer. On a cross-tenant transition rows not owned by the current
// tenant are securely purged; ordered child to parent so the FKs hold within one transaction. Deliberately excluded: appliance/system/network tables (the box
// stays reachable and recoverable), guest_networks (repointed to the current owner, not deleted, so VLAN/DHCP/portal keep running) and audit_log (retained;
// the transition record is written there).
var tenantOwnedTables = []string{
	"auth_otps",
	"pms_attempts",
	"accounting_records",
	"social_oauth_states",
	"stripe_events",
	// sealed provider secrets: keyed by tenant, unreadable under another tenant's AAD, purged with their providers.
	"iam_v2.notification_provider_secret_generations",
	"iam_v2.social_provider_secret_generations",
	"walled_garden_rules",
	"notification_providers",
	"pms_providers",
	"social_oauth_providers",
	"operator_roles",
	"operators",
	"tenant_effective_limits",
}

// iam_v2 tables holding a customer's GUEST RECORDS. The purge does not reach them (append-only, guarded, tied to enforcement, financial rows kept by law), so
// their presence under another tenant is detected and refused: serving guests beside the previous customer's records, or purging around them, would both be
// wrong. See DATA_OWNERSHIP.md, "Moving an appliance to another customer".
var guestRecordTables = []string{
	"iam_v2.stays", "iam_v2.guest_access_accounts", "iam_v2.guest_principals", "iam_v2.devices",
	"iam_v2.sign_in_attempts", "iam_v2.sessions",
}

func (s *server) foreignGuestRecords(ctx context.Context) (bool, error) {
	q := "SELECT false"
	for _, t := range guestRecordTables {
		q += " OR EXISTS(SELECT 1 FROM " + t + " WHERE tenant_id <> $1)"
	}
	var foreign bool
	err := s.db.QueryRow(ctx, q, s.tenID).Scan(&foreign)
	return foreign, err
}

// Reports whether the site DB holds ANY tenant-owned row not belonging to the current tenant (compared by immutable UUID): the signal of a cross-tenant
// reassignment, a recreated Customer, decommission/reuse or ownership transfer.
func (s *server) hasForeignTenantData(ctx context.Context) (bool, error) {
	q := "SELECT EXISTS(SELECT 1 FROM tenants WHERE id <> $1)"
	for _, t := range append([]string{"sites"}, tenantOwnedTables...) {
		q += " OR EXISTS(SELECT 1 FROM " + t + " WHERE tenant_id IS NOT NULL AND tenant_id <> $1)"
	}
	var foreign bool
	err := s.db.QueryRow(ctx, q, s.tenID).Scan(&foreign)
	return foreign, err
}

// If any tenant-owned row does not belong to the CURRENT tenant (compared by UUID, never name or slug), all such rows and their cached secrets are
// purged in ONE transaction BEFORE guest authorization resumes, guest networks are repointed, an audited record is written and runtime authorization
// is flushed. Same-tenant reactivation preserves everything. Idempotent; on ANY error it returns non-nil and the caller MUST leave the appliance
// fail-closed: a partial cleanup is never a completed transition.
func (s *server) reconcileTenantOwnership(ctx context.Context) error {
	if s.db == nil || s.tenID == "" {
		return nil // not yet assigned to a tenant; nothing to reconcile
	}
	foreign, err := s.hasForeignTenantData(ctx)
	if err != nil {
		return fmt.Errorf("detect foreign-tenant data: %w", err)
	}
	// Another customer's GUEST RECORDS are never served next to, and this version cannot purge them: held fail-closed whether or not other tenant tables
	// hold anything; the appliance must be re-provisioned for its new customer, not reassigned.
	if foreignGuests, err := s.foreignGuestRecords(ctx); err != nil {
		return fmt.Errorf("detect another customer's guest records: %w", err)
	} else if foreignGuests {
		return fmt.Errorf("this appliance holds another customer's guest records, which a reassignment does not remove: " +
			"it stays closed to guests until it is re-provisioned for its new customer (see DATA_OWNERSHIP.md, \"Moving an appliance to another customer\")")
	}
	if !foreign {
		return nil
	}
	slog.Warn("tenant-transition: foreign-tenant data present — securely purging previous tenant before guest auth resumes",
		"current_tenant", s.tenID, "current_site", s.siteID)

	// Archive before purge, before the purge transaction opens: a failure aborts the reconciliation and the caller holds fail-closed (keeping data one
	// boot longer is recoverable, deleting without an archive is not). Also gated in the database (p4_assert_compliance_archived).
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

	// Repoint the live guest networks to the current owner FIRST so the VLAN, DHCP and captive portal keep running.
	if _, err := tx.Exec(ctx,
		`UPDATE guest_networks SET tenant_id=$1, site_id=$2
		  WHERE tenant_id IS DISTINCT FROM $1 OR site_id IS DISTINCT FROM $2`,
		s.tenID, s.siteID); err != nil {
		return fmt.Errorf("repoint guest_networks: %w", err)
	}

	// The database gate refuses the purge unless an EXTERNAL authority acknowledged custody of the archive (the deleting party must not certify its own
	// copy). No receipt authority exists yet, so this always refuses and the caller holds fail-closed; the error must say WHICH thing is missing.
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

	// The erasure ledger and its secondary storage belong to the departing customer: the new customer starts a new ledger and its own storage setup;
	// the old setting and credentials are forgotten.
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

	// Failure injection for acceptance: fail AFTER the deletes but BEFORE commit to prove no partial purge commits. Off unless the env var is set; never in production.
	if os.Getenv("SCD_TENANT_PURGE_FAIL_INJECT") == "true" {
		return fmt.Errorf("injected cleanup failure (acceptance test) — transaction will roll back, no partial purge")
	}

	// Audited transition record in the preserved audit_log: metadata and purged counts only, no secrets or guest PII.
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

	// The kernel nftables set persists across an scd restart, so a device authorized under the previous tenant could stay admitted; a fresh tenant
	// has no live guests, so a full flush is safe.
	s.flushGuestAuthRuntime(ctx)
	return nil
}

// Best-effort.
func (s *server) flushGuestAuthRuntime(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(c, "nft", "flush", "set", "inet", "stayconnect", "auth_ipv4").CombinedOutput(); err != nil {
		slog.Warn("tenant-transition: nft auth flush failed (best-effort)", "err", err, "out", string(out))
	}
}

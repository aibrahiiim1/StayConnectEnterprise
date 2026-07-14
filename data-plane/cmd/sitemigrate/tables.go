package main

import "strings"

// fkRef describes one foreign-key-like edge used for the orphan pre-scan and
// for the FK-order unit test. RefTable must appear BEFORE the owning table in
// migrationTables.
type fkRef struct {
	Col      string // column on the owning table (may be nullable)
	RefTable string // referenced table (must be part of the migration set)
	RefCol   string // referenced column (always the ref table's PK here)
}

// tableSpec describes one table to migrate.
//
// Where is a predicate valid against BOTH databases (the edge schema is
// shape-compatible), parameterised as $1 = tenant UUID, $2 = site UUID.
// PK is the single-column primary key used as the ON CONFLICT target;
// empty PK marks a TimescaleDB hypertable WITHOUT a primary key
// (accounting_records, audit_log) — those cannot be deduped by ON CONFLICT,
// so if dest already has scoped rows the table is skipped entirely unless
// --force-append is given.
type tableSpec struct {
	Name  string
	Where string
	PK    string
	FKs   []fkRef
	Note  string
}

// scoped-appliance subquery shared by sessions / accounting_records.
const siteAppliances = "(SELECT id FROM appliances WHERE tenant_id = $1 AND site_id = $2)"

// keepOperators: the tenant's operators MINUS those whose ONLY role is
// platform_admin (platform admins are a cloud concern; they must not become
// local site operators — and the edge operator_roles CHECK constraint does
// not even allow the platform_admin role value).
const keepOperators = `tenant_id = $1 AND (
    NOT EXISTS (SELECT 1 FROM operator_roles r WHERE r.operator_id = operators.id AND r.role = 'platform_admin')
    OR EXISTS (SELECT 1 FROM operator_roles r WHERE r.operator_id = operators.id AND r.role <> 'platform_admin'))`

// migrationTables lists every table to copy, in FK-safe order: each entry's
// FK targets appear earlier in the slice (enforced by TestFKOrder).
//
// Scoping rules:
//   - tenants / sites: the single row for this tenant / site.
//   - appliances: this site's rows.
//   - sessions / accounting_records: rows whose appliance belongs to this
//     site (they carry appliance scoping in addition to tenant_id).
//   - pms_providers / walled_garden_rules / payments: tenant-wide rows
//     (site_id IS NULL) plus rows explicitly for this site.
//   - everything else: tenant-scoped. NOTE for multi-site tenants: tables
//     without a site_id column (vouchers, ticket_templates, guests,
//     voucher_batches, auth_otps, pms_attempts, provider configs, ...)
//     cannot be split automatically — all tenant rows are copied. Split
//     manually before migrating a second site of the same tenant.
var migrationTables = []tableSpec{
	{
		Name:  "tenants",
		Where: "id = $1",
		PK:    "id",
		Note:  "single row (identity + auth_methods)",
	},
	{
		Name:  "sites",
		Where: "id = $2 AND tenant_id = $1",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}},
		Note:  "single row",
	},
	{
		Name:  "appliances",
		Where: "tenant_id = $1 AND site_id = $2",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}, {"site_id", "sites", "id"}},
	},
	{
		Name:  "operators",
		Where: keepOperators,
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}},
		Note:  "platform_admin-only operators skipped",
	},
	{
		Name: "operator_roles",
		// Roles of the kept operators, minus platform_admin role rows (the
		// edge CHECK constraint rejects that value; skipped operators only
		// ever had platform_admin rows, so none of their rows survive).
		Where: "role <> 'platform_admin' AND operator_id IN (SELECT id FROM operators WHERE " + keepOperators + ")",
		PK:    "id",
		FKs:   []fkRef{{"operator_id", "operators", "id"}, {"tenant_id", "tenants", "id"}},
		Note:  "platform_admin role rows dropped",
	},
	{
		Name:  "ticket_templates",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}},
		Note:  "tenant-scoped (no site_id)",
	},
	{
		Name:  "voucher_batches",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs: []fkRef{
			{"tenant_id", "tenants", "id"},
			{"template_id", "ticket_templates", "id"},
			{"created_by", "operators", "id"},
		},
		Note: "tenant-scoped (no site_id)",
	},
	{
		Name:  "vouchers",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs: []fkRef{
			{"tenant_id", "tenants", "id"},
			{"template_id", "ticket_templates", "id"},
			{"batch_id", "voucher_batches", "id"},
		},
		Note: "tenant-scoped (no site_id)",
	},
	{
		Name:  "guests",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}},
		Note:  "tenant-scoped (no site_id)",
	},
	{
		Name:  "sessions",
		Where: "tenant_id = $1 AND appliance_id IN " + siteAppliances,
		PK:    "id",
		FKs: []fkRef{
			{"tenant_id", "tenants", "id"},
			{"site_id", "sites", "id"},
			{"appliance_id", "appliances", "id"},
			{"guest_id", "guests", "id"},
			{"voucher_id", "vouchers", "id"},
		},
		Note: "scoped via this site's appliances",
	},
	{
		Name:  "accounting_records",
		Where: "tenant_id = $1 AND appliance_id IN " + siteAppliances,
		PK:    "", // hypertable, no PK: skip-if-dest-non-empty strategy
		Note:  "hypertable, no PK; scoped via appliances",
	},
	{
		Name:  "auth_otps",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs: []fkRef{
			{"tenant_id", "tenants", "id"},
			{"appliance_id", "appliances", "id"},
			{"template_id", "ticket_templates", "id"},
		},
		Note: "tenant-scoped (no site_id)",
	},
	{
		Name:  "social_oauth_states",
		Where: "tenant_id = $1",
		PK:    "state",
		FKs: []fkRef{
			{"tenant_id", "tenants", "id"},
			{"appliance_id", "appliances", "id"},
			{"template_id", "ticket_templates", "id"},
		},
		Note: "tenant-scoped (no site_id)",
	},
	{
		Name:  "pms_providers",
		Where: "tenant_id = $1 AND (site_id IS NULL OR site_id = $2)",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}, {"site_id", "sites", "id"}},
		Note:  "tenant-wide (site_id IS NULL) + this site's rows",
	},
	{
		Name:  "pms_attempts",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}, {"appliance_id", "appliances", "id"}},
		Note:  "tenant-scoped (no site_id)",
	},
	{
		Name:  "walled_garden_rules",
		Where: "tenant_id = $1 AND (site_id IS NULL OR site_id = $2)",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}, {"site_id", "sites", "id"}},
		Note:  "tenant-wide (site_id IS NULL) + this site's rows",
	},
	{
		Name:  "notification_providers",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}},
		Note:  "tenant-scoped (no site_id)",
	},
	{
		Name:  "social_oauth_providers",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}},
		Note:  "tenant-scoped (no site_id)",
	},
	{
		Name:  "stripe_accounts",
		Where: "tenant_id = $1",
		PK:    "id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}},
		Note:  "tenant-scoped (no site_id)",
	},
	{
		Name:  "payments",
		Where: "tenant_id = $1 AND (site_id IS NULL OR site_id = $2)",
		PK:    "id",
		FKs: []fkRef{
			{"tenant_id", "tenants", "id"},
			{"site_id", "sites", "id"},
			{"template_id", "ticket_templates", "id"},
			{"voucher_id", "vouchers", "id"},
		},
		Note: "tenant rows for this site (or site_id IS NULL)",
	},
	{
		Name:  "stripe_events",
		Where: "tenant_id = $1",
		PK:    "event_id",
		FKs:   []fkRef{{"tenant_id", "tenants", "id"}},
		Note:  "tenant-scoped (no site_id)",
	},
	{
		Name:  "audit_log",
		Where: "tenant_id = $1",
		PK:    "", // hypertable, no PK: skip-if-dest-non-empty strategy
		Note:  "hypertable, no PK; tenant-scoped",
	},
}

// specFor returns the spec for a table name (used by the orphan scanner to
// resolve the referenced table's scope predicate).
func specFor(name string) (tableSpec, bool) {
	for _, s := range migrationTables {
		if s.Name == name {
			return s, true
		}
	}
	return tableSpec{}, false
}

// intersectColumns returns the columns present in BOTH src and dst, in src
// order. This is what makes the copy tolerant of schema drift in either
// direction (extra edge-only columns like tenants.branding, or central-only
// columns the edge schema dropped).
func intersectColumns(src, dst []string) []string {
	in := make(map[string]bool, len(dst))
	for _, c := range dst {
		in[c] = true
	}
	out := make([]string, 0, len(src))
	for _, c := range src {
		if in[c] {
			out = append(out, c)
		}
	}
	return out
}

// whereArgs returns the positional args a Where predicate needs. Every
// predicate uses $1 (tenant); only some use $2 (site) — Postgres rejects
// unused bind parameters, so we must not over-supply.
func whereArgs(where, tenant, site string) []any {
	if strings.Contains(where, "$2") {
		return []any{tenant, site}
	}
	return []any{tenant}
}

// inlineParams substitutes validated UUID literals for $1/$2 so a predicate
// can be used where bind parameters are unavailable (COPY ... TO STDOUT).
// Callers must have validated tenant/site with isUUID first.
func inlineParams(where, tenant, site string) string {
	where = strings.ReplaceAll(where, "$1", "'"+tenant+"'::uuid")
	where = strings.ReplaceAll(where, "$2", "'"+site+"'::uuid")
	return where
}

package main

import "strings"

// One foreign-key-like edge for the orphan pre-scan and the FK-order test; RefTable must appear BEFORE the owning table in migrationTables.
type fkRef struct {
	Col      string // column on the owning table (may be nullable)
	RefTable string // referenced table (must be part of the migration set)
	RefCol   string // referenced column (always the ref table's PK here)
}

// Where is valid against BOTH databases, parameterised $1 = tenant UUID, $2 = site UUID. Empty PK marks a PK-less TimescaleDB hypertable (accounting_records,
// audit_log): ON CONFLICT cannot dedupe it, so it is skipped if dest already has scoped rows unless --force-append.
type tableSpec struct {
	Name  string
	Where string
	PK    string
	FKs   []fkRef
	Note  string
}

// scoped-appliance subquery used by accounting_records.
const siteAppliances = "(SELECT id FROM appliances WHERE tenant_id = $1 AND site_id = $2)"

// keepOperators: the tenant operators minus those whose ONLY role is platform_admin (a cloud concern; the edge operator_roles CHECK does not allow it).
const keepOperators = `tenant_id = $1 AND (
    NOT EXISTS (SELECT 1 FROM operator_roles r WHERE r.operator_id = operators.id AND r.role = 'platform_admin')
    OR EXISTS (SELECT 1 FROM operator_roles r WHERE r.operator_id = operators.id AND r.role <> 'platform_admin'))`

// Every table to copy, in FK-safe order (enforced by TestFKOrder). Scoping: tenants/sites and appliances by this tenant/site; accounting_records by
// appliance; pms_providers and walled_garden_rules tenant-wide (site_id IS NULL) plus this site; everything else tenant-scoped. Tables without site_id
// cannot be split for a multi-site tenant: all tenant rows are copied.
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
		// Roles of the kept operators minus platform_admin rows (the edge CHECK rejects that value).
		Where: "role <> 'platform_admin' AND operator_id IN (SELECT id FROM operators WHERE " + keepOperators + ")",
		PK:    "id",
		FKs:   []fkRef{{"operator_id", "operators", "id"}, {"tenant_id", "tenants", "id"}},
		Note:  "platform_admin role rows dropped",
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

func specFor(name string) (tableSpec, bool) {
	for _, s := range migrationTables {
		if s.Name == name {
			return s, true
		}
	}
	return tableSpec{}, false
}

// Columns present in BOTH src and dst, in src order, which tolerates schema drift in either direction.
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

// Every predicate uses $1; only some use $2, and Postgres rejects unused bind parameters, so do not over-supply.
func whereArgs(where, tenant, site string) []any {
	if strings.Contains(where, "$2") {
		return []any{tenant, site}
	}
	return []any{tenant}
}

// Substitutes validated UUID literals for $1/$2 where bind parameters are unavailable (COPY ... TO STDOUT); callers must validate with isUUID first.
func inlineParams(where, tenant, site string) string {
	where = strings.ReplaceAll(where, "$1", "'"+tenant+"'::uuid")
	where = strings.ReplaceAll(where, "$2", "'"+site+"'::uuid")
	return where
}

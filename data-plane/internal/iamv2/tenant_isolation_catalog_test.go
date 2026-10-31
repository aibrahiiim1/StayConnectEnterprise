package iamv2

// Tenant isolation in iam_v2 (audit AD-12, P3 L11). iam_v2 has no row-level security: isolation rests on every table carrying
// its tenant (and, where it is site-owned, its site) and on every query filtering by them. Two checks hold that:
//
//   1. THE CATALOG (needs a full-schema database, PHASE3_TEST_DSN): every iam_v2 table has tenant_id and site_id, or is named in
//      tenantOnlyTables / unscopedTables with the reason it is not site-owned or not tenant-owned. A new table with neither is a
//      failure, and so is an exemption for a table that no longer exists. The set of tenant-keyed tables is mirrored in
//      testdata/iamv2_tenant_tables.txt so the second check runs everywhere.
//
//   2. THE QUERY PATHS (static, runs everywhere): every SQL literal in data-plane/cmd and data-plane/internal that reads or
//      writes a tenant-keyed table must mention tenant_id. 151 literals that did not were found when this test was written; most
//      reach a row by a primary key that an earlier tenant-filtered step produced (an appliance serves exactly one customer),
//      and each is recorded in testdata/iamv2_unfiltered_queries.txt. The list is a RATCHET: it may only shrink. A new
//      literal that touches a tenant-keyed table without tenant_id fails the build unless a reviewer adds it there on purpose.
//
// Regenerate both files after a deliberate change with UPDATE_TENANT_ISOLATION=1 (a database is needed for the table list).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantOnlyTables carry tenant_id but no site_id, deliberately.
var tenantOnlyTables = map[string]string{
	"guest_principals":                         "a client is known across every site of the customer; the site is on the records that use them",
	"guest_principal_identities":               "the contact identities of a client principal, tenant-wide like the principal",
	"notification_provider_secret_generations": "sealed provider secrets are keyed by tenant and provider (the provider rows carry the site)",
	"social_provider_secret_generations":       "sealed provider secrets are keyed by tenant and provider",
	"otp_device_bindings":                      "a one-time-code binding is per challenge; the challenge carries the site",
}

// unscopedTables carry neither: they are children keyed by their parent, singletons, or appliance-wide bookkeeping.
var unscopedTables = map[string]string{
	"assisted_guest_request_anchors": "child of assisted_guest_requests (request_id), which carries tenant and site",
	"assisted_guest_request_events":  "child of assisted_guest_requests (request_id)",
	"assisted_guest_request_records": "child of assisted_guest_requests (request_id)",
	"controlled_operation_scope":     "per-transaction scratch that names the operation, not a customer's data",
	"ledger_mac_key":                 "the one erasure-ledger key of this appliance (AD-3)",
	"ledger_meta":                    "the one erasure-ledger identity of this appliance",
	"retention_floor":                "a singleton: the earliest time retention may reach",
	"scrub_registry":                 "static registry of columns the erasure scrub reaches",
	"stay_event_payload_reductions":  "child of stay_events (event_id, which carries tenant and site): ids only, the undo record of 0171",
}

const (
	tablesFile     = "testdata/iamv2_tenant_tables.txt"
	unfilteredFile = "testdata/iamv2_unfiltered_queries.txt"
)

func TestIAMv2CatalogEveryTableCarriesItsTenantAndSite(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN (a full appliance schema) is needed for the table catalog")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `
	  SELECT c.relname, bool_or(a.attname = 'tenant_id'), bool_or(a.attname = 'site_id')
	    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
	   WHERE n.nspname = 'iam_v2' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
	   GROUP BY c.relname ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keyed []string
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		var hasT, hasS bool
		if err := rows.Scan(&name, &hasT, &hasS); err != nil {
			t.Fatal(err)
		}
		seen[name] = true
		if hasT {
			keyed = append(keyed, name)
		}
		_, tenantOnly := tenantOnlyTables[name]
		_, unscoped := unscopedTables[name]
		switch {
		case hasT && hasS:
			if tenantOnly || unscoped {
				t.Errorf("%s has tenant_id and site_id but is listed as an exemption: remove the stale entry", name)
			}
		case hasT && !hasS:
			if !tenantOnly {
				t.Errorf("iam_v2.%s has tenant_id but no site_id and is not in tenantOnlyTables: say why it is not site-owned, or add site_id", name)
			}
		default:
			if !unscoped {
				t.Errorf("iam_v2.%s has no tenant_id: add the key, or name it in unscopedTables with the reason it is not customer data", name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, m := range []map[string]string{tenantOnlyTables, unscopedTables} {
		for name := range m {
			if !seen[name] {
				t.Errorf("exemption for iam_v2.%s, which no longer exists", name)
			}
		}
	}
	sort.Strings(keyed)
	got := strings.Join(keyed, "\n") + "\n"
	if os.Getenv("UPDATE_TENANT_ISOLATION") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tablesFile, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(tablesFile)
	if err != nil {
		t.Fatalf("%s is missing: run with UPDATE_TENANT_ISOLATION=1 (%v)", tablesFile, err)
	}
	if string(want) != got {
		t.Errorf("the tenant-keyed table list changed; review the new tables' queries, then regenerate %s with UPDATE_TENANT_ISOLATION=1", tablesFile)
	}
}

var (
	sqlLiteral = regexp.MustCompile("`([^`]*)`")
	tableUse   = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE|INTO)\s+iam_v2\.([a-z_0-9]+)\b`)
	tenantWord = regexp.MustCompile(`(?i)tenant_id`)
)

type queryKey struct{ file, hash, snippet string }

// scanQueries returns every SQL literal that uses a tenant-keyed table without mentioning tenant_id.
func scanQueries(t *testing.T, tables map[string]bool) []queryKey {
	t.Helper()
	var out []queryKey
	seen := map[string]bool{}
	for _, root := range []string{"../../cmd", "../../internal"} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			rel := filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(p), "../../"))
			for _, m := range sqlLiteral.FindAllStringSubmatch(string(b), -1) {
				q := m[1]
				if !strings.Contains(q, "iam_v2.") || tenantWord.MatchString(q) {
					continue
				}
				touches := false
				for _, u := range tableUse.FindAllStringSubmatch(q, -1) {
					if tables[u[1]] {
						touches = true
					}
				}
				if !touches {
					continue
				}
				norm := strings.Join(strings.Fields(q), " ")
				sum := sha256.Sum256([]byte(norm))
				k := queryKey{file: rel, hash: hex.EncodeToString(sum[:])[:12], snippet: shorten(norm)}
				if id := k.file + "\t" + k.hash; !seen[id] {
					seen[id] = true
					out = append(out, k)
				}
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].hash < out[j].hash
	})
	return out
}

func shorten(s string) string {
	if len(s) > 90 {
		return s[:90]
	}
	return s
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s is missing: run TestIAMv2CatalogEveryTableCarriesItsTenantAndSite and this test with UPDATE_TENANT_ISOLATION=1 (%v)", path, err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func TestIAMv2QueryPathsFilterByTenantOrAreInTheReviewedList(t *testing.T) {
	tables := map[string]bool{}
	if os.Getenv("UPDATE_TENANT_ISOLATION") != "1" {
		for _, n := range readLines(t, tablesFile) {
			tables[n] = true
		}
	} else if b, err := os.ReadFile(tablesFile); err == nil {
		for _, n := range strings.Fields(string(b)) {
			tables[n] = true
		}
	}
	found := scanQueries(t, tables)
	if os.Getenv("UPDATE_TENANT_ISOLATION") == "1" {
		var sb strings.Builder
		sb.WriteString("# Reviewed SQL literals that touch a tenant-keyed iam_v2 table without mentioning tenant_id (audit AD-12).\n")
		sb.WriteString("# A RATCHET: entries may be removed (fix the query), never added without a reviewer's decision.\n")
		sb.WriteString("# file<TAB>hash<TAB>start of the statement\n")
		for _, k := range found {
			fmt.Fprintf(&sb, "%s\t%s\t%s\n", k.file, k.hash, k.snippet)
		}
		if err := os.WriteFile(unfilteredFile, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	reviewed := map[string]bool{}
	for _, l := range readLines(t, unfilteredFile) {
		f := strings.SplitN(l, "\t", 3)
		if len(f) >= 2 {
			reviewed[f[0]+"\t"+f[1]] = true
		}
	}
	have := map[string]bool{}
	for _, k := range found {
		id := k.file + "\t" + k.hash
		have[id] = true
		if !reviewed[id] {
			t.Errorf("%s: this query reads or writes a tenant-keyed iam_v2 table without filtering by tenant_id:\n    %s\n"+
				"  Add the tenant (and site) filter. If the row is reached by a key an earlier tenant-filtered step produced, say so in review and record it with UPDATE_TENANT_ISOLATION=1.", k.file, k.snippet)
		}
	}
	for id := range reviewed {
		if !have[id] {
			t.Errorf("%s is in %s but no longer exists: regenerate the list (the ratchet only shrinks)", strings.ReplaceAll(id, "\t", " "), unfilteredFile)
		}
	}
}

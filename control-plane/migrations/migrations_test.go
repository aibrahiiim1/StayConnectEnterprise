// Package migrations holds Central's SQL migrations. These tests guard properties that must never regress:
// 0046 refused to drop a table that held data and archived commercial history; 0047 deletes that archive by
// Product-Owner decision but never takes a live table with it, keeps the signed licence format, and hard-codes
// no appliance; and every up has a down.
package migrations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func Test0046GuardRefusesToDropData(t *testing.T) {
	up := read(t, "0046_central_redesign.up.sql")
	guardAt := strings.Index(up, "RAISE EXCEPTION")
	if guardAt < 0 || !strings.Contains(up, "count(*)") {
		t.Fatal("0046 up has no row-count guard that raises")
	}
	firstDrop := regexp.MustCompile(`(?m)^DROP TABLE`).FindStringIndex(up)
	if firstDrop == nil || firstDrop[0] < guardAt {
		t.Fatal("the guard must run before the first DROP TABLE")
	}
	// Every dropped table is in the guard's list.
	guard := up[:strings.Index(up, "] LOOP")]
	for _, m := range regexp.MustCompile(`(?m)^DROP TABLE IF EXISTS (\w+);`).FindAllStringSubmatch(up, -1) {
		if !strings.Contains(guard, "'"+m[1]+"'") {
			t.Errorf("table %s is dropped but not checked by the guard", m[1])
		}
	}
	if strings.Contains(strings.ToUpper(up), " CASCADE;") {
		t.Error("0046 must not DROP ... CASCADE: an unexpected dependency has to abort, not be dropped with it")
	}
}

func Test0046ArchivesCommercialHistory(t *testing.T) {
	up := read(t, "0046_central_redesign.up.sql")
	for _, tbl := range []string{"plans", "plan_limits", "plan_limit_history", "subscription_events"} {
		if !strings.Contains(up, "public."+tbl+" ") || !regexp.MustCompile(`public\.`+tbl+`\s+SET SCHEMA legacy_archive`).MatchString(up) {
			t.Errorf("%s must be moved to legacy_archive", tbl)
		}
		if regexp.MustCompile(`DROP TABLE IF EXISTS ` + tbl + `;`).MatchString(up) {
			t.Errorf("%s must not be dropped", tbl)
		}
	}
	down := read(t, "0046_central_redesign.down.sql")
	for _, tbl := range []string{"plans", "plan_limits", "plan_limit_history", "subscription_events"} {
		if !regexp.MustCompile(`legacy_archive\.` + tbl + `\s+SET SCHEMA public`).MatchString(down) {
			t.Errorf("down must move %s back to public", tbl)
		}
	}
}

func Test0046DownRecreatesEveryDroppedTable(t *testing.T) {
	up := read(t, "0046_central_redesign.up.sql")
	down := read(t, "0046_central_redesign.down.sql")
	for _, m := range regexp.MustCompile(`(?m)^DROP TABLE IF EXISTS (\w+);`).FindAllStringSubmatch(up, -1) {
		if !strings.Contains(down, "CREATE TABLE public."+m[1]+" (") {
			t.Errorf("down does not recreate %s", m[1])
		}
	}
	if !strings.Contains(down, "create_hypertable('public.accounting_records'") {
		t.Error("down must restore accounting_records as a hypertable")
	}
}

func TestEveryUpFrom0037HasADown(t *testing.T) {
	ups, _ := filepath.Glob("*.up.sql")
	for _, u := range ups {
		if u < "0037" {
			continue // 0023–0036 predate the down-file rule
		}
		if _, err := os.Stat(strings.TrimSuffix(u, ".up.sql") + ".down.sql"); err != nil {
			t.Errorf("%s has no down migration", u)
		}
	}
}

func Test0047DropsTheArchiveBehindAGuard(t *testing.T) {
	up := read(t, "0047_central_cleanup.up.sql")
	guard := strings.Index(up, "RAISE EXCEPTION")
	drop := strings.Index(up, "DROP SCHEMA IF EXISTS legacy_archive CASCADE;")
	if guard < 0 || drop < 0 || guard > drop {
		t.Fatal("0047 must check for outside dependents (RAISE) before DROP SCHEMA legacy_archive CASCADE")
	}
	if !strings.Contains(up, "_timescaledb_catalog.hypertable") {
		t.Error("hypertables in the archive must be dropped through TimescaleDB first")
	}
	// CASCADE is used for the archive only.
	if n := strings.Count(strings.ToUpper(up), "CASCADE;"); n != 1 {
		t.Errorf("0047 uses CASCADE %d times; only the archive schema may be dropped with CASCADE", n)
	}
}

func Test0047HardCodesNoAppliance(t *testing.T) {
	for _, f := range []string{"0047_central_cleanup.up.sql", "0047_central_cleanup.down.sql"} {
		s := read(t, f)
		if regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|SC-[A-Z0-9]{4}-`).MatchString(s) {
			t.Errorf("%s names a specific appliance; deleting one is an API operation, not a migration", f)
		}
		if regexp.MustCompile(`(?i)DELETE\s+FROM\s+appliances`).MatchString(s) {
			t.Errorf("%s deletes appliances", f)
		}
	}
}

func Test0047DownRestoresStructure(t *testing.T) {
	down := read(t, "0047_central_cleanup.down.sql")
	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS legacy_archive",
		"legacy_archive.plans (", "legacy_archive.plan_limits (", "legacy_archive.plan_limit_history (",
		"legacy_archive.subscription_events (",
		"RENAME COLUMN registered_at TO enrolled_at",
		"DROP TABLE IF EXISTS retired_appliance_identities",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("0047 down is missing %q", want)
		}
	}
	up := read(t, "0047_central_cleanup.up.sql")
	for _, m := range regexp.MustCompile(`(?m)^ALTER TABLE (\w+)\s+DROP COLUMN IF EXISTS (\w+);`).FindAllStringSubmatch(up, -1) {
		if !regexp.MustCompile(`ALTER TABLE ` + m[1] + `\s+ADD COLUMN IF NOT EXISTS ` + m[2] + ` `).MatchString(down) {
			t.Errorf("0047 down does not restore %s.%s", m[1], m[2])
		}
	}
	if !strings.Contains(strings.ToUpper(down), "DOES NOT RESTORE DATA") {
		t.Error("0047 down must say that it restores structure only")
	}
}

// Package migrations holds Central's SQL migrations. This test guards the properties of 0046 that must
// never regress: it refuses to drop a table that holds data, it archives (never drops) commercial history,
// and every up has a down.
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

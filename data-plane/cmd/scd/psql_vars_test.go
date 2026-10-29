package main

import (
	"strings"
	"testing"
)

// P4 CC4: values reach psql as variables, never as text inside the statement.
func TestSupportedRestoreStatementFormatsNothingIntoSQL(t *testing.T) {
	hostile := "o'brien'); DROP TABLE x; --"
	sql, vars := supportedRestoreStatement("11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 7,
		strings.Repeat("a", 64), "2026-10-01T00:00:00Z", hostile)
	if strings.Contains(sql, "o'brien") || strings.Contains(sql, "DROP") || strings.Contains(sql, "1111") {
		t.Fatalf("a value is in the SQL text: %s", sql)
	}
	for _, name := range []string{"tenant", "site", "gen", "sha", "taken", "by"} {
		if !strings.Contains(sql, ":'"+name+"'") {
			t.Errorf("the statement does not use :'%s'", name)
		}
	}
	if vars["by"] != hostile || vars["gen"] != "7" {
		t.Fatalf("variables = %v", vars)
	}
	args := strings.Join(psqlArgs("restore_x", vars), "\x00")
	if !strings.Contains(args, "-v\x00by="+hostile) || !strings.Contains(args, "-v\x00ON_ERROR_STOP=1") {
		t.Fatalf("the variable did not travel as an argument: %q", args)
	}
}

func TestRandomHexIsNotRepeated(t *testing.T) {
	a, b := randomHex(16), randomHex(16)
	if len(a) != 32 || a == b {
		t.Fatalf("%q %q", a, b)
	}
}

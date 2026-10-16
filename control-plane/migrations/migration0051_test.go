package migrations

import (
	"regexp"
	"strings"
	"testing"
)

// 0051 lets a retired serial come back as a NEW row: the global unique on serial becomes unique among live
// rows only. It must delete nothing, and its down must refuse (not delete) when duplicates exist.
func Test0051SerialUniqueAmongLiveRowsOnly(t *testing.T) {
	up := read(t, "0051_retired_serial_new_row_and_operator_sessions.up.sql")
	for _, want := range []string{
		"ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_serial_key;",
		"ON appliances (serial) WHERE lifecycle_state NOT IN ('revoked','decommissioned');",
		"ALTER TABLE operators ADD COLUMN IF NOT EXISTS sessions_not_before timestamptz;",
		"CHECK (operator_session_max_hours BETWEEN 1 AND 168)",
		"operator_session_max_hours  int NOT NULL DEFAULT 24",
		"ALTER TABLE licenses ADD COLUMN IF NOT EXISTS revocation_delivered_at timestamptz;",
		"ALTER TABLE appliance_certificates ADD COLUMN IF NOT EXISTS superseded_at timestamptz;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("0051 up is missing %q", want)
		}
	}
	code := strings.ToUpper(regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(up, ""))
	for _, bad := range []string{"DELETE ", "UPDATE ", "DROP TABLE"} {
		if strings.Contains(code, bad) {
			t.Errorf("0051 up must not contain %q", bad)
		}
	}
	down := read(t, "0051_retired_serial_new_row_and_operator_sessions.down.sql")
	guard, restore := strings.Index(down, "RAISE EXCEPTION"), strings.Index(down, "ADD CONSTRAINT appliances_serial_key UNIQUE (serial)")
	if guard < 0 || restore < 0 || guard > restore {
		t.Fatal("0051 down must refuse duplicates before restoring the global unique serial")
	}
	if regexp.MustCompile(`(?i)DELETE\s+FROM\s+appliances`).MatchString(down) {
		t.Fatal("0051 down must never delete appliance history")
	}
}

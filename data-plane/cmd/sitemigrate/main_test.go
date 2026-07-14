package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestIntersectColumns(t *testing.T) {
	cases := []struct {
		name     string
		src, dst []string
		want     []string
	}{
		{
			name: "dest has extra edge-only column (tenants.branding)",
			src:  []string{"id", "slug", "name", "auth_methods"},
			dst:  []string{"id", "slug", "name", "auth_methods", "branding"},
			want: []string{"id", "slug", "name", "auth_methods"},
		},
		{
			name: "source has columns edge lacks",
			src:  []string{"id", "tenant_id", "cloud_only_col", "created_at"},
			dst:  []string{"id", "tenant_id", "created_at"},
			want: []string{"id", "tenant_id", "created_at"},
		},
		{
			name: "source order is preserved",
			src:  []string{"c", "a", "b"},
			dst:  []string{"a", "b", "c"},
			want: []string{"c", "a", "b"},
		},
		{
			name: "no overlap",
			src:  []string{"x"},
			dst:  []string{"y"},
			want: []string{},
		},
	}
	for _, tc := range cases {
		got := intersectColumns(tc.src, tc.dst)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: intersectColumns(%v, %v) = %v, want %v", tc.name, tc.src, tc.dst, got, tc.want)
		}
	}
}

// TestFKOrder proves the migration list is FK-safe: every declared FK target
// appears strictly BEFORE the table that references it, so parents always
// exist in dest by the time children are inserted.
func TestFKOrder(t *testing.T) {
	pos := map[string]int{}
	for i, s := range migrationTables {
		if _, dup := pos[s.Name]; dup {
			t.Fatalf("table %s listed twice", s.Name)
		}
		pos[s.Name] = i
	}
	for i, s := range migrationTables {
		for _, fk := range s.FKs {
			j, ok := pos[fk.RefTable]
			if !ok {
				t.Errorf("%s.%s references %s, which is not in the migration set", s.Name, fk.Col, fk.RefTable)
				continue
			}
			if j >= i {
				t.Errorf("FK order violated: %s (index %d) references %s (index %d) which is not copied first",
					s.Name, i, fk.RefTable, j)
			}
		}
	}
}

// TestMigrationTableSet pins the exact table set so an accidental edit is
// caught, and checks per-spec invariants.
func TestMigrationTableSet(t *testing.T) {
	want := []string{
		"tenants", "sites", "appliances", "operators", "operator_roles",
		"ticket_templates", "voucher_batches", "vouchers", "guests", "sessions",
		"accounting_records", "auth_otps", "social_oauth_states", "pms_providers",
		"pms_attempts", "walled_garden_rules", "notification_providers",
		"social_oauth_providers", "stripe_accounts", "payments", "stripe_events",
		"audit_log",
	}
	if len(migrationTables) != len(want) {
		t.Fatalf("expected %d tables, got %d", len(want), len(migrationTables))
	}
	for i, s := range migrationTables {
		if s.Name != want[i] {
			t.Errorf("table %d: got %s, want %s", i, s.Name, want[i])
		}
		if !strings.Contains(s.Where, "$1") {
			t.Errorf("%s: Where must scope by tenant ($1): %q", s.Name, s.Where)
		}
	}
	// The two PK-less hypertables are exactly accounting_records + audit_log.
	var noPK []string
	for _, s := range migrationTables {
		if s.PK == "" {
			noPK = append(noPK, s.Name)
		}
	}
	if !reflect.DeepEqual(noPK, []string{"accounting_records", "audit_log"}) {
		t.Errorf("PK-less tables = %v, want [accounting_records audit_log]", noPK)
	}
}

func TestWhereArgs(t *testing.T) {
	if got := whereArgs("tenant_id = $1", "T", "S"); !reflect.DeepEqual(got, []any{"T"}) {
		t.Errorf("tenant-only predicate: got %v", got)
	}
	if got := whereArgs("tenant_id = $1 AND site_id = $2", "T", "S"); !reflect.DeepEqual(got, []any{"T", "S"}) {
		t.Errorf("tenant+site predicate: got %v", got)
	}
}

func TestInlineParams(t *testing.T) {
	got := inlineParams("tenant_id = $1 AND (site_id IS NULL OR site_id = $2)",
		"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	want := "tenant_id = '11111111-1111-1111-1111-111111111111'::uuid AND (site_id IS NULL OR site_id = '22222222-2222-2222-2222-222222222222'::uuid)"
	if got != want {
		t.Errorf("inlineParams:\n got %s\nwant %s", got, want)
	}
}

func TestIsUUID(t *testing.T) {
	if !isUUID("a3bb189e-8bf9-3888-9912-ace4e6543002") {
		t.Error("valid UUID rejected")
	}
	for _, bad := range []string{"", "not-a-uuid", "a3bb189e8bf938889912ace4e6543002", "xyz;DROP TABLE tenants--"} {
		if isUUID(bad) {
			t.Errorf("invalid UUID accepted: %q", bad)
		}
	}
}

func TestBuildInsertSQL(t *testing.T) {
	sql := buildInsertSQL("guests", []string{"id", "mac"}, []string{"uuid", "macaddr"}, "id", 2)
	want := "INSERT INTO guests (id, mac) VALUES ($1::uuid,$2::macaddr),($3::uuid,$4::macaddr) ON CONFLICT (id) DO NOTHING"
	if sql != want {
		t.Errorf("with PK:\n got %s\nwant %s", sql, want)
	}
	sql = buildInsertSQL("audit_log", []string{"ts"}, []string{"timestamp with time zone"}, "", 1)
	want = "INSERT INTO audit_log (ts) VALUES ($1::timestamp with time zone)"
	if sql != want {
		t.Errorf("no PK:\n got %s\nwant %s", sql, want)
	}
	if containsForbidden(sql) {
		t.Errorf("insert SQL flagged as destructive: %s", sql)
	}
}

func TestBatchSizeFor(t *testing.T) {
	if got := batchSizeFor(1000, 6); got != 1000 {
		t.Errorf("small table: got %d, want 1000", got) // accounting_records: 6 cols
	}
	if got := batchSizeFor(1000, 100); got != 600 {
		t.Errorf("wide table capped: got %d, want 600", got)
	}
	if got := batchSizeFor(1000, maxBindParams+1); got != 1 {
		t.Errorf("degenerate width: got %d, want 1", got)
	}
}

// TestNothingDestructive asserts the never-TRUNCATE/never-DELETE invariant
// over every piece of SQL the tool can generate from its table specs.
func TestNothingDestructive(t *testing.T) {
	for _, s := range migrationTables {
		for n := 1; n <= 2; n++ {
			sql := buildInsertSQL(s.Name, []string{"id"}, []string{"uuid"}, s.PK, n)
			if containsForbidden(sql) {
				t.Errorf("%s: generated SQL is destructive: %s", s.Name, sql)
			}
		}
		if containsForbidden(s.Where) {
			t.Errorf("%s: Where predicate is destructive: %s", s.Name, s.Where)
		}
	}
}

func TestRedactDSN(t *testing.T) {
	got := redactDSN("postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect")
	want := "postgres://stayconnect:***@127.0.0.1:5432/stayconnect"
	if got != want {
		t.Errorf("url dsn: got %s, want %s", got, want)
	}
	got = redactDSN("host=localhost password=hunter2 dbname=stayconnect_site")
	if strings.Contains(got, "hunter2") {
		t.Errorf("keyword dsn still contains password: %s", got)
	}
}

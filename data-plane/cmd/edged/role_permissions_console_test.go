package main

// The Admin Console's role table is generated from rolePerms by this test, which fails when
// hotel-admin/lib/role-permissions.generated.json differs from it byte for byte. Regenerate after changing rolePerms:
//
//	UPDATE_ROLE_PERMISSIONS=1 go test ./cmd/edged -run TestConsoleRolePermissionsAreGeneratedFromTheServerTable
//
// site_admin is not a row: it holds write on everything. Legacy tenant_* aliases resolve the same way on both sides.

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

const consoleRolePermissionsFile = "../../../hotel-admin/lib/role-permissions.generated.json"

func renderConsoleRolePermissions(t *testing.T) []byte {
	t.Helper()
	roles := map[string]map[string]string{}
	for role, perms := range rolePerms {
		if perms == nil { // the legacy aliases
			continue
		}
		row := map[string]string{}
		for res, p := range perms {
			switch p {
			case permRead:
				row[res] = "read"
			case permWrite:
				row[res] = "write"
			}
		}
		roles[role] = row
	}
	doc := map[string]any{
		"_generated": "GENERATED from data-plane/cmd/edged/auth.go rolePerms by " +
			"role_permissions_console_test.go. Do not edit: run UPDATE_ROLE_PERMISSIONS=1 go test ./cmd/edged " +
			"-run TestConsoleRolePermissionsAreGeneratedFromTheServerTable",
		"roles": roles,
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestConsoleRolePermissionsAreGeneratedFromTheServerTable(t *testing.T) {
	want := renderConsoleRolePermissions(t)
	if os.Getenv("UPDATE_ROLE_PERMISSIONS") == "1" {
		if err := os.WriteFile(consoleRolePermissionsFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(consoleRolePermissionsFile)
	if err != nil {
		t.Fatalf("the console's role table is missing (%v); generate it with UPDATE_ROLE_PERMISSIONS=1", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatal("hotel-admin/lib/role-permissions.generated.json does not match auth.go rolePerms: the menu would " +
			"offer screens the server refuses, or hide ones it serves. Regenerate it with " +
			"UPDATE_ROLE_PERMISSIONS=1 go test ./cmd/edged -run TestConsoleRolePermissionsAreGeneratedFromTheServerTable")
	}
}

func TestRoleDrift_TheDocumentedGrantsAreServed(t *testing.T) {
	for _, c := range []struct {
		role, resource string
		want           perm
	}{
		{"front_office_operator", "usage", permRead},
		{"guest_relations_operator", "usage", permRead},
		{"payments_operator", "usage", permRead},
		{"payments_operator", "commercial-packages", permRead},
		{"hotel_it_manager", "commercial-packages", permWrite},
	} {
		if !permFor([]string{c.role}, c.resource, c.want) {
			t.Errorf("%s lacks %v on %s", c.role, c.want, c.resource)
		}
	}
	// ...and nothing wider than read where the role is read-only.
	for _, c := range [][2]string{{"payments_operator", "commercial-packages"}, {"front_office_operator", "usage"}} {
		if permFor([]string{c[0]}, c[1], permWrite) {
			t.Errorf("%s holds WRITE on %s", c[0], c[1])
		}
	}
	if permFor([]string{"voucher_operator"}, "usage", permRead) {
		t.Error("voucher_operator reads usage")
	}
}

func TestUsageAndPackagesMatchTheDocumentedRoleMatrix(t *testing.T) {
	for _, resource := range []string{"usage", "commercial-packages"} {
		row := docMatrixRow(t, resource)
		for role := range rolePerms {
			if role == "tenant_admin" || role == "tenant_operator" {
				continue
			}
			cell, ok := row[role]
			if !ok {
				t.Errorf("%s: the documented row says nothing about %s", resource, role)
				continue
			}
			read := permFor([]string{role}, resource, permRead)
			write := permFor([]string{role}, resource, permWrite)
			switch cell {
			case "W":
				if !write {
					t.Errorf("%s is documented W on %s but the code refuses the write", role, resource)
				}
			case "R":
				if !read || write {
					t.Errorf("%s is documented R on %s; code read=%v write=%v", role, resource, read, write)
				}
			case "–", "-", "—":
				if read || write {
					t.Errorf("%s is documented – on %s; code read=%v write=%v", role, resource, read, write)
				}
			default:
				t.Errorf("unrecognised cell %q for %s on %s", cell, role, resource)
			}
		}
	}
}
